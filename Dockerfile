# syntax=docker/dockerfile:1

# Base: Go toolchain and module cache, shared by every stage.
FROM golang:1.27.3-alpine3.24@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS base
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Dev: hot reload with air; the source is bind-mounted by docker compose.
FROM base AS dev
RUN go install github.com/air-verse/air@v1.67.4
EXPOSE 8080 9464
CMD ["air", "-c", ".air.toml"]

# Build: static, stripped binary with the version stamped in.
FROM base AS build
ARG VERSION=dev
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/api ./cmd/api

# Runtime: distroless, no shell, non-root.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329 AS runtime
COPY --from=build /out/api /api
USER nonroot:nonroot
EXPOSE 8080 9464
HEALTHCHECK --interval=15s --timeout=3s --start-period=20s --retries=3 CMD ["/api", "healthcheck"]
ENTRYPOINT ["/api"]
