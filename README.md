# monthra-server

The Monthra API: personal finance (expenses, income, investments and Open Finance connections) in **Go**, with **MongoDB** and **Redis**.

> Status: service skeleton (config, structured logs, metrics, tracing, health probes, MongoDB indexes, Docker), browser-facing protections (CORS, CSRF, rate limiting, client IP behind proxies) and authentication (sign-up, email verification, login, refresh rotation, logout). Password recovery, MFA, the profile and the domain features come next; see the roadmap in [CLAUDE.md](CLAUDE.md) §10.

## Stack

Go 1.27 · chi · mongo-driver v2 · go-redis v9 · slog (JSON) · Prometheus · OpenTelemetry (OTLP/HTTP) · testify + testcontainers-go · golangci-lint, gosec, govulncheck, gitleaks, Trivy.

## Running locally

Requirements: Docker (with Compose v2) and, to run outside Docker, Go 1.27 (any Go 1.21+ downloads the right toolchain by itself).

```bash
cp .env.example .env     # then replace every change-me value
make secrets             # prints random values for the secret variables (CSRF_SECRET, JWT keys)
make up                  # creates the monthra-net network, builds and starts everything
curl localhost:8080/readyz
```

| Service | Address (host) | Notes |
|---|---|---|
| API | http://localhost:8080 | Hot reload with `air`; OpenAPI at `/docs/openapi.yaml` |
| Metrics | http://localhost:9464/metrics | Internal port, never exposed through the public proxy |
| MongoDB | localhost:27017 | The API connects with a least-privilege user (`readWrite` on `monthra` only) |
| Redis | localhost:6379 | Password from `.env` |
| Mailpit | http://localhost:8025 | Catches every email sent locally (SMTP on localhost:1025) |
| mongo-express | http://localhost:8081 | Only with `docker compose --profile tools up -d`; basic auth from `.env` |

Host ports bind to `127.0.0.1` only. The MongoDB users are created when the `mongo-data` volume is first initialized; after changing the Mongo passwords in `.env`, recreate the volume with `docker compose down -v`.

To run the API on the host instead (Mongo, Redis and Mailpit still in Docker): `make run`.

### Make targets

| Target | What it does |
|---|---|
| `make up` / `make down` | Start / stop the local environment |
| `make secrets` | Print random values for the secret variables of `.env` |
| `make logs` / `make ps` | Follow logs / service status |
| `make run` | Run the API on the host |
| `make test` | Unit tests with the race detector |
| `make test-integration` | Unit + integration tests (testcontainers starts real MongoDB, Redis and Mailpit; needs Docker) |
| `make cover` | All tests with a cross-package coverage report |
| `make lint` | gofmt, go vet, golangci-lint (with gosec), govulncheck, gitleaks |
| `make build` | Build the production image (`ghcr.io/nickealves/monthra-server`) |
| `make staging-up` / `make staging-down` | Homologation stack locally |

Run `make lint` and `make test` before every commit.

## Homologation (staging) locally

```bash
make staging-up
```

