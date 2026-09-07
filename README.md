# go-template

A Go service template: config-driven dependencies, an OpenAPI-first HTTP layer, structured logging, and graceful shutdown. Clone it, delete the example module, and start writing your own.

## Quickstart

```bash
cp configs/config.example.yaml configs/config.yaml
cp .env.example .env
docker compose up -d --wait   # PostgreSQL + Redis on the ports config.yaml expects
make run
```

The defaults in `config.example.yaml` match `compose.yaml`, so nothing needs editing to get a running server. `--wait` matters on a fresh clone: the app pings every dependency at startup, and Postgres does not accept connections until it has run the schema below.

```bash
curl localhost:8080/health
# {"condition":"Healthy"}

curl localhost:8080/example/visits
# {"visits":1}                            <- Redis

curl -X POST localhost:8080/example/notes -H 'Content-Type: application/json' \
     -d '{"title":"First note","body":"Written from the example module."}'
# {"id":1,"title":"First note","body":"...","created_at":"..."}   <- PostgreSQL

curl 'localhost:8080/example/notes?limit=5'
```

Run `make help` for every target.

## The example module

`modules/example` exists to show the wiring, over both dependencies at once. Delete it — with `api/server/specs/example.yaml`, its generated package, and `scripts/schema.sql` — once you have your own.

| Route | Shows |
| --- | --- |
| `GET /example/visits` | Reaching a client (Redis) from the service layer |
| `GET`/`POST /example/notes` | Input validation, the full path down to PostgreSQL, a transaction, and logging with the request ID |

It is split into four files, the shape to follow for a module that has dependencies. Layers only ever point downwards:

| File | Holds | Knows about |
| --- | --- | --- |
| `constant.go` | Keys and limits | nothing |
| `handler.go` | Generated request in, generated response out | the service and the logger |
| `service.go` | Validation, defaults, the actual behaviour | the repository and any clients |
| `repository.go` | The stored rows and their queries | `*gorm.DB` |

The handler does no validation and holds no client, so the rules are testable without HTTP; the service returns a `ValidationError` for a caller mistake and a wrapped error for anything else, and the handler turns the first into the spec's `400` and lets the second become a `500`. Nothing above `repository.go` sees a `*gorm.DB`.

Take only the layers you need. `modules/health` has no dependencies and no rules, so it is a single `handler.go` — add `service.go` when there is a rule to enforce, `repository.go` when there is a table.

`Repository.Create` writes the note and its audit row through `database.WithTransaction`, so a note can never exist without its event. Reach for that helper whenever two writes have to land together; a single write does not need one, because GORM already wraps it.

A module mounts only when **every** dependency it needs is configured. Remove `redis:` from `config.yaml` and all three routes return 404 rather than failing at startup or panicking on the first request. See `register` in `internal/api/server/router.go`.

The `example_notes` table comes from `scripts/schema.sql`, which compose mounts into the Postgres entrypoint — it runs once, when the data volume is first created. After editing it, replay with `docker compose down -v && docker compose up -d`. Reach for a migration tool (goose, atlas) once you have a schema that has to change in place.

## Adding a module

1. Write `api/server/specs/<name>.yaml`.
2. `make oapicodegen` — generates `internal/api/server/oapicodegen/<name>/`.
3. Implement `StrictServerInterface` in `internal/api/server/modules/<name>/`, following the four files above.
4. Register it in `register()` in `internal/api/server/router.go`, wiring `NewRepository` → `NewService` → `NewHandler`.
5. If it needs a table, add it to `scripts/schema.sql` and replay with `docker compose down -v && docker compose up -d`.

One spec per module: every route in `<name>.yaml` is registered in a single call, so a module is mounted whole or not at all.

The generated server does not validate request bodies against the schema. Validate in the service and return a `ValidationError` — `modules/example/service.go` does this.

## Configuration

`configs/config.yaml` is the source of truth. It and `.env` are both gitignored, which is why the repo ships `.example` copies of each. Values are overridden by `.env` and real environment variables, using the config path uppercased with `_` between segments:

```
databases.postgres.example.password  ->  DATABASES_POSTGRES_EXAMPLE_PASSWORD
```

