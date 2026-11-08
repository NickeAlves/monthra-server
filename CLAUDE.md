# monthra-server - instructions for Claude Code

> Built from the Monthra master guide (`MONTHRA_CLAUDE.md`) following its §10.6: sections 1-3, this repo's section, all of §6 and §9-10. When the guide changes, update this file too.

## Project decisions

- **Branches:** `main` (production), `staging` (homologation), `develop` (development), all protected (PR only). Work happens on `feature/<name>` or `fix/<name>` branched from `develop`, with a PR back to `develop`. Promotion: PR `develop` -> `staging` -> `main`.
- **Language:** code, comments, README and commits in English (Conventional Commits). UI text only through i18n (pt-BR default, es-ES, en-US). The guide sections below stay in Portuguese, as in the master guide.
- **Shared Docker network:** `monthra-net` (external), created once with `docker network create monthra-net`.
- **Pending decisions (ask before implementing):** cloud provider (Terraform), Open Finance aggregator, transactional email provider for staging/production (picked together with the cloud), alert channel, Flutter bundle ID.
- **Email:** a `Sender` port with a generic SMTP adapter (`wneessen/go-mail`). Mailpit locally and in the local homologation stack (`SMTP_ALLOW_INSECURE=true`, never on a real deployment); the real provider plugs in through the `SMTP_*` variables. Delivery is asynchronous (bounded queue, retries, drained on shutdown). Templates never include user-chosen text.
- **Captcha:** Cloudflare Turnstile, verified on the server, on only when `TURNSTILE_SECRET` is set. It is asked after 3 failed logins (by address) or 10 (by IP), and after 3 sign-ups from one IP within an hour.
- **Sessions in MongoDB, not Redis:** `sessions` and `refresh_tokens` (rotation chain, SHA-256 hashes, TTL on `expires_at`), so a Redis eviction never signs everyone out and the session list can be queried. Redis holds the denylist (`jwt:deny:{jti}`, `session:revoked:{sid}`), checked on every authenticated request, failing closed.
- **Refresh reuse grace:** a rotated refresh token presented again within `REFRESH_REUSE_GRACE` (10 s) answers `409 refresh_conflict` instead of revoking the session, because two browser tabs share the cookie. Later reuse revokes the session.
- **Cookie prefixes (deviation from §6.2):** the access cookie is `__Host-monthra_at` (`Path=/`). The refresh cookie is `__Secure-monthra_rt` on `Path=/api/v1/auth/refresh`, because `__Host-` requires `Path=/`. Both are host-only, `HttpOnly`, `Secure`, `SameSite=Strict`; locally (no TLS) they have no prefix and no `Secure`.
- **Token delivery:** requests with an `Origin` header (browsers) get cookies and never tokens in the body; native clients get JSON and send `Authorization: Bearer`.
- **Email links:** tokens travel in the URL fragment (`{APP_PUBLIC_URL}/verify-email#token=…`, `/reset-password#token=…`), so they never reach logs or `Referer`. The Flutter web app must use the path URL strategy.
- **JWT:** EdDSA (Ed25519) with a `kid` key ring (`JWT_SIGNING_KEYS`, `JWT_ACTIVE_KID`); `iss` is `monthra-server:{env}` and `aud` is `monthra-api`. Keys come from `make secrets`, so there is no `keygen` subcommand.
- **Encryption at rest:** AES-256-GCM through `internal/crypto/aead` with a `kid` key ring (`ENCRYPTION_KEYS`, `ENCRYPTION_ACTIVE_KID`), stored as `kid:base64url(nonce|ciphertext|tag)` with additional data `"{collection}.{field}|{user_id}"`. Rotation: add a key and make it active, keep the old one; values are sealed again when used. The keys never reuse `CSRF_SECRET` or a JWT seed (checked at boot). Open Finance reuses it for `provider_connection_id`.
- **Password reset:** single-use link (`/reset-password#token=…`, `RESET_TOKEN_TTL`), checked against the policy and HIBP before the token is spent. It marks the address verified (the link proves control of the inbox, which also ends a pre-registered hijack), revokes every session and leaves MFA on. The Flutter app needs the `/reset-password` route.
- **Account email quota:** `RATE_LIMIT_AUTH_MAIL` (5/h) per address on register, resend and forgot-password, checked before the lookup (no enumeration), against inbox flooding.
- **MFA (TOTP):** SHA-1, 6 digits, 30 s, skew 1 (authenticator-app compatible); 10 recovery codes of 80 bits stored as SHA-256. Setup asks for the password again (step-up); disable asks for the password and a code. Anti-replay is a compare-and-set on `users.mfa.last_step` in MongoDB (durable, atomic), not a Redis key. The login challenge (`mfaToken`, 5 min, 5 attempts) lives in Redis by hash and fails closed. Wrong codes and step-up passwords feed the login lockout of the address, which only a full login clears. Step-up and code failures answer 403 so clients keep the session.
- **Locale of the answers:** `Accept-Language` before sign-in; on authenticated routes the stored preference (`UserLocale` middleware after `Authenticate`), cached in Redis at `monthra:{env}:user:locale:{user_id}` (1 h; readers `SET NX`, writers `SET`) and filled from MongoDB. It fails open to `Accept-Language`. Emails always use the stored locale.
- **Sessions endpoints:** `GET /me/sessions` returns up to 100 active sessions (IP and user agent included) instead of cursor pagination. Revoking one checks `user_id` in the query (another user's session is a `404`) and is idempotent. Revoking many denies the sessions in Redis **before** revoking them in MongoDB, so a Redis failure leaves nothing half done; password change also revokes the other sessions before storing the new hash.
- **Account deletion (`DELETE /me`, LGPD):** password step-up, plus a code when MFA is on. Order: deny every session in Redis and MongoDB, run `authuc.Deps.Erasers` in order, delete the user document last (idempotent, no transaction; an interrupted deletion is retried by the owner). `audit_logs` are kept until `AUDIT_LOG_TTL` (Marco Civil art. 15, LGPD art. 16) and never hold the address. The owner gets an `account_deleted` email.
- **`UserDataEraser` contract (step 3 and later):** every feature that stores data by `user_id` (accounts, categories, transactions, budgets, investments, Open Finance connections) must register an idempotent `EraseUserData(ctx, userID)` in `internal/app/features.go`, which runs them **ahead** of the auth erasers. Open Finance revokes the connections at the provider before deleting them. `repomongo.NewUserEraser(db, coll)` covers a plain `DeleteMany({user_id})`.
- **Step 3 roadmap (one PR per feature, each with its own plan):** accounts -> categories -> transactions -> credit card invoices -> installments (expenses on any account, 2 to 48, edit/delete scope `this`/`future`/`all`) -> recurrences -> dashboard -> budgets -> investments (ask about a quotes API first) -> Open Finance (ask which aggregator first) -> reports.
- **Money:** amounts are `int64` minor units (cents) in MongoDB **and in the API**, never floats or decimal strings, bounded by `domain.MaxAmount` (10^13) so totals stay exact in clients holding numbers as doubles (JS, Dart web, exact up to 2^53).
- **Currency per account:** BRL, USD or EUR, fixed at creation (like the account type); balances, dashboard and budgets sum by currency, with no conversion.
- **Feature pattern:** a use case package per feature (`internal/usecase/<feature>`) on a store port; the handler implements `handler.Routes` and is wired in `internal/app/features.go` (`newFeatures`), which also registers the feature's `UserDataEraser`. Repositories filter every query by `{_id, user_id}` (`ownedBy`), with the user id from the token only; another user's record is `domain.ErrNotFound`, answered `404 not_found`. Use case writes use millisecond time (MongoDB precision).
- **Accounts:** `checking`, `savings` (added to §5.3), `credit_card` (closing day, due day 1-31 clamped to short months, optional limit), `cash`, `brokerage`. `type` and `currency` are not patchable. Archive (`archived_at`) keeps the history and takes no new transactions; deleting an account with transactions (as their account or a transfer's destination) answers `409 account_in_use`. `balance` (initial balance plus the transactions up to `?asOf`, the client's local today, UTC today by default) and `projectedBalance` (all of them) come from one aggregation per read, uncached until the dashboard. Caps: 100 accounts (`409 limit_reached`), lists returned whole. Cursor pagination only where lists grow without bound (transactions, investment movements).
- **Categories:** `expense` or `income` (`kind` is not patchable), a Lucide icon name in kebab-case (format only, no closed list) and a `#RRGGBB` color stored in upper case. 22 default categories per user (`internal/domain/category_defaults.go`) are seeded on the first `GET`/`POST /categories`: an unordered insert deduplicated by the partial unique index `{user_id, default_key}`, then `users.categories_seeded_at` is set. So a failed seed is completed by the next read, and a deleted default never returns. A default's name follows the request locale until renamed; `default_key` stays after a rename (for Open Finance category mapping later). Cap 200, the defaults included; the list comes back whole. Deleting a category in use answers `409 category_in_use`; `?reassignTo=` (another category of the same kind) moves its transactions, deletes it and moves again what arrived in between.
- **Transactions:** `expense` | `income` (a category of the same kind is required) | `transfer` (one document: `account_id` to `to_account_id`, no category); `kind` is not patchable. Amounts are positive; the account's currency is copied on the transaction (`currency`, `to_currency`), and a transfer between two currencies stores what arrived (`to_amount`, required then; equal to `amount` otherwise). `date` is a civil day (`domain.Date`, "YYYY-MM-DD", 1900-2100) stored as midnight UTC: a documented departure from §5.3, so a month never shifts with the time zone. Up to 10 lower-case tags; a description of up to 200 characters, plain text (not encrypted, user decision of 2026-11-08) with a folded copy (`description_search`, lower case without accents) for `?q=`, quoted with `regexp.QuoteMeta`. References are looked up among the caller's records (`invalid` otherwise, `archived` for an archived account, `kind_mismatch` for a category of the other kind); a patch checks only the references it moves to. Listing: `{date: -1, _id: -1}` with an opaque base64url cursor, 50 per page (max 100); each transactions index ends with `date: -1, _id: -1`, and replaced indexes are dropped at boot (`migrations.obsolete`). No multi-document transactions (standalone MongoDB): a transaction created while its account or category is deleted is an accepted race of one user with themselves. Metric `monthra_transactions_created_total{kind}`. The transactions eraser runs first. Filters on either leg (account statements, invoices, balances, the in-use check) put the `$or` at the root with `user_id` in each branch (`eitherLeg`): beside a top-level `user_id`, MongoDB reads every transaction of the user. The cursor also bounds `date`. `TestTransactionsQueriesReadOnlyTheirEntries` checks the keys read with the profiler.
- **Credit card invoices (user decisions of 2026-11-08):** an invoice is named after its due month (`DueDate(M) = M.Day(dueDay)`; it closes on `closingDay` of M when `dueDay > closingDay`, of M-1 otherwise; days clamped to short months). A leg on a card stores its invoice (`invoice_month` on the account leg: expenses and transfers out are charges, income a credit; `to_invoice_month` on a transfer to a card: a payment), never null (`$unset`), picked when written: a purchase goes to the first invoice closing after its day (the closing day goes to the next), a payment to the last one closed on its day. Changing the card's days moves nothing already stored. Clients may name another invoice within 12 months (`MaxInvoiceShift`); a patch picks it again when the leg's day or account moves, unless it names one ("" picks the default again). What an invoice leaves unpaid rolls into the next as `previousBalance`, starting from the opening debt (`-initialBalance`), with no interest or minimum payment. Statuses on `asOf`: `upcoming`, `open`, `closed`, `paid`, `overdue`, `carried_over`; closing and due days shown come from the current settings. `GET /accounts/{id}/invoices?from&to&asOf` (at most 24 months, default the open invoice; not a card is `404`) and `GET /transactions?accountId&invoice=`. No backfill of card transactions written before this (nothing deployed).
- **Installments (user decisions of 2026-11-08):** `installments` (2 to 48) on `POST /transactions`, expenses on any account only; `amount` is the total, each installment `amount / n` with the remainder on the first, installment *i* on the purchase day + (*i*-1) months clamped from the original day (`Date.AddMonths`), on a card in the first invoice + (*i*-1). Each is a transaction with `installment {group_id, number, count}` (group id assigned by the store, never patched, never renumbered); `POST` answers the first, `?installmentGroupId=` lists them. `PATCH`/`DELETE ?scope=this|future|all` (`domain.EditScope`, reused by recurrences; `future` = number >= the addressed one's; on a transaction outside a purchase, that one alone). A scoped patch propagates the **changes** made to the addressed installment, not the fields sent (`ApplyInScope`): amount per installment, category, description, tags, account; a new day shifts the others by months on the new day; a new day/account/invoice gives installment *j* the addressed invoice + (*j*-*k*). A purchase stays on one account: `accountId` needs `scope=all` (`scope: invalid`). The count never changes (delete with `all` and recreate). No multi-document transactions: a failed `InsertMany` is deleted once with `context.WithoutCancel`; a scoped patch is one unordered `BulkWrite`, idempotent when resent. Partial index `transactions_user_installment_date_id`.
- **Not yet:** changing the email address (needs a verification flow for the new address) and the LGPD data export (lands with `/reports/export`).
- **Validation:** `go-playground/validator` on request DTOs (size bounds, required fields); the domain rules live in `internal/domain`. Bodies must be `application/json`, and unknown fields are rejected.
- **Go:** `go 1.27` with `toolchain go1.27.3`; module `github.com/NickeAlves/monthra-server`.
- **Config:** `caarlos0/env/v11` (environment variables only; `.env` is loaded by Docker Compose or `make run`).
- **Ports:** API `8080`; Prometheus `/metrics` on a separate internal port `9464` (`METRICS_ADDR`), never exposed through the public proxy.
- **Health:** `/healthz` and `/readyz` live outside `/api/v1`; the distroless image runs `api healthcheck` for its Docker HEALTHCHECK.
- **MongoDB and the host kernel:** MongoDB exits at startup on Linux kernels 6.19 to 7.0.13 (tcmalloc/rseq incompatibility, SERVER-121912 / SERVER-125742; older builds risked corruption). Any host running `mongod` (CI runners, staging/production VMs) needs kernel <= 6.18 or >= 7.0.14, or use a managed MongoDB. CI integration tests therefore run on `ubuntu-22.04` (kernel 6.8).

---

## 1. Visão geral do produto

**Monthra** é uma plataforma de finanças pessoais para gerenciar:

- **Gastos** (despesas): categorizadas, recorrentes ou avulsas, com método de pagamento.
- **Entradas** (receitas): salário, freelas, reembolsos, recorrentes ou avulsas.
- **Investimentos**: aportes, resgates, tipo de ativo (renda fixa, ações, FIIs, cripto, fundos, previdência), rentabilidade e evolução patrimonial.

Funcionalidades centrais:

1. Autenticação segura (cadastro, login, verificação de e-mail, recuperação de senha, MFA/TOTP opcional).
2. Dashboard mensal: saldo do mês, entradas x saídas, gastos por categoria, evolução do patrimônio.
3. CRUD de transações (gasto/entrada) com filtros por período, categoria, conta, tags.
4. Contas/carteiras (conta corrente, cartão de crédito, dinheiro, corretora).
5. Categorias personalizáveis (com ícone e cor).
6. Orçamentos mensais por categoria com alertas de limite.
7. Carteira de investimentos com histórico de aportes e posição consolidada.
8. Relatórios e exportação (CSV/PDF).
9. Preferências do perfil: moeda (padrão BRL), **idioma (Português pt-BR, Español es-ES, English en-US)** e **tema (Sistema, Claro ou Escuro)**. Detalhes em §7.6.
10. **Open Finance**: o usuário conecta suas contas de bancos, cartões e corretoras (Open Finance Brasil, via agregador regulado) e o Monthra importa automaticamente contas, saldos, transações, faturas e investimentos. Detalhes em §5.6, §6.9 e §7.7.

Plataformas: **Web (desktop/laptop), Android e iOS** — um único código Flutter.

---

## 2. Repositórios e responsabilidades

| Repositório | Responsabilidade |
|---|---|
| [`monthra-server`](https://github.com/NickeAlves/monthra-server) | API em **Go**, banco **MongoDB**, cache (**Redis**), autenticação, rate limit, regras de negócio, migrations/índices, métricas e traces da API. |
| [`monthra-app`](https://github.com/NickeAlves/monthra-app) | App **Flutter** (Web + Android + iOS), design system, integração com a API. |
| [`monthra-infra`](https://github.com/NickeAlves/monthra-infra) | **Terraform** (deploy/IaC), observabilidade (**Prometheus, Grafana, Loki, Tempo, Alertmanager, OpenTelemetry Collector**), compose de orquestração de todo o ambiente, versionamento/release, CI/CD compartilhado. |

Regras gerais:

- Nunca misture responsabilidades entre repos. Ex.: dashboards do Grafana ficam no `monthra-infra`, mas o endpoint `/metrics` é implementado no `monthra-server`.
- Todos os repos usam **Conventional Commits** (`feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, `test:`, `ci:`) e **SemVer** com tags `vX.Y.Z`.
- Branches fixas: `main` (produção), `staging` (homologação) e `develop` (desenvolvimento) — as três protegidas, só recebem código por PR (sem push direto, sem force-push).
- Branches de trabalho saem **sempre da `develop`** e voltam para ela por PR: `feature/<nome>` (ex.: `feature/new-service`) e `fix/<nome>`.
- Promoção entre ambientes por PR: `develop` → `staging` → `main`. Hotfix urgente: `fix/<nome>` a partir da `main`, PR para `main` e depois back-merge para `staging` e `develop`.
- Idioma: código, comentários, README e commits em **inglês**; textos da interface via i18n (§7.6).
- Cada repo deve ter: `README.md`, `CLAUDE.md`, `.editorconfig`, `.gitignore`, `.env.example` (nunca `.env` commitado), `Dockerfile`, `docker-compose.yml`, `Makefile` e workflow de CI em `.github/workflows/`.

---

## 3. Ambientes

| Ambiente | Nome | Como sobe | Observações |
|---|---|---|---|
| Local | `local` | `docker compose up` em cada repo, ou tudo junto via `monthra-infra` | Hot reload, dados fake (seed), Mailpit para e-mails. |
| Homologação | `staging` | `docker compose -f docker-compose.yml -f docker-compose.staging.yml` localmente **ou** deploy via Terraform | Espelho de produção, dados anonimizados, TLS. |
| Produção | `production` | Terraform + CI/CD a partir de tags | Somente via pipeline, nunca deploy manual. |

Variável `APP_ENV` (`local` | `staging` | `production`) controla comportamentos em todos os serviços.

Mapeamento branch → ambiente: `develop` (e PRs) ⇒ só CI; `staging` ⇒ deploy em **staging**; tag `vX.Y.Z` na `main` ⇒ deploy em **produção** (com aprovação).

**Rede Docker compartilhada:** todos os composes usam a rede externa `monthra-net` para que os repos se comuniquem quando executados separadamente:

```bash
docker network create monthra-net
```

---

## 5. monthra-server (Go + MongoDB + Redis)

### 5.1 Stack
- **Go** (última versão estável), módulos Go.
- Router: **chi** (`go-chi/chi/v5`).
- MongoDB: driver oficial `go.mongodb.org/mongo-driver/v2`.
- Redis: `redis/go-redis/v9` (cache, rate limit, sessões/refresh tokens, blacklist).
- Config: `caarlos0/env` ou `spf13/viper` + `.env` em local.
- Logs: `log/slog` em **JSON** estruturado (com `request_id`, `trace_id`, `user_id` quando houver). Nunca logar senha, token, CPF ou dados financeiros sensíveis.
- Validação: `go-playground/validator`.
- Observabilidade: `prometheus/client_golang` (`/metrics`) + **OpenTelemetry** (traces OTLP para o collector).
- Documentação: **OpenAPI 3** (`docs/openapi.yaml`), servido em `/docs` apenas fora de produção.
- Testes: `testing` + `testify` + **testcontainers-go** (Mongo/Redis reais nos testes de integração).
- Lint: `golangci-lint`; segurança: `govulncheck`, `gosec`.

### 5.2 Arquitetura (Clean / Hexagonal)
```
monthra-server/
├── cmd/api/main.go
├── internal/
│   ├── config/
│   ├── domain/            # entidades e regras puras (User, Account, Transaction, Category, Budget, Investment)
│   ├── usecase/           # casos de uso / serviços
│   ├── repository/        # interfaces + impl mongo/
│   ├── cache/             # redis
│   ├── http/
│   │   ├── handler/
│   │   ├── middleware/    # auth, csrf, ratelimit, cors, security headers, logging, recover, request id
│   │   ├── dto/
│   │   └── router.go
│   ├── auth/              # jwt, hashing, totp, tokens
│   └── observability/     # metrics, tracing, logger
├── migrations/            # criação de índices e seeds
├── docs/openapi.yaml
├── Dockerfile
├── docker-compose.yml
├── docker-compose.staging.yml
├── Makefile
└── .env.example
```

### 5.3 Modelagem MongoDB (coleções principais)
- `users` — email (índice único, lowercase), password_hash, email_verified, mfa_secret (criptografado), preferences `{ locale: "pt-BR"|"es-ES"|"en-US", theme: "system"|"light"|"dark", currency }`, created_at, updated_at, deleted_at.
- `accounts` — user_id, name, type (checking, credit_card, cash, brokerage), currency, initial_balance.
- `categories` — user_id, name, kind (expense|income), icon (nome lucide), color.
- `transactions` — user_id, account_id, category_id, kind (expense|income|transfer), amount, date, description, tags, recurrence_id, created_at.
- `recurrences` — regra de recorrência (RRULE simplificada).
- `budgets` — user_id, category_id, month (YYYY-MM), limit.
- `investments` / `investment_movements` — ativo, tipo, aportes/resgates, quantidade, preço.
- `audit_logs` — ações sensíveis (login, troca de senha, MFA, export), TTL configurável.
- `refresh_tokens` (ou Redis) — família de tokens para detecção de reuso.
- `bank_connections` — conexões de Open Finance (§5.6).
- `accounts`, `transactions` e `investments` têm `source` (`manual` | `open_finance`) e, quando importados, `connection_id` e `external_id` (§5.6).

Regras:
- **Valores monetários em inteiros (centavos, `int64`)** ou `Decimal128` — **nunca `float`**.
- Datas sempre em **UTC**; o app converte para o fuso do usuário.
- **Toda query filtra por `user_id`** obtido do token (nunca do body) — impedir IDOR.
- Índices: `{user_id:1, date:-1}` em transactions, `{user_id:1, month:1, category_id:1}` único em budgets, etc. Criados no boot via `migrations/`.
- Soft delete onde fizer sentido.
- Paginação por cursor nas listagens.

### 5.4 Cache (Redis)
- Cache-aside para dashboard/resumos mensais (`summary:{user_id}:{YYYY-MM}`), TTL curto (ex.: 5 min) e **invalidação** ao criar/editar/excluir transação.
- Cache de categorias padrão e cotações (se integrar API de cotações).
- Headers `ETag`/`Cache-Control: private` em respostas GET adequadas; `no-store` em respostas com dados sensíveis.
- Chaves sempre com namespace por ambiente: `monthra:{env}:...`.

### 5.5 API — convenções
- Base: `/api/v1`. JSON em camelCase. Erros no formato **RFC 7807** (`application/problem+json`).
- Health: `/healthz` (liveness) e `/readyz` (checa Mongo e Redis).
- Endpoints principais:
  - `POST /auth/register`, `POST /auth/login`, `POST /auth/refresh`, `POST /auth/logout`, `POST /auth/verify-email`, `POST /auth/forgot-password`, `POST /auth/reset-password`, `POST /auth/mfa/setup`, `POST /auth/mfa/verify`, `GET /auth/csrf`
  - `GET/PATCH /me`, `DELETE /me` (exclusão de conta — LGPD)
  - `GET/PATCH /me/preferences` — `locale` e `theme` (validar contra os valores permitidos)
- Mensagens de erro (problem+json `title`/`detail`) e e-mails transacionais traduzidos em **pt-BR, es-ES e en-US**, escolhidos pela preferência do usuário ou pelo header `Accept-Language` (antes do login).
  - `/accounts`, `/categories`, `/transactions`, `/budgets`, `/investments`, `/investments/{id}/movements`
  - `GET /dashboard/summary?month=YYYY-MM`, `GET /reports/export?format=csv`
  - `/open-finance/...` e `POST /webhooks/open-finance/{provider}` (§5.6)
- Graceful shutdown, timeouts de servidor (`ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`), limite de tamanho de body (ex.: 1 MB).

### 5.6 Open Finance (conexão com bancos, cartões e corretoras)
Objetivo: o usuário conecta suas contas e o Monthra importa contas, saldos, transações, faturas de cartão e investimentos automaticamente.

- **Escopo:** somente **leitura de dados** do **Open Finance Brasil**. Iniciação de pagamento (Pix via ITP) e outros países ficam **fora de escopo** até decisão explícita.
- **Agregador regulado:** o Monthra não é instituição autorizada pelo Banco Central, então usa um agregador (candidatos: **Pluggy**, **Belvo**; outros: Klavi, Celcoin). **Pergunte qual usar antes de implementar** (custo por conexão, cobertura de instituições, widget/SDK, sandbox). Não presuma.
- **Arquitetura (porta + adapter):** interface `OpenFinanceProvider` em `internal/openfinance/` (criar connect token, consultar conexão, listar contas, listar transações desde uma data, listar investimentos, revogar conexão, verificar webhook) e um adapter por provedor (`internal/openfinance/<provedor>/`). O domínio nunca conhece tipos do provedor; a conversão acontece no adapter (valores para centavos `int64`, datas para UTC).
- **Fluxo de conexão:**
  1. App chama `POST /open-finance/connect-token`; o server gera um token curto no provedor (client id/secret **só no server**).
  2. App abre o widget do provedor; o usuário escolhe a instituição, autentica **no próprio banco** e dá o consentimento. O Monthra **nunca vê nem armazena credenciais bancárias**.
  3. Widget devolve o id da conexão; app chama `POST /open-finance/connections`. O server confirma no provedor que a conexão existe e **nasceu do connect token deste usuário** (impede registrar a conexão de outra pessoa), salva e dispara a primeira sincronização.
  4. Sincronização por **webhook** do provedor + **job periódico de reconciliação**; incremental (`desde = última sync − margem`, para pegar transações atrasadas/pendentes) e **idempotente**; roda em worker com fila no Redis e **lock por conexão** (nunca duas syncs da mesma conexão em paralelo).
- **Modelagem:**
  - `bank_connections` — user_id, provider, provider_connection_id (**criptografado AES-256-GCM**), institution `{ id, name, logo_url }`, status (`syncing` | `active` | `needs_reauth` | `error` | `revoked` | `expired`), consent_expires_at, last_synced_at, last_error_code, created_at, updated_at, deleted_at.
  - Itens importados: `source: "open_finance"`, `connection_id`, `external_id`; índice **único parcial** `{user_id:1, connection_id:1, external_id:1}` para deduplicação. Transações `pending` são atualizadas quando liquidam.
  - Categorização: categoria do provedor mapeada para as categorias do usuário + regras do usuário ("descrição contém X → categoria Y"). Recategorização manual prevalece nas próximas sincronizações.
  - Em itens importados, valor e data vêm da instituição e não são editáveis; categoria, tags e descrição são.
- **Endpoints** (`/api/v1/open-finance`): `GET /institutions?search=`, `POST /connect-token`, `GET/POST /connections`, `GET /connections/{id}`, `POST /connections/{id}/sync` (rate limit próprio, ex.: 1 a cada 5 min por conexão), `POST /connections/{id}/reconnect` (novo token para reautenticar/renovar consentimento), `DELETE /connections/{id}?keepData=true|false`. Webhook: `POST /webhooks/open-finance/{provider}` (fora de `/api/v1`, sem cookie/JWT).
- **Consentimento e LGPD:** registrar consentimento (finalidade, escopos, data, validade) em `audit_logs`; avisar antes de expirar (e-mail + banner no app) com renovação em 1 clique; desconectar revoga no provedor e, conforme escolha do usuário, apaga ou mantém os dados importados; `DELETE /me` revoga todas as conexões.
- **Cache:** cada sync invalida `summary:{user_id}:{YYYY-MM}` dos meses afetados.

---

## 6. Segurança (obrigatório — aplicar em todos os repos)

> Segurança não é opcional. Ao implementar qualquer feature, verifique esta lista. Siga **OWASP ASVS nível 2** e **OWASP API Security Top 10**.

### 6.1 Senhas e identidade
- Hash com **Argon2id** (`golang.org/x/crypto/argon2`) com parâmetros robustos (ex.: m=64MB, t=3, p=2) e salt aleatório.
- Política: mínimo 10 caracteres, checagem contra senhas vazadas (k-anonymity HIBP opcional), sem regras de composição absurdas.
- Verificação de e-mail obrigatória antes de usar a conta.
- Mensagens genéricas em login/recuperação ("credenciais inválidas") — **sem enumeração de usuários**.
- **MFA TOTP** opcional (RFC 6238) com códigos de recuperação (armazenados com hash).
- Bloqueio progressivo após tentativas falhas (por conta e por IP) + backoff.
- Tokens de reset/verificação: aleatórios (32 bytes, `crypto/rand`), armazenados **com hash**, uso único, expiração curta (15–30 min).

### 6.2 Tokens (JWT + refresh)
- **Access token JWT** curto (**10–15 min**), assinado com **EdDSA (Ed25519)** ou RS256, com `kid` para rotação de chaves. Claims: `sub`, `iat`, `exp`, `nbf`, `iss`, `aud`, `jti`. Validar **todas** as claims e fixar o algoritmo (rejeitar `none`/troca de alg).
- **Refresh token opaco** (não JWT), longo (ex.: 30 dias), armazenado com hash no Redis/Mongo, com **rotação a cada uso** e **detecção de reuso** (reuso ⇒ revoga toda a família e força novo login).
- Logout revoga refresh token e adiciona `jti` do access à denylist até expirar.
- Lista de sessões/dispositivos ativos com opção de revogar.
- **Armazenamento no cliente:**
  - **Web:** access e refresh em **cookies `HttpOnly`, `Secure`, `SameSite=Strict`** (refresh com `Path=/api/v1/auth/refresh`), prefixo `__Host-`. Nunca em `localStorage`.
  - **Mobile:** tokens no **`flutter_secure_storage`** (Keychain no iOS / Keystore no Android), enviados via header `Authorization: Bearer`.

### 6.3 CSRF
- Como a web usa cookies, aplicar **double-submit token assinado** (HMAC) ou synchronizer token: `GET /auth/csrf` devolve token em cookie não-HttpOnly + o cliente envia em `X-CSRF-Token` em todo método mutável (POST/PUT/PATCH/DELETE).
- Somado a `SameSite=Strict` e validação de **`Origin`/`Referer`** contra allowlist.
- Requisições com `Authorization: Bearer` (mobile) não dependem de cookie e ficam isentas do CSRF, mas o middleware deve garantir que cookie **e** header nunca sejam aceitos misturados de forma insegura.

### 6.4 Rate limiting e abuso
- Rate limit distribuído no **Redis** (sliding window ou token bucket), por **IP** e por **usuário**:
  - Auth (login, register, forgot): ex.: 5 req/min por IP e por e-mail.
  - API autenticada: ex.: 100 req/min por usuário.
  - Export/relatórios: limites mais baixos.
- Respostas `429` com `Retry-After` e headers `RateLimit-*`.
- Atrás de proxy, confiar em `X-Forwarded-For` **apenas** de proxies conhecidos.
- CAPTCHA (ex.: Cloudflare Turnstile) após falhas repetidas no login/cadastro.

### 6.5 Headers e transporte
- **TLS 1.2+ obrigatório** em staging/produção (Traefik/Caddy com Let's Encrypt), **HSTS** (`max-age=63072000; includeSubDomains; preload`).
- Headers: `Content-Security-Policy` estrita (sem `unsafe-inline` onde possível; o build web do Flutter deve ser compatível), `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY` / `frame-ancestors 'none'`, `Referrer-Policy: strict-origin-when-cross-origin`, `Permissions-Policy` restritiva, `Cross-Origin-Opener-Policy: same-origin`.
- **CORS** com allowlist explícita de origens por ambiente, `AllowCredentials: true` só para essas origens, nunca `*`.
- Remover header `Server`/versões.

### 6.6 Autorização e dados
- Autorização em toda rota: o recurso pertence ao `user_id` do token (prevenir **IDOR/BOLA**).
- Validação e sanitização de toda entrada; whitelist de campos (prevenir **mass assignment**); impedir **NoSQL injection** (nunca montar filtros a partir de JSON cru do cliente, rejeitar chaves começando com `$`).
- Dados sensíveis (segredo MFA, eventualmente descrições) criptografados em repouso com **AES-256-GCM** (chave via secret manager); MongoDB com autenticação, usuário de menor privilégio, sem exposição pública, TLS e backups criptografados.
- Respostas nunca expõem stack traces, hashes ou campos internos.
- **LGPD:** exportação e exclusão de dados do usuário, política de retenção, consentimento, logs sem PII.
- Audit log de eventos sensíveis.

### 6.7 Segredos e supply chain
- Nenhum segredo no código ou em imagens Docker. Local: `.env` (gitignored). Staging/prod: secret manager do provedor (ou Docker secrets/SOPS), injetado via Terraform.
- Rotação de chaves JWT suportada (JWKS interno com múltiplos `kid`).
- CI com: `govulncheck`, `gosec`, `golangci-lint`, `flutter analyze`, `dart pub outdated`, **Trivy** (imagens e IaC), **gitleaks** (segredos), **tfsec/checkov** (Terraform), Dependabot/Renovate.
- Imagens Docker: multi-stage, base **distroless** ou `alpine` mínima, **usuário não-root**, `read_only` filesystem, `no-new-privileges`, healthcheck, versões fixas (digest).

### 6.8 App Flutter
- Certificate pinning opcional no mobile (configurável), ofuscação no build release (`--obfuscate --split-debug-info`).
- Bloquear screenshots em telas sensíveis no Android (`FLAG_SECURE`) — opcional por preferência.
- Biometria (`local_auth`) para desbloquear o app no mobile.
- Auto-logout por inatividade configurável.
- Nunca logar tokens; desabilitar logs verbosos em release.

### 6.9 Open Finance
- Credenciais do agregador (client id/secret, segredo de webhook) **só no server**, via secret manager; nunca no app, em imagens ou em logs.
- Webhook: validar assinatura/segredo do provedor (e allowlist de IP, se o provedor publicar), limitar tamanho do body, responder 2xx rápido e processar assíncrono, idempotência por id de evento, e **nunca confiar no payload** — rebuscar os dados na API do provedor.
- Webhook isento de CSRF (não é chamado pelo navegador), mas com rate limit próprio.
- `provider_connection_id` tratado como segredo: criptografado em repouso e nunca exposto em respostas (a API usa o id interno).
- Toda operação sobre conexão checa o `user_id` do token (IDOR/BOLA).
- Sandbox do provedor em `local`/`staging`; chaves de produção só em `production`.
- Audit log de conectar, reconectar, sincronizar manualmente e desconectar.

---

## 9. Docker e Docker Compose

### 9.1 Por repositório (rodar isolado)
- **monthra-server/docker-compose.yml:** `api` (com hot reload via `air` em local), `mongo` (com usuário/senha e volume), `redis` (com senha), `mongo-express` opcional (só local), `mailpit` (e-mails locais).
- **monthra-app/docker-compose.yml:** `web` (nginx servindo o build), apontando para a API via `monthra-net`.
- **monthra-infra/compose/docker-compose.yml:** sobe tudo, incluindo `prometheus`, `grafana`, `loki`, `tempo`, `alertmanager`, `otel-collector`/`alloy`, exporters e `traefik`/`caddy`.

### 9.2 Regras
- Healthchecks em todos os serviços e `depends_on: condition: service_healthy`.
- Volumes nomeados para dados; `restart: unless-stopped`.
- Limites de CPU/memória definidos.
- Portas de banco/cache **não** expostas no host em staging.
- `docker-compose.staging.yml` como override: TLS, sem hot reload, sem ferramentas de debug, `APP_ENV=staging`.

### 9.3 Comandos padrão (Makefile em cada repo)
```
make up          # sobe ambiente local
make down        # derruba
make logs        # logs agregados
make test        # testes
make lint        # lint + security scan
make build       # build da imagem
make seed        # dados fake (server)
make staging-up  # sobe homologação local
```

---

## 10. Como trabalhar (regras para o Claude Code)

1. **Ordem de implementação sugerida:**
   1. `monthra-server`: esqueleto, config, logger, health, Docker/compose, Mongo/Redis, índices.
   2. Autenticação completa + todos os middlewares de segurança (§6) + testes.
   3. Domínio: accounts → categories → transactions → dashboard → budgets → investments → **open finance** (§5.6, após eu escolher o agregador) → reports.
   4. `monthra-app`: design system e tema a partir do pack de marca → auth → dashboard → demais features → conexões bancárias (§7.7).
   5. `monthra-infra`: compose integrado + observabilidade → Terraform (após eu escolher o provedor) → CI/CD.
2. Trabalhe em **pequenos incrementos**, um PR por feature (`feature/<nome>` a partir da `develop`, PR para `develop`), com testes. Rode `make lint` e `make test` antes de commitar.
3. Cobertura mínima: **80%** em `domain`/`usecase`/`auth` do server; testes de widget para componentes do design system.
4. Mantenha o **OpenAPI** sincronizado com o código; o app consome os contratos dele.
5. Atualize o `README.md` de cada repo com como rodar localmente e em staging.
6. Ao criar o `CLAUDE.md` de cada repo, inclua: as §1–3, a seção específica do repo (§5, §7 ou §8), a §6 completa e a §9–10.
7. Se uma decisão não estiver coberta aqui (ex.: provedor de cloud, agregador de Open Finance, serviço de e-mail, canal de alertas, bundle ID do app), **pergunte antes** de implementar.
8. Nunca commite segredos, `.env`, chaves, `terraform.tfstate` ou `*.tfvars` reais.
9. Código, comentários, README e commits em **inglês** (commits em Conventional Commits); textos da interface via i18n (pt-BR padrão, es-ES, en-US).
10. Priorize: **segurança > corretude > clareza > performance > conveniência**.