This applies `docker-compose.staging.yml` over the base file: the distroless runtime image (non-root, read-only filesystem, `no-new-privileges`), `APP_ENV=staging` (adds HSTS, `Secure` cookies with the `__Host-`/`__Secure-` prefixes and https-only CORS origins, set with `STAGING_CORS_ALLOWED_ORIGINS`), no hot reload, no mongo-express, and MongoDB/Redis with no host ports. Emails still go to Mailpit (UI on `localhost:8025`) through `SMTP_ALLOW_INSECURE=true`, until the email provider is chosen together with the cloud. A real staging deploy configures its SMTP relay in Terraform and never sets that flag. TLS terminates at the proxy in `monthra-infra`. The real staging deploy comes from the `staging` branch through `monthra-infra`.

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
| `CORS_ALLOWED_ORIGINS` | | Web app origins allowed from browsers, comma-separated, exact (no wildcards). Required and https-only outside `local`. List the app origin even when it shares the API origin |
| `HTTP_TRUSTED_PROXIES` | | Proxies (CIDRs or addresses) whose `X-Forwarded-For` is trusted; empty means the TCP peer is the client |
| `CSRF_SECRET` | (required) | Base64, at least 32 bytes (`make secrets`) |
| `RATE_LIMIT_API_IP` | `300/m` | Baseline budget of `/api/v1` per client IP (`<n>/s`, `<n>/m`, `<n>/h`) |
| `RATE_LIMIT_API_USER` | `100/m` | Budget of authenticated requests per user |
| `RATE_LIMIT_AUTH_IP` / `RATE_LIMIT_AUTH_EMAIL` | `5/m` / `5/m` | Register, login (and its MFA step), email verification and password recovery together, per client IP and per target address |
| `RATE_LIMIT_REFRESH_IP` | `30/m` | Token refreshes per client IP |
| `RATE_LIMIT_AUTH_MAIL` | `5/h` | Account emails (verification, sign-up attempt, password reset) one address receives |
| `JWT_SIGNING_KEYS` | (required) | Ed25519 seeds as `kid:base64` pairs, comma-separated (`make secrets`) |
| `JWT_ACTIVE_KID` | (required) | The kid that signs new tokens; the others only verify |
| `ACCESS_TOKEN_TTL` / `REFRESH_TOKEN_TTL` | `15m` / `720h` | Access: 1 to 15 minutes. Refresh: 1 hour to 90 days |
| `REFRESH_REUSE_GRACE` | `10s` | A rotated refresh token reused this soon answers 409 instead of revoking the session (0 to 1 minute) |
| `EMAIL_TOKEN_TTL` | `30m` | Lifetime of verification links (5 to 60 minutes) |
| `RESET_TOKEN_TTL` | `15m` | Lifetime of password reset links (5 to 30 minutes) |
| `ENCRYPTION_KEYS` | (required) | AES-256 keys (32 bytes) as `kid:base64` pairs, comma-separated (`make secrets`); never the CSRF or JWT keys |
| `ENCRYPTION_ACTIVE_KID` | (required) | The kid that encrypts new values; the others only decrypt |
| `PASSWORD_HASH_CONCURRENCY` | `4` | Parallel Argon2id hashes (64 MiB each) |
| `HIBP_ENABLED` | `true` | Reject new passwords found in Have I Been Pwned (k-anonymity) |
| `APP_PUBLIC_URL` | `http://localhost:3000` | Web app base URL used in email links; https outside `local` |
| `TURNSTILE_SECRET` | | Cloudflare Turnstile secret; empty disables the captcha |
| `SMTP_HOST`, `SMTP_PORT` | (required), `587` | SMTP relay (Mailpit locally) |
| `SMTP_USERNAME`, `SMTP_PASSWORD` | | Credentials require TLS |
| `SMTP_FROM` | (required) | `Name <address>` or a bare address |
| `SMTP_TLS` | `starttls` | `none` (local only), `starttls` or `tls` |
| `SMTP_ALLOW_INSECURE` | `false` | Allows `SMTP_TLS=none` in staging (local homologation with Mailpit); refused in production |
| `AUDIT_LOG_TTL` | `8760h` | Audit log retention (TTL index) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | | Empty disables trace export; the standard `OTEL_*` variables apply |

## API conventions

- Base path `/api/v1`, JSON in camelCase. `/healthz` (liveness, no dependencies) and `/readyz` (MongoDB and Redis) live at the root.
- Errors are problem details (`application/problem+json`) with a stable `code`, translated `title`/`detail` (pt-BR, es-ES, en-US from `Accept-Language`) and the `requestId`.
- Every response carries `X-Request-ID` and the security headers (CSP, nosniff, frame denial, referrer and permissions policies, COOP/CORP, `Cache-Control: no-store`; HSTS outside `local`).
- The OpenAPI document is [docs/openapi.yaml](docs/openapi.yaml), served at `/docs` outside production. Keep it in sync with the code.

## Authentication

The flows and their answers are documented in [docs/openapi.yaml](docs/openapi.yaml). In short:

- **Sign-up** answers `202` whatever the address. A new address gets a verification link and a taken one gets a "someone tried to sign up" email, so the API never tells who has an account. The link puts its token in the URL fragment (`/verify-email#token=…`), so the token stays out of server logs and `Referer` headers. The web app must use Flutter's path URL strategy, so that the fragment stays free for the token.
- **Passwords:** 10 to 1024 characters with no composition rules, checked against Have I Been Pwned, hashed with Argon2id (m=64 MiB, t=3, p=2). Hashes made with older parameters are upgraded on the next login.
- **Login** needs a verified email and answers wrong addresses and wrong passwords alike, after the same work. Failures lock the address after 5 tries (and an IP after 20), for 1 minute doubling to 15. With Turnstile on, a captcha is asked after 3 failures, or after 3 sign-ups from one IP within an hour.
- **Tokens:**
  - Access tokens are EdDSA JWTs (15 minutes, `kid` for rotation). The `iss` claim is `monthra-server:{env}`, so a staging token is never accepted in production.
  - Refresh tokens are opaque, stored as SHA-256 in MongoDB (`refresh_tokens`, one chain per session in `sessions`), and rotate on every use. A rotated token presented again within the grace window answers `409` (two tabs refreshing together); later, it revokes the whole session.
  - Logout and revocations go to a Redis denylist (`monthra:{env}:jwt:deny:{jti}`, `monthra:{env}:session:revoked:{sid}`), checked on every authenticated request. It fails closed.
