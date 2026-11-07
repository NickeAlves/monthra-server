# monthra-server

The Monthra API: personal finance (expenses, income, investments and Open Finance connections) in **Go**, with **MongoDB** and **Redis**.

> Status: service skeleton (config, structured logs, metrics, tracing, health probes, MongoDB indexes, Docker). Auth and the domain features come next; see the roadmap in [CLAUDE.md](CLAUDE.md) §10.

## Stack

Go 1.27 · chi · mongo-driver v2 · go-redis v9 · slog (JSON) · Prometheus · OpenTelemetry (OTLP/HTTP) · testify + testcontainers-go · golangci-lint, gosec, govulncheck, gitleaks, Trivy.

## Running locally

Requirements: Docker (with Compose v2) and, to run outside Docker, Go 1.27 (any Go 1.21+ downloads the right toolchain by itself).

```bash
cp .env.example .env     # then replace every change-me value
make up                  # creates the monthra-net network, builds and starts everything
curl localhost:8080/readyz
```

| Service | Address (host) | Notes |
|---|---|---|
| API | http://localhost:8080 | Hot reload with `air`; OpenAPI at `/docs/openapi.yaml` |
| Metrics | http://localhost:9464/metrics | Internal port, never exposed through the public proxy |
| MongoDB | localhost:27017 | The API connects with a least-privilege user (`readWrite` on `monthra` only) |
| Redis | localhost:6379 | Password from `.env` |
| Mailpit | http://localhost:8025 | Catches every email sent locally |
| mongo-express | http://localhost:8081 | Only with `docker compose --profile tools up -d`; basic auth from `.env` |

Host ports bind to `127.0.0.1` only. The MongoDB users are created when the `mongo-data` volume is first initialized; after changing the Mongo passwords in `.env`, recreate the volume with `docker compose down -v`.

To run the API on the host instead (Mongo and Redis still in Docker): `make run`.

### Make targets

| Target | What it does |
|---|---|
| `make up` / `make down` | Start / stop the local environment |
| `make logs` / `make ps` | Follow logs / service status |
| `make run` | Run the API on the host |
| `make test` | Unit tests with the race detector |
| `make test-integration` | Unit + integration tests (testcontainers starts real MongoDB and Redis; needs Docker) |
| `make cover` | All tests with a cross-package coverage report |
| `make lint` | gofmt, go vet, golangci-lint (with gosec), govulncheck, gitleaks |
| `make build` | Build the production image (`ghcr.io/nickealves/monthra-server`) |
| `make staging-up` / `make staging-down` | Homologation stack locally |

Run `make lint` and `make test` before every commit.

## Homologation (staging) locally

```bash
make staging-up
```

This applies `docker-compose.staging.yml` over the base file: the distroless runtime image (non-root, read-only filesystem, `no-new-privileges`), `APP_ENV=staging` (adds HSTS), no hot reload, no Mailpit or mongo-express, and MongoDB/Redis with no host ports. TLS terminates at the proxy in `monthra-infra`. The real staging deploy comes from the `staging` branch through `monthra-infra`.

## Configuration

Everything comes from environment variables (see [.env.example](.env.example)); the service refuses to start with an invalid configuration.

| Variable | Default | Notes |
|---|---|---|
| `APP_ENV` | `local` | `local`, `staging` or `production` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `HTTP_ADDR` / `METRICS_ADDR` | `:8080` / `:9464` | Must differ |
| `HTTP_READ_HEADER_TIMEOUT`, `HTTP_READ_TIMEOUT`, `HTTP_WRITE_TIMEOUT`, `HTTP_IDLE_TIMEOUT`, `HTTP_SHUTDOWN_TIMEOUT` | `5s`, `15s`, `30s`, `60s`, `20s` | |
| `HTTP_MAX_BODY_BYTES` | `1048576` | Larger bodies get 413 |
| `MONGO_URI` | (required) | |
| `MONGO_DATABASE` | `monthra` | |
| `REDIS_ADDR` | (required) | |
| `REDIS_PASSWORD` | | Required outside `local` |
| `AUDIT_LOG_TTL` | `8760h` | Audit log retention (TTL index) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | | Empty disables trace export; the standard `OTEL_*` variables apply |

## API conventions

- Base path `/api/v1`, JSON in camelCase. `/healthz` (liveness, no dependencies) and `/readyz` (MongoDB and Redis) live at the root.
- Errors are problem details (`application/problem+json`) with a stable `code`, translated `title`/`detail` (pt-BR, es-ES, en-US from `Accept-Language`) and the `requestId`.
- Every response carries `X-Request-ID` and the security headers (CSP, nosniff, frame denial, referrer and permissions policies, COOP/CORP, `Cache-Control: no-store`; HSTS outside `local`).
- The OpenAPI document is [docs/openapi.yaml](docs/openapi.yaml), served at `/docs` outside production. Keep it in sync with the code.

## Observability

- Logs: one JSON line per event on stdout, with `request_id`, `trace_id`/`span_id` and (once auth exists) `user_id`. Sensitive keys (password, token, secret, cookie, authorization, cpf...) are redacted; access logs leave out query strings, IPs and user agents.
- Metrics: `monthra_http_requests_total`, `monthra_http_request_duration_seconds` (by chi route pattern; unmatched paths share one label), `monthra_http_requests_in_flight`, `monthra_build_info`, plus Go and process metrics.
- Traces: HTTP server spans named after the route, MongoDB command spans (operation and collection only, never query values) and Redis spans.

## Layout

```
cmd/api/              entrypoint (and the `healthcheck` subcommand for the image)
internal/app/         wiring, servers and graceful shutdown
internal/config/      environment configuration
internal/http/        router, servers, handler/, middleware/, problem/
internal/i18n/        locale matching and translated messages
internal/observability/  logger, metrics, tracing
internal/repository/mongo/  MongoDB client and command tracing
internal/cache/       Redis client and key naming (monthra:{env}:...)
migrations/           MongoDB indexes, applied at boot
docs/                 OpenAPI document (embedded in the binary)
deploy/mongo/         MongoDB init script (least-privilege app user)
```

## Branches

- `main`: production. `staging`: homologation. `develop`: development. All three accept changes only through pull requests.
- Work on `feature/<name>` or `fix/<name>` branched from `develop`, then open a PR to `develop`.
- Promotion: PR `develop` -> `staging` -> `main`. Releases are `vX.Y.Z` tags on `main`.
- Commits follow [Conventional Commits](https://www.conventionalcommits.org/).

## Related repositories

| Repository | Purpose |
|---|---|
| [monthra-server](https://github.com/NickeAlves/monthra-server) | Go API, MongoDB, Redis, auth, business rules, metrics and traces |
| [monthra-app](https://github.com/NickeAlves/monthra-app) | Flutter app (web, Android, iOS) |
| [monthra-infra](https://github.com/NickeAlves/monthra-infra) | Terraform, observability stack, integrated compose, CI/CD |
