# attendance-system

Employee attendance backend: authentication, tiered RBAC, attendance with GPS + selfie
verification, daily reports, leave requests with approval, and summary reports for supervisors.

A single Go binary + PostgreSQL + S3 object storage (Cloudflare R2 in production, MinIO in
development).

## Status

Built incrementally in 7 phases — the full plan is in [`docs/plan.md`](docs/plan.md).
**Phases 0–2 are done** — skeleton, migrations, auth, presigned uploads, and the employee
directory: users, the manager hierarchy, departments, and role grants recorded in `audit_logs`.
Live endpoints: `GET /health`, `POST /auth/{login,refresh,logout}`, `GET /me`, `/users` and
`/users/{id}` (+ `/role`), `/departments` and `/departments/{id}`, `POST /uploads/intent`.

Who sees whom follows `manager_id`: a supervisor reaches their own subtree, hr_admin and
super_admin reach everyone, and only super_admin grants roles.

To get a first account, set `AUTH_SEED_ADMIN_EMAIL` and `AUTH_SEED_ADMIN_PASSWORD` in `.env`; the
API creates that super_admin at startup if the email is free.

## Running

```sh
cp configs/config.example.yaml configs/config.yaml
cp .env.example .env

docker compose up -d --wait  # postgres, redis, minio; --wait until the bucket exists
make migrate-up             # apply migrations
make run                    # API on :8080

curl localhost:8080/health
```

## Commands

`make help` lists everything. The common ones:

| Command | Purpose |
|---|---|
| `make run` | run the API (`APP=migrate` for the other binary) |
| `make check` | fmt-check + vet + lint + test (exactly what CI runs) |
| `make test` | tests with the race detector |
| `make cover` | coverage report in the browser |
| `make fmt` | fix formatting (`check` only verifies) |
| `make migrate-up` / `migrate-down` / `migrate-status` | goose migrations |
| `make oapicodegen` | generate server code from the OpenAPI specs |

## Layout

```
api/server/specs/       OpenAPI specs, one file per module
cmd/api/                HTTP server
cmd/migrate/            goose migration runner
internal/api/server/
  modules/<name>/       handler -> service -> repository
  oapicodegen/          generated code (do not edit)
internal/config/        config loader (configs/config.yaml + .env + env)
internal/container/     shared dependencies, opened once at startup
internal/db/migrations/ SQL migrations, embedded in the binary
internal/middleware/    request id, access log, recovery, timeout, CORS, security headers
internal/pkg/           generic helpers shared only within this module (storage, util)
pkg/                    wrappers other modules may import
  database, redis, logger
docs/plan.md            the 7-phase plan
```

Adding a module: write `api/server/specs/<name>.yaml`, run `make oapicodegen`, implement the
generated `StrictServerInterface` in `internal/api/server/modules/<name>/`, then register it in
`internal/api/server/router.go`. Validation rules live in the **service**, not the handler.

## Tests that need services

The `cmd/migrate` and `internal/pkg/storage` tests `t.Skip` automatically when Postgres/MinIO is
unreachable, so `make check` stays green without anything running. If you moved the compose ports
via `.env`, tell the tests too:

```sh
POSTGRES_TEST_PORT=5433 make check
```

CI sets `POSTGRES_TEST_REQUIRED` and `STORAGE_TEST_REQUIRED` so a skip there becomes a failure.

## Notes

- The schema is owned by goose, not GORM `AutoMigrate`.
- All timestamps are `timestamptz` UTC; the work date is stored as a separate `date` column.
- Files never pass through the server: clients PUT/GET directly to object storage via presigned
  URLs signed by the server.
