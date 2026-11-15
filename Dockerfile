# syntax=docker/dockerfile:1

# Base: Go toolchain and module cache, shared by every stage.
FROM golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS base
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