- **Delivery:**
  - Browsers get `HttpOnly`, `Secure`, `SameSite=Strict` cookies and never see a token: `__Host-monthra_at` on `/`, and `__Secure-monthra_rt` on `/api/v1/auth/refresh` (`__Host-` requires `Path=/`).
  - Native clients get the tokens as JSON and send `Authorization: Bearer`.
- **Password recovery:** `forgot-password` answers `202` whatever the address and emails a single-use link (`/reset-password#token=…`, 15 minutes) only to an existing account; a new request voids the earlier links. The reset applies the password policy and HIBP before spending the token, marks the address verified (the link proves control of the inbox), revokes every session in MongoDB and Redis and sends a "password changed" notice. One address gets at most `RATE_LIMIT_AUTH_MAIL` account emails, checked before the lookup, so the forms cannot flood someone else's inbox.
- **Two-step verification (TOTP, RFC 6238):**
  - SHA-1, 6 digits, 30 seconds and one step of drift each way, which every authenticator app supports. `mfa/setup` asks for the password again (a stolen session alone cannot turn MFA on and lock the owner out) and `mfa/enable` confirms a first code and returns 10 recovery codes (80 bits each, shown once).
  - At rest the secret is sealed with AES-256-GCM (`kid:nonce|ciphertext`, bound to the user and field) and recovery codes are SHA-256 hashes. Secrets sealed with an old `kid` are sealed again with the active one on use.
  - With MFA on, login answers `{mfaRequired, mfaToken}` (5 minutes, 5 codes, kept in Redis by hash) and `mfa/verify` opens the session. Each TOTP step works once (compare-and-set on `mfa.last_step`), each recovery code once (`$pull`), and of two concurrent verifications only one opens a session.
  - Wrong codes and wrong step-up passwords count toward the lockout of the address, and the password alone no longer clears it, so knowing the password does not buy unlimited guesses at the code.
  - `mfa/disable` takes the password and a code. Turning MFA on or off emails a notice. These routes answer a wrong password or code with `403`, never `401`.
- **Email** goes through any SMTP relay, asynchronously. A bounded queue retries transient failures and drains on shutdown. Templates are translated, and they never include user-chosen text.
- **Audit:** `register`, `email_verified`, `login_success` (with how the last factor was checked), `login_failure`, `logout`, `refresh_reuse_detected`, `password_reset_requested`, `password_reset`, `reauth_failure`, `mfa_setup`, `mfa_enabled`, `mfa_disabled`, `mfa_failure`, `mfa_recovery_code_used`, `password_changed`, `session_revoked`, `sessions_revoked` and `account_deleted` are stored in `audit_logs` with user id, IP and user agent. They never store the address, password, token or code.

## Profile and account

Every `/api/v1/me` route acts on the caller of the access token; none takes a user id, so one user cannot reach another's account.

- **Profile:** `GET /me` returns the name, address, verification and MFA status (with the recovery codes left) and the preferences, never a hash or secret. `PATCH /me` changes only the display name; any other field is rejected.
- **Preferences:** `GET`/`PATCH /me/preferences` with `locale` (`pt-BR`, `es-ES`, `en-US`), `theme` (`system`, `light`, `dark`) and `currency` (`BRL`, `USD`, `EUR`). Fields are optional, and one unsupported value rejects the whole change.
- **Language of the answers:** before sign-in it comes from `Accept-Language`. On authenticated routes the `UserLocale` middleware switches to the stored locale, read from Redis (`monthra:{env}:user:locale:{user_id}`, 1 hour) and filled from MongoDB on a miss. Readers fill with `SET NX` and a preferences change writes with `SET`, so a stale read never replaces a newer value. If both stores fail the request keeps `Accept-Language`: the language guards nothing. Account emails always use the stored locale.
- **Password change:** `POST /me/password` asks for the current password (wrong ones count toward the lockout and answer `403`), applies the policy and HIBP, signs out every other session, voids pending reset links and emails a notice. The caller's session stays.
- **Sessions:** `GET /me/sessions` lists up to 100 active sessions (IP, user agent, `current`), the most recently used first. `DELETE /me/sessions/{id}` signs one out; another user's session answers `404` like an unknown one, and repeating the request is safe. `DELETE /me/sessions` signs out all but the current one. Revoking many sessions denies them in Redis before revoking them in MongoDB, so a Redis failure leaves nothing half done and a retry works.
- **Account deletion (LGPD):** `DELETE /me` takes the password and, with MFA on, a code. It denies every session, runs the registered `UserDataEraser`s in order (feature data first, then the auth stores and the locale cache) and deletes the account last. Each step is idempotent and there is no transaction, so an interrupted deletion leaves an account its owner can sign in to and delete again. The owner gets a confirmation email and the address can sign up again. The audit log is kept until `AUDIT_LOG_TTL`: access records must be retained (Marco Civil art. 15, LGPD art. 16), and they hold no address.

