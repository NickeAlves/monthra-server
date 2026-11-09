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

To get a user with data to look at, `make seed` (only with `APP_ENV=local`): it replaces `demo@monthra.local` (verified, pt-BR) with five accounts (checking, savings, a credit card, USD and cash), five recurrences started six months ago (salary, rent, a streaming on the card, a monthly saving, the card payment) and six months of everyday spending, a currency exchange, freelance income and a 10x purchase, then four budgets for the current month sized from that spending (groceries with room left, dining at the warning, transport past its limit, travel in USD). Everything goes through the use cases, so their rules apply. Running it again deletes the demo user (the same erasers as `DELETE /me`) and creates it anew; the amounts come from a fixed random seed. The password is `SEED_PASSWORD` from `.env`, or a random one, printed at the end.

### Make targets

| Target | What it does |
|---|---|
| `make up` / `make down` | Start / stop the local environment |
| `make secrets` | Print random values for the secret variables of `.env` |
| `make logs` / `make ps` | Follow logs / service status |
| `make run` | Run the API on the host |
| `make seed` | Replace the local demo user (`demo@monthra.local`) and its fake data |
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

## Accounts

The money accounts of the caller: `/api/v1/accounts` and `/api/v1/accounts/{id}`.

- **Money:** every amount is an integer in minor units (cents) in MongoDB and in the API, never a float or a decimal string (`150000` is R$ 1,500.00). Amounts are bounded by `domain.MaxAmount` (10^13), so totals stay exact in JavaScript and Dart on the web, which hold integers exactly only up to 2^53.
- **Currency per account:** `BRL`, `USD` or `EUR`, chosen at creation and never changed, like the type (`checking`, `savings`, `credit_card`, `cash`, `brokerage`). Totals are grouped by currency; nothing is converted.
- **Credit cards** carry their closing day, due day (1 to 31; a shorter month uses its last day) and an optional limit; the other types refuse those settings.
- **Balances:** every account answers `balance`, its initial balance plus the transactions up to `?asOf=YYYY-MM-DD` (the client's local today; today in UTC when absent), and `projectedBalance`, plus every transaction, future ones included. One aggregation per read sums them by account; a cache comes with the dashboard.
- **Archive or delete:** `PATCH {"archived": true}` hides an account from the default list (`?includeArchived=true` shows it) and keeps its history; an archived account takes no new transactions. `DELETE` removes an account for good only while it has no transactions (as their account or as the destination of a transfer); otherwise it answers `409 account_in_use`.
- **Limits:** at most 100 accounts per user, archived ones included (`409 limit_reached`); the list comes back whole, without pagination.
- **Ownership:** every query filters by the record id and the caller's `user_id`, which only ever comes from the access token. Another user's account answers `404` like an unknown one, and the bodies reject unknown fields, so no client can choose an owner or set a `$` operator.
- `DELETE /me` erases the accounts before the auth data.

## Categories

The expense and income categories of the caller: `/api/v1/categories` (`?kind=expense|income`) and `/api/v1/categories/{id}`.

- **Fields:** a name (1 to 50 characters, may repeat), a `kind` (`expense` or `income`, fixed at creation), a Lucide icon name in kebab-case (`shopping-cart`; clients show a generic icon for a name they do not know) and a `#RRGGBB` color, answered in upper case.
- **Default set:** every user starts with 22 categories (16 expense, 6 income; see `internal/domain/category_defaults.go`), created on their first `GET` or `POST /categories`. A default carries a stable `defaultKey` (`groceries`, `salary`, ...) and is named in the caller's language until renamed. After a rename, the name stays the same in every language and the key stays too.
- **Seeding without duplicates:** the defaults are inserted unordered, and the partial unique index `categories_user_default_key_unique` on `{user_id, default_key}` drops the ones already there. Then `users.categories_seeded_at` is set. A request that fails halfway is completed by the next one, concurrent first reads all get the whole set, and once the user is marked, a deleted default never comes back.
- **Limits:** at most 200 categories per user, the defaults included (`409 limit_reached`); the list comes back whole, oldest first, so the defaults keep their curated order.
- **Ownership:** same as accounts (`{_id, user_id}` filter, `404` for another user's category, strict bodies). `kind` and `defaultKey` are not patchable.
- **Deleting a category in use** answers `409 category_in_use`. `DELETE /categories/{id}?reassignTo={other}` moves its transactions to another of the caller's categories of the same kind, deletes it, then moves again any transaction created in between.
- `DELETE /me` erases the categories before the auth data.

## Transactions

The expenses, income and transfers of the caller: `/api/v1/transactions` and `/api/v1/transactions/{id}`.

- **Kinds:** `expense` and `income` need a category of the same kind; a `transfer` moves money between two of the caller's accounts and has none. `kind` never changes.
- **Amounts:** always positive, in minor units of the account's currency, which the transaction copies (`currency`), since an account never changes currency. A transfer also stores what arrived (`toAmount`, in `toCurrency`): equal to `amount` between accounts of one currency, required between two. Nothing is converted.
- **Dates:** a civil day (`"2026-11-30"`, 1900 to 2100) stored as midnight UTC, so a transaction never changes month with the user's time zone. This departs from "every date is an instant in UTC" on purpose; instants (`createdAt`, ...) stay UTC times.
- **Description and tags:** an optional description (up to 200 characters) and up to 10 tags (lower case, no repeats). Text search (`?q=`) matches a folded copy of the description (lower case, accents removed), so `cafe` finds `Café`; the folded copy never leaves the store, and the text is quoted, never read as a pattern.
- **Listing:** newest day first, ties broken by the newest id, 50 per page (at most 100), with an opaque `nextCursor` (base64url of the last day and id) to pass back as `?cursor=`. Filters: `from`/`to` (days, both included), `accountId` (also the transfers the account received), `categoryId`, `kind`, `tag` and `invoice` (with `accountId`, one card invoice). Every transactions index ends with `{date: -1, _id: -1}`, the listing order, and the cursor bounds the date, so a page reads only its own entries; indexes replaced this way are dropped at boot (`migrations.obsolete`). A filter on either leg of a transaction (its account or a transfer's destination) is an `$or` at the root of the query with `user_id` in each branch, so each branch reads its own index; a profiler test checks how many keys these queries read.
- **References:** the accounts and the category are looked up among the caller's records only, so another user's id is `invalid` on the field, like one that does not exist; an archived account is `archived`. A change moves only the references it names, so a transaction of an account archived since can still be corrected.
- **Without multi-document transactions** (MongoDB runs standalone), two races are accepted, both one user racing themselves: a transaction created while its account is deleted, and one created while its category is deleted without `reassignTo`.
- **Metric:** `monthra_transactions_created_total{kind}`.
- `DELETE /me` erases the transactions first, before the accounts and the categories they name.

## Credit card invoices

`GET /api/v1/accounts/{id}/invoices?from=YYYY-MM&to=YYYY-MM&asOf=YYYY-MM-DD`: one invoice per month (at most 24; without a range, the invoice open on `asOf`). An account that is not a credit card answers `404`.

- **Cycle:** an invoice is named after the month it falls due in, on the card's `dueDay`; it closes on `closingDay` of that month when `dueDay` comes later, of the month before otherwise, and a day past the end of a shorter month is its last day. A purchase belongs to the first invoice that closes after its day, so the closing day goes to the next invoice.
- **Stored with the transaction:** a leg on a card keeps its invoice (`invoiceMonth`; `toInvoiceMonth` for a transfer to a card, which is a payment), picked when the transaction is written, so changing the card's days later moves nothing. An expense or a transfer out of the card is a charge, an income on it a credit. A payment pays the last invoice closed on its day unless it names another. The owner may name any invoice within 12 months of the default (a purchase posted late, a payment made early); a change of day or account picks it again unless the change names one.
- **Carried over:** what an invoice leaves unpaid becomes the next one's `previousBalance`, starting from the card's opening debt (a negative `initialBalance`), with no interest and no minimum payment. So the last invoice's `remaining` matches the card's debt.
- **Status on `asOf`:** `upcoming`, `open`, `closed`, `paid`, `overdue`, `carried_over`. Closing and due days come from the card's current settings.
- **Reads:** one aggregation of the card's legs up to the last month asked, served by two partial indexes on `{user_id, account_id | to_account_id, invoice_month | to_invoice_month, date, _id}`; not cached yet (the dashboard brings the cache).
- Card transactions written before invoices existed have none; nothing was deployed, so local data can simply be recreated.

## Installments

`POST /api/v1/transactions` with `"installments": 2..48` splits an expense (on any account) into monthly installments, `amount` being the total. Each installment is a transaction with `installment: {groupId, number, count}`; the answer is the first one, and `GET /api/v1/transactions?installmentGroupId=` lists the purchase.

- **Split:** each installment takes `amount / count`, the first one the remainder too. Installment *i* falls on the day of the purchase *i*-1 months later (January 31, February 28, March 31); on a credit card it belongs to the first one's invoice plus *i*-1, one installment per invoice. So future invoices show as `upcoming` with their installments, and `balance` counts each installment on its day (`projectedBalance` counts them all).
- **Scope:** `PATCH` and `DELETE /api/v1/transactions/{id}?scope=this|future|all` (`this` by default) reach the installment, it and the ones numbered after it, or the whole purchase; on a transaction outside a purchase, that one alone.
- **Scoped edits:** the other installments take the changes made to the one addressed: `amount` (each installment's), category, description, tags and account take its new value; a new day moves each by the same months, on the new day; a new day, account or invoice gives installment *j* the addressed one's invoice plus *j*-*k*. A field sent unchanged changes nothing, so clients may send their whole form. A purchase stays on one account: `accountId` needs `scope=all` (`422 scope: invalid` otherwise).
- **Kept as created:** numbers never change (deleting some leaves "3/12"), and so does the count: to change it, delete the purchase (`scope=all`) and create it again.
- **No multi-document transactions:** a failed insert removes the installments already stored (once, best effort); a scoped edit writes the other installments in one unordered bulk write and the addressed one last, so until it is stored, sending the same edit again finds the same changes and completes an interrupted one. One partial index `{user_id, installment.group_id, date, _id}` serves the listing and the group reads, which touch at most 48 entries.

## Recurrences

`/api/v1/recurrences` holds the transactions that repeat (rent, subscriptions, a salary, a weekly saving): an expense, income or transfer with the fields of a transaction (validated the same way on its start), and a simplified RRULE. Up to 200 per user, listed whole.

- **Rule:** `frequency` (`daily`, `weekly`, `monthly`, `yearly`) every `interval` (1-99) from `startDate`, counted from the start (a monthly rule from January 31 falls on February 28, then March 31; a yearly one from February 29 on February 28 of common years). It ends on `endDate`, after `count` occurrences (1-1000, stored as that `endDate`), or never.
- **Occurrences are materialized lazily:** every read of transactions, accounts (balances), invoices and recurrences first creates the occurrences due up to `?asOf` (the client's local today, clamped to a day around today in UTC; today in UTC when absent). Each is a transaction with `recurrence: {id, occurrenceDate}`. A start in the past creates the history at once, at most 500 occurrences per read (the next reads create the rest). A read never fails because of it: a failure is logged and the read goes on.
- **Idempotent without transactions:** a pass stores the occurrences first (unordered, the unique index `{recurrence_id, occurrence_date}` skipping the ones already there) and then advances the recurrence's `next_date` with a compare-and-set, so an interrupted pass or two readers at once create each occurrence once. Ended recurrences have no `next_date`; the partial index `{user_id, next_date}` holds the active ones, so the read before each listing touches only what is due.
- **Next ones:** `nextDate` and `lastDate` on each recurrence; `GET /api/v1/recurrences/upcoming?to=` projects the occurrences not created yet (30 days by default, up to 366, at most 1000), with the card invoices, stored nowhere.
- **Editing the rule:** `PATCH /api/v1/recurrences/{id}` changes the next occurrences only, never the transactions already created. A new `startDate` must come after `lastDate`, so a series never covers its past again. `DELETE` stops the recurrence and keeps its transactions.
- **Editing occurrences:** `PATCH /api/v1/transactions/{id}?scope=future|all` on an occurrence gives the later (or all) occurrences and the recurrence the changes made to the one addressed (amounts, category, description, tags, accounts); moving to another account is allowed and each occurrence picks its invoice again by its day. Day and invoice change with `scope=this` only. `DELETE ?scope=future` ends the rule before the occurrence and deletes the later ones (cut on or before `startDate`, the recurrence goes, unless earlier occurrences remain: then it stays ended, `endDate` before `startDate`); `?scope=all` deletes the recurrence and all its occurrences. The writes go others, then the recurrence, then the addressed one, so a resend completes an interrupted edit.
- **Guards:** an account an active recurrence names cannot be archived, and one any recurrence names cannot be deleted (`409 used_by_recurrence`); a category one names needs `reassignTo` to be deleted, which moves the recurrences too. `DELETE /me` erases the recurrences before the transactions.

## Dashboard

`GET /api/v1/dashboard/summary?month=YYYY-MM&asOf=YYYY-MM-DD` sums one month (the month of the reader's today by default) by currency, with no conversion between them. It creates the occurrences of the recurrences due up to the reader's today first (`asOf`, held within a day of today in UTC).

- **Flows of the month:** `income`, `expense` and `net` add up the whole month, days still to come included (a card installment later in the month counts, on its date). Transfers (a card payment, a saving, a currency exchange) are neither. `scheduled` is what the recurrences are still to create in the month (`domain.Recurrence.Between`). `expensesByCategory` and `incomeByCategory` hold `{categoryId, amount, count}`, the largest first.
- **Balances:** `asOf` in the answer is the day they count up to: the month's last day, or the reader's today when that comes first. `balance` is the net worth that day (a card's debt counts against it, archived accounts count) and `accounts` holds each account's balance. As on `/accounts?asOf`, an initial balance has no day, so it counts in every month.
- **History:** 12 points up to `month`, `{month, income, expense, netWorth}`; `netWorth` is null for the months after `asOf`.
- **Ids only:** accounts and categories come as ids, joined with `/accounts` and `/categories` by the app. So the cache does not depend on the language, and renaming invalidates nothing.
- **Two aggregations:** the flows of the 12 months through `{user_id, date}` (`Transactions.Flows`), and what every transaction up to the cut moved on each account by month (`Transactions.LegSums`, sharing the signed legs of the balances).
- **Cache-aside (§5.4):** a summary lives in Redis for 5 minutes at `monthra:{env}:summary:{user_id}:{version}:v1:{YYYY-MM}:{cut}`. `monthra:{env}:summary:{user_id}:ver` holds a random version (24 h), which every write to the user's data replaces (`dashboard.Invalidator`). The accounts, categories (a deletion that moves transactions), transactions and recurrences use cases call it after every write attempt, through their `AfterWrite` hook, and `Materialize` calls it when it creates occurrences, so one write drops every month at once. A lost version only makes the next reads miss. The cache fails open: a Redis error is logged and counted, and the summary is computed. Only months within 12 of the reader's are cached. `DELETE /me` drops the version, and what was cached under it expires within 5 minutes.

## Budgets

`/api/v1/budgets` holds the most the caller means to spend on one expense category in one month (§1 item 6), in one currency: only the expenses in that currency count, with no conversion. One per category and month (`409 budget_exists`); the category, month and currency never change, `limit` and `warnAt` (the warning share, 1 to 100 percent, 80 by default) do.

- **Usage:** `spent` adds up the category's expenses over the whole month, the days still to come included (an installment), as the dashboard counts them (`Transactions.Flows` of the month). `scheduled` is what the recurrences are still to create in it; it never counts toward `status`: `ok`, `warning` (from `warnAt` percent of `limit`, the limit itself included) or `exceeded` (past `limit`). `remaining` is `limit - spent`. `GET /budgets?month=&asOf=` returns the month whole, with `totals` by currency. Every read creates the occurrences due first.
- **Monthly carry-over:** the budgets of a month are copied to the next one when it starts, lazily (on the first read or write of budgets, or alert check, of the month), each month from the one before. `users.budgets_rolled_through` marks the last month done (moved with `$max`); the unique index `{user_id, month, category_id}` skips the categories a month already has, so a pass that fails halfway is completed by the next one. A change or a deletion in a month therefore applies from it on, and the months gone by keep theirs. After a gap longer than 12 months, only the last 12 are filled. Budgets can be created for the reader's month or one gone by; a later month answers a projection of the reader's month (`projected: true`, no ids) until it starts.
- **Alerts:** after every write to the caller's finance data (the `AfterWrite` hook of the accounts, categories, transactions and recurrences use cases, beside the dashboard invalidation), `budgets.Alerts` checks the budgets of the current month (UTC today ±1 day) not exceeded yet; with none, that is one query. Each alert (warning, exceeded) is claimed once per budget by a compare-and-set on its mark (`warned_at`, `exceeded_at`), and the owner gets one email about the worst (`budget_warning`, `budget_exceeded`), naming no category (no user-chosen text in emails), with a link to `{APP_PUBLIC_URL}/budgets?month=YYYY-MM`. `preferences.budgetAlerts` (`PATCH /me/preferences`, on by default) mutes the emails; the marks are still set, so unmuting sends no old alert. Creating a budget or changing its limit is silent (the marks follow the state then), and a limit raised rearms the alerts; spending less afterwards does not. A month carried over starts with no marks, so one that starts already past a share (installments) alerts on the next write. The check never creates occurrences, runs even when the request is gone, and fails open (warn log, `monthra_budget_alert_errors_total`).
- **Categories:** deleting a category deletes its budgets in every month, before the category itself. `DELETE /me` erases the budgets (after the transactions, before the categories).

## Browser security

- **CORS:** only `CORS_ALLOWED_ORIGINS` get CORS headers (with credentials). A preflight from any other origin gets 403.
- **CSRF** (signed double submit): browsers call `GET /api/v1/auth/csrf` and send the token in `X-CSRF-Token` on every `POST`/`PUT`/`PATCH`/`DELETE`. A request counts as a browser request when it has an `Origin` header or carries auth cookies. Foreign origins are rejected, and so are Bearer tokens sent together with auth cookies. Native clients (Bearer only, no cookies) are exempt.
- **Same site:** the auth cookies are `SameSite=Strict`, so the web app and the API must share a site (for example `app.example.com` and `api.example.com`).
- **Rate limits:** GCRA budgets in Redis, shared by every instance. `/api/v1` has a per-IP baseline (IPv6 grouped by /64). Responses carry `RateLimit-Limit`, `RateLimit-Remaining` and `RateLimit-Reset`, and a spent budget answers 429 with `Retry-After`. The keys are `rate:monthra:{env}:rl:{policy}:{subject}`. The baseline and the per-user budget let requests through when Redis is down; the budgets of the credential endpoints fail closed.
- **Client IP:** taken from `X-Forwarded-For` only when the TCP peer is in `HTTP_TRUSTED_PROXIES`. The header is read right to left, skipping the trusted proxies.

## Observability

- Logs: one JSON line per event on stdout, with `request_id`, `trace_id`/`span_id` and, on authenticated requests, `user_id`. Sensitive keys (password, token, secret, cookie, authorization, cpf...) are redacted; access logs leave out query strings, IPs and user agents.
- Metrics: `monthra_http_requests_total`, `monthra_http_request_duration_seconds` (by chi route pattern; unmatched paths share one label), `monthra_http_requests_in_flight`, `monthra_build_info`, `monthra_ratelimit_rejected_total{policy}`, `monthra_ratelimit_errors_total{policy}`, `monthra_csrf_rejected_total{reason}`, `monthra_auth_logins_total{result}`, `monthra_auth_refresh_reuse_total`, `monthra_auth_password_resets_total`, `monthra_auth_mfa_total{event}`, `monthra_email_sent_total{template,result}`, `monthra_email_dropped_total{template}`, `monthra_transactions_created_total{kind}`, `monthra_cache_requests_total{cache,result}` (hit, miss), `monthra_cache_errors_total{cache,op}`, `monthra_budget_alerts_total{level,result}` (sent, muted), `monthra_budget_alert_errors_total`, plus Go and process metrics.
- Traces: HTTP server spans named after the route, MongoDB command spans (operation and collection only, never query values) and Redis spans.

## Layout

```
cmd/api/              entrypoint (and the `healthcheck` and `seed` subcommands)
internal/app/         wiring, servers, graceful shutdown and the local seed
internal/config/      environment configuration
internal/http/        router, servers, handler/, middleware/, dto/, problem/, cookie/
internal/domain/      entities and validation rules (no I/O)
internal/usecase/     use cases on ports (auth/, profile/, accounts/, categories/, transactions/, recurrences/, dashboard/)
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
internal/cache/       Redis client, key naming (monthra:{env}:...), denylist, lockout, MFA challenges, the locale cache and the dashboard summaries
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
