# Contributing

Issues and pull requests are welcome. Open an issue first for anything larger than a fix so the shape can be agreed before the code exists.

## Setup

Go 1.25 or newer, and the `claude` CLI on PATH for the commands that call a model. The SDKs under `sdk/` need [uv](https://docs.astral.sh/uv/) for Python and Node 24 with npm for TypeScript.

```
git clone https://github.com/jeon-jihyeon/nodloop
cd nodloop
go install ./cmd/nodloop
nodloop run record --producer dev --label repo=nodloop --output README.md --record-dir /tmp/nodloop-dev
nodloop trace list --record-dir /tmp/nodloop-dev
```

The last command lists the run you recorded, which means the binary and a record directory are in place.

## Checks

Every pull request runs these in CI and they must be green.

```
gofmt -l .
go vet ./... ./server/...
go test -race -cover ./... ./server/...
golangci-lint run ./...
cd server && golangci-lint run --config ../.golangci.yml ./...
```

`server/` is a Go module of its own for `nodloop-server`, joined to the root module by `go.work`, so its packages need `./server/...` beside `./...`. A release tags `v<version>` first, then sets `require github.com/jeon-jihyeon/nodloop v<version>` in `server/go.mod` and tags `server/v<version>`, since `go install` of the server resolves that tag without `go.work`. The workflow `server-release` builds and tests the server that way on every `server/v*` tag.

CI runs the Go tests on Linux with the Go version of go.mod and the newest stable one, and on macOS without the PostgreSQL suites. The fuzz tests run their seeds as plain tests. Fuzz a parser you change for a minute and commit any input it finds under `testdata/fuzz`:

```
go test ./internal/veto -run '^$' -fuzz FuzzCommandLines -fuzztime 60s
go test ./internal/jsonl -run '^$' -fuzz FuzzFileNewest -fuzztime 60s
```

`go test ./cmd/nodloop -run '^$' -bench BenchmarkHook` times the conversation hooks over 1,000 and 50,000 runs. A change to what the hooks read shows the numbers before and after.

The SDK jobs run from their directories:

```
cd sdk/python && uvx ruff check . && uvx ruff format --check . && uv run --locked pytest -q
cd sdk/typescript && npm ci && npm run typecheck && npm test
```

The PostgreSQL store suites run when `NODLOOP_TEST_POSTGRES` holds a database URL and are skipped otherwise. CI runs them against a postgres 17 service:

```
NODLOOP_TEST_POSTGRES=postgres://postgres:nodloop@localhost:5432/nodloop?sslmode=disable go test ./server/internal/pg/...
```

Tests that talk to a model are opt in:

```
NODLOOP_LLM_LIVE=1 go test ./internal/llm/... -run Live
```

Use haiku while iterating with `--model haiku` on the commands that call a model, such as `nodloop knowledge compact`, `nodloop knowledge check`, `nodloop knowledge extract` and `nodloop knowledge replay`.

## Layout

| Layer | Packages | Rule |
|---|---|---|
| Domain | `feedback`, `trace`, `llm`, `classify`, `veto`, `settings`, `jsonl`, `atomicfile` | No imports from the layers above |
| Core | `knowledge` | The ledger of items, their scopes and their history. Never a file store |
| Application | `compact`, `extract`, `loop`, `replay` | Build on the core. They never import each other. `compact`, `extract` and `loop` are imported by `mcp`, `cmd/nodloop` and the server, and `replay` by `cmd/nodloop` alone |
| Infra | the `file` subpackages and `otel` | Implements the stores in files, the veto and settings files and the OTLP export. Application code never imports one outside its tests |
| Controllers | `cmd/nodloop`, `mcp`, `guard` | `cmd/nodloop` is the composition root and the only reader of the process environment |
| Library | `nodloop` at the module root | A second composition root that other Go code imports |
| Server | `server/cmd/nodloop-server`, `server/internal/pg` | A module of its own: the HTTP server, its keys and the PostgreSQL stores. It composes the core packages itself and the root module never imports it |
| SDKs | `sdk/python`, `sdk/typescript` | Clients of the MCP tools over a local `nodloop mcp` or a server. They never reach the records directly |
| Test harness | `testkit` | File stores in a temp directory and a fake clock |

depguard enforces the direction. If a change needs an import that the linter rejects, the change is in the wrong package.

## Conventions

- Interfaces are declared by the package that calls them and hold only the methods it uses. Constructors return the concrete type
- Enums are typed strings with a valid set. JSON values are the strings and never change without a note in the pull request
- Errors are package sentinels in `errors.go`, wrapped with `%w`. Callers use `errors.Is`
- Logic lives as a method on the type that owns the data. No free helper over a slice
- The clock is injected as `now func() time.Time`. Domain and application code never call `time.Now`
- Comments explain why and constraints. No trailing period, no comma-joined clauses, no comment that restates the identifier
- Tests are table-driven with testify. Fixtures live under `testdata` and captured model outputs are saved as is. Test doubles come from `go generate` and are never edited by hand

## Pull requests

- One change per pull request. Keep refactors and behavior changes apart
- Commit messages follow `type: what changed` with `feat`, `fix`, `docs`, `refactor`, `test` or `chore`, for example `fix: refuse a label no recorded run carries`
- Say in the description what you ran. A pull request that touches a store format or what a prompt receives shows a trace or the `nodloop report loop` output before and after
- New tools, commands and flags come with a line in the usage text and, when they change the plugin, in the skill under `plugin/skills` that calls them
- A change users notice adds a line under Unreleased in CHANGELOG.md. A change that breaks a promise of the Versioning section in the README waits for a minor release
- Everyone taking part follows the [code of conduct](CODE_OF_CONDUCT.md)

## Security

See SECURITY.md for reporting a vulnerability privately.