## Browser security

- **CORS:** only `CORS_ALLOWED_ORIGINS` get CORS headers (with credentials). A preflight from any other origin gets 403.
- **CSRF** (signed double submit): browsers call `GET /api/v1/auth/csrf` and send the token in `X-CSRF-Token` on every `POST`/`PUT`/`PATCH`/`DELETE`. A request counts as a browser request when it has an `Origin` header or carries auth cookies. Foreign origins are rejected, and so are Bearer tokens sent together with auth cookies. Native clients (Bearer only, no cookies) are exempt.
- **Same site:** the auth cookies are `SameSite=Strict`, so the web app and the API must share a site (for example `app.example.com` and `api.example.com`).
- **Rate limits:** GCRA budgets in Redis, shared by every instance. `/api/v1` has a per-IP baseline (IPv6 grouped by /64). Responses carry `RateLimit-Limit`, `RateLimit-Remaining` and `RateLimit-Reset`, and a spent budget answers 429 with `Retry-After`. The keys are `rate:monthra:{env}:rl:{policy}:{subject}`. The baseline and the per-user budget let requests through when Redis is down; the budgets of the credential endpoints fail closed.
- **Client IP:** taken from `X-Forwarded-For` only when the TCP peer is in `HTTP_TRUSTED_PROXIES`. The header is read right to left, skipping the trusted proxies.

## Observability

- Logs: one JSON line per event on stdout, with `request_id`, `trace_id`/`span_id` and, on authenticated requests, `user_id`. Sensitive keys (password, token, secret, cookie, authorization, cpf...) are redacted; access logs leave out query strings, IPs and user agents.
- Metrics: `monthra_http_requests_total`, `monthra_http_request_duration_seconds` (by chi route pattern; unmatched paths share one label), `monthra_http_requests_in_flight`, `monthra_build_info`, `monthra_ratelimit_rejected_total{policy}`, `monthra_ratelimit_errors_total{policy}`, `monthra_csrf_rejected_total{reason}`, `monthra_auth_logins_total{result}`, `monthra_auth_refresh_reuse_total`, `monthra_auth_password_resets_total`, `monthra_auth_mfa_total{event}`, `monthra_email_sent_total{template,result}`, `monthra_email_dropped_total{template}`, plus Go and process metrics.
- Traces: HTTP server spans named after the route, MongoDB command spans (operation and collection only, never query values) and Redis spans.

## Layout

```
cmd/api/              entrypoint (and the `healthcheck` subcommand for the image)
internal/app/         wiring, servers and graceful shutdown
internal/config/      environment configuration
internal/http/        router, servers, handler/, middleware/, dto/, problem/, cookie/
internal/domain/      entities and validation rules (no I/O)
internal/usecase/     use cases on ports (auth/, profile/)
internal/auth/        Argon2id, HIBP, EdDSA JWT, opaque tokens, TOTP, recovery codes, lockout policy
internal/crypto/aead/ AES-256-GCM sealing with a key ring (data at rest)
internal/email/       SMTP sender, async dispatcher, localized templates
internal/captcha/     Cloudflare Turnstile
internal/audit/       audit log recorder
internal/csrf/        signed CSRF tokens
internal/ratelimit/   Redis GCRA rate limiter
internal/i18n/        locale matching and translated messages
internal/observability/  logger, metrics, tracing
internal/repository/mongo/  MongoDB client, command tracing and repositories
internal/cache/       Redis client, key naming (monthra:{env}:...), denylist, lockout, MFA challenges and the locale cache
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