The key must already exist in `config.yaml` — an environment variable for a key that is not in the file is ignored. Keep secrets out of the YAML and set them this way.

Every entry under `databases:` and `redis:` is opened **and pinged** at startup, so a block you are not running yet must be removed or commented out, not left blank. `services:` only builds HTTP clients; an unreachable `base_url` costs nothing until a handler calls it, though a malformed one is rejected at startup along with a service that declares no endpoints.

`server.timeout` is the deadline a handler and everything it calls gets. The server's write timeout is derived from it, so raising one raises the other; keep every upstream `services.*.timeout` below it, or the request dies before the shorter deadline can fire.

## Layout

```
api/server/specs/      OpenAPI specs, one file per module
scripts/               Codegen script and the example schema compose loads
cmd/api/               Entry point: load config, build container, serve, shut down
internal/api/server/   Router, generated contracts, module handlers
internal/config/       Config structs, loading, validation
internal/container/    Opens every configured dependency; Close unwinds in reverse
internal/httpclient/   The app's configured upstream clients
internal/middleware/   Request ID, access log, recovery, timeout, security headers, CORS, body limit
pkg/                   Reusable, app-agnostic: database, redis, logger, httpclient, util
```

`internal/` is app-specific and free to change; `pkg/` is meant to be lifted into other services.

### Dependencies

`container.NewContainer` opens everything in `config.yaml` and registers a shutdown hook per connection. `Close` runs them in reverse so the logger outlives anything that might log while closing. If one dependency fails to open, the ones already open are closed before the error is returned.

Handlers receive what they need through their constructor — they never see the container.

## Requests

Every request passes through `middleware.Default`: request ID, access log, panic recovery, timeout, security headers, CORS, and a body-size limit. `ContextWithFallback` is on, so the `context.Context` a handler receives carries the timeout deadline — pass it to every database, cache, and HTTP call and a stalled dependency cannot outlive the request.

That same context carries the request ID. Read it with `middleware.RequestIDFrom(ctx)` and every line you log lands next to the access-log line for the same request; `modules/example/handler.go` does this after storing a note.

## Make targets

| Target | |
| --- | --- |
| `make run` | Run an app (`APP=name`, default: first in `cmd/`) |
| `make test` | Tests with the race detector |
| `make cover` | Coverage report in the browser |
| `make cover-gaps` | List functions that are not fully covered |
| `make check` | fmt, vet, lint, test |
| `make lint-install` | Install the pinned golangci-lint |
| `make oapicodegen` | Regenerate server code from the specs |
| `make build` / `build-all` | Host binary / cross-compile |
| `make docker-build` / `docker-run` | Distroless image |

`docker-run` starts a container, so `localhost` in `config.yaml` points at that container, not your machine. Point the hosts at `host.docker.internal` (or run the app inside the compose network) when you containerise it.

## Requirements

Go 1.26+, Docker for the local dependencies. `make fmt` needs `goimports`; `make lint` needs golangci-lint, which `make lint-install` pins to the version `.golangci.yml` is written for — the config uses the v1 format, which v2 does not read.

Beyond the golangci-lint defaults the config turns on `errorlint` (a `%v` where `%w` was meant silently breaks `errors.Is`), `bodyclose`, `sqlclosecheck`, `revive`, and `lll` at 120 columns with tabs counted as four. Generated code under `oapicodegen/` is excluded, since `make oapicodegen` overwrites any fix made there. A `//nolint` must name its linter and give a reason.

## Coverage

`make cover-gaps` prints the current number and everything short of 100%. One branch is
knowingly uncovered: the fallback in `middleware.RequestID` for a failing `crypto/rand`.
Anything else that appears is a genuine gap.

`cmd/api` reaches its failure paths through the package-level seams in `main.go` — `exit`,
`listen`, `newRouter`, `closeDeps` — which tests swap to force an error the real process
cannot be made to produce. `internal/container` uses the same pattern.

`make cover` and `make cover-gaps` exclude generated code under `oapicodegen/` and pass
`-coverpkg`, so a module reached through the router is credited rather than reported as
untested. Without that flag, coverage is counted per package and anything exercised only
through another package's tests reads as 0%.

## License

MIT — see [LICENSE](LICENSE).
