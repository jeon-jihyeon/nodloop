# Contributing

Issues and pull requests are welcome. Open an issue first for anything larger than a fix so the shape can be agreed before the code exists.

## Setup

Go 1.25 or newer, and the `claude` CLI on PATH for the commands that call a model.

```
git clone https://github.com/jeon-jihyeon/nodloop
cd nodloop
go install ./cmd/nodloop
nodloop setup --data-dir examples/demo
nodloop evidence events
```

The last command lists the 24 demo events of `examples/demo`, which means the binary and the demo data are in place. `go run ./internal/demo` writes its events, contexts, labels, policy and knowledge files again.

## Checks

Every pull request runs these in CI and they must be green.

```
gofmt -l .
go vet ./...
go test ./... -cover
golangci-lint run ./...
```

Tests that talk to a model are opt in:

```
NODLOOP_LLM_LIVE=1 go test ./internal/llm/... -run Live
nodloop diagnose --event tq-001 --model haiku
```

Use haiku while iterating. The numbers in README.md come from `nodloop eval` on sonnet only, so change them only with a fresh run and the report that produced them.

## Layout

| Layer | Packages | Rule |
|---|---|---|
| Domain | `evidence`, `feedback`, `trace`, `llm`, `veto`, `settings`, `jsonl` | No imports from the layers above |
| Core | `analysis`, `knowledge`, `diagnose` | Imports domain and the core below it. Never a file store |
| Application | `eval`, `compact`, `loop` | Build on the core. `compact` and `loop` never import `eval`, and only `eval`, `mcp` and `cmd/nodloop` import `loop` |
| Infra | the `file` subpackages | Implements the stores and the veto and settings files. Application code never imports one outside its tests |
| Controllers | `cmd/nodloop`, `mcp`, `guard` | `cmd/nodloop` is the composition root and the only reader of the process environment |
| Test harness | `testkit` | File stores in a temp directory, the demo source and a fake clock |

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
- Commit messages follow `type: what changed` with `feat`, `fix`, `docs`, `refactor`, `test` or `chore`, for example `fix: send back a cause without a listed paragraph before the gate holds it`
- Say in the description what you ran. A pull request that touches a review prompt or a store format links the trace or the eval report that shows the effect
- New tools, commands and flags come with a line in the usage text and, when they change the plugin, in the skill template in `internal/diagnose/gen/main.go` followed by `go generate ./...`

## Security

See SECURITY.md for reporting a vulnerability privately.
