# attendance-system

Employee attendance backend: authentication, tiered RBAC, attendance with GPS + selfie
verification, daily reports, leave requests with approval, and summary reports for supervisors.

A single Go binary + PostgreSQL + S3 object storage (Cloudflare R2 in production, MinIO in
development).

## Status

Built incrementally, one reviewed phase at a time. **Done:** skeleton, migrations, auth, presigned
uploads, the employee directory (users, the manager hierarchy, departments, role grants in
`audit_logs`), work schedules and the roster, and attendance: check-in/out with a selfie and GPS,
the daily report, who's in, and corrections approved up the hierarchy. **Still to come:** leave
(statutory leave types, balances, approval, attachments) and reports (JSON, CSV, PDF).

Live endpoints, all specified in `api/server/specs/`:

| Area | Endpoints |
|---|---|
| Health, auth | `GET /health`, `POST /auth/{login,refresh,logout}` |
| Directory | `GET /me`, `/users` and `/users/{id}`, `PATCH /users/{id}/role`, `/departments` and `/departments/{id}` |
| Uploads | `POST /uploads/intent` |
| Schedules | `GET /me/schedule`, `/work-schedules`, `/holidays`, `/office-locations`, `/shift-assignments` |
| Attendance | `POST /attendance/{check-in,check-out}`, `GET /attendance/me`, `PUT /attendance/me/daily-report`, `GET /attendance/{id}/photo/{in,out}`, `GET /attendance/whos-in` |
| Corrections | `POST /attendance/corrections`, `GET /attendance/corrections/{me,pending}`, `DELETE /attendance/corrections/{id}`, `POST /attendance/corrections/{id}/decision` |

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
```

Adding a module: write `api/server/specs/<name>.yaml`, run `make oapicodegen`, implement the
generated `StrictServerInterface` in `internal/api/server/modules/<name>/`, then register it in
`internal/api/server/router.go`. Validation rules live in the **service**, not the handler.

## Tests that need services

Every test that needs Postgres or MinIO (`cmd/migrate`, `internal/pkg/storage`, and the modules)
`t.Skip`s automatically when it is unreachable, so `make check` stays green without anything
running. If you moved the compose ports via `.env`, tell the tests too:

```sh
POSTGRES_TEST_PORT=5433 make check
```

CI sets `POSTGRES_TEST_REQUIRED` and `STORAGE_TEST_REQUIRED` so a skip there becomes a failure.

## Notes

- The schema is owned by goose, not GORM `AutoMigrate`.
- All timestamps are `timestamptz` UTC; the work date is stored as a separate `date` column, so a
  22:00–06:00 night shift stays one day.
- `attendance.timezone` (required, e.g. `Asia/Jakarta`) is the zone schedule times and every "today"
  are read in. `attendance.geofence_enforce: false` only flags a check-in outside every office;
  `true` refuses it.
- Attendance is never edited directly: every change after the fact is a correction that someone
  above the employee approves, and the correction keeps the times it replaced.
- Files never pass through the server: clients PUT/GET directly to object storage via presigned
  URLs signed by the server.
