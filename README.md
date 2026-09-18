# Malaka Backend

Go REST API for multi-user task management. SQLite by default, PostgreSQL for production.

## API

Public:

```text
POST /api/v1/auth/register
POST /api/v1/auth/login
GET  /healthz   liveness probe
GET  /readyz    readiness probe (database connectivity)
GET  /metrics   event bus metrics
```

Bearer-authenticated:

```text
POST   /api/v1/auth/logout
GET    /api/v1/auth/me

POST   /api/v1/tasks
GET    /api/v1/tasks?status=pending&search=report&limit=20&page=2
GET    /api/v1/tasks/statuses
GET    /api/v1/tasks/{id}
PUT    /api/v1/tasks/{id}
DELETE /api/v1/tasks/{id}
POST   /api/v1/tasks/{id}/assign
```

Task list parameters: `status` (exact match), `search` (case-insensitive title match, `%` and `_` are literal), `limit` (1-100, default 25), `page` (1-based) or `offset`. Invalid values return `422 VALIDATION_ERROR`. `GET /api/v1/tasks/statuses` returns `{value, label}` pairs for select inputs.

`POST /api/v1/tasks` accepts a UUID `Idempotency-Key`, scoped per user, method, and path with a request hash over method, query, and body. Replays within the TTL (default 24h) return the stored response byte-for-byte; reuse with a different payload returns `422 IDEMPOTENCY_KEY_MISMATCH`.

Assignment updates the task, appends `task_logs`, queues an outbox event, and writes audit data in one transaction. Every task statement is scoped to the owning user.

Errors use one envelope with `code`, `message`, `status`, and `timestamp`. Messages are English-only and never include SQL, stack traces, or internal details.

## Layout

```text
cmd/api/       HTTP server
cmd/migrate/   schema migration
cmd/seed/      seed users: sugeng, anita, adit
internal/app/  dependency wiring
internal/modules/iam/    register, login, JWT, sessions
internal/modules/tasks/  task CRUD and assignment
internal/platform/       database, audit, events, janitor, middleware, HTTP helpers
pkg/slogx/               structured logging with redaction
```

## Run locally

Requires Go 1.26+. SQLite needs no extra services.

```sh
cp .env.example .env    # set JWT_SECRET (32+ characters)
make migrate
make run                # seeds users, then starts the API
```

For PostgreSQL set `DB_DRIVER=postgres` and a PostgreSQL `DATABASE_URL`; `make migrate` applies the PostgreSQL schema instead of the embedded SQLite one.

With Docker Compose, API, PostgreSQL, and Caddy start together:

```sh
set -a; . ./.env; set +a
docker compose up --build -d
go run ./cmd/seed
docker compose down
```

Compose applies the PostgreSQL schema on first volume initialization. Seeded credentials are `Password123!`; change them outside local development.

## Database

Both drivers sit behind the same ports in `internal/platform/db`. SQLite applies its embedded schema at open; PostgreSQL is migrated by `cmd/migrate`. Only the outbox relay and janitor run in-process as background jobs.

## Checks

```sh
make check       # sqlc generate + test + vet + build
go test -short ./...   # SQLite paths only, no Docker
```

## Smoke test

```sh
curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"sugeng@example.com","password":"Password123!"}'

TOKEN=<token from the response>

curl -s -X POST http://localhost:8080/api/v1/tasks \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"title":"My task","status":"pending"}'

curl -s "http://localhost:8080/api/v1/tasks?status=pending&search=my&limit=25&page=1" \
  -H "Authorization: Bearer $TOKEN"
```

See `.env.example` for the supported environment variables.
