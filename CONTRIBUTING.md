# Contributing

Issues and pull requests are welcome. Open an issue first for anything larger than a fix so the shape can be agreed before the code exists.

## Setup

Go 1.25 or newer, and the `claude` CLI on PATH for the commands that call a model.

```
git clone https://github.com/jeon-jihyeon/nodloop
cd nodloop
go install ./cmd/nodloop
nodloop setup --demo
nodloop evidence events
```

The last command lists the 24 demo events, which means the binary, the demo data and the record directory are all in place.

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
| Application | `analysis`, `knowledge`, `diagnose`, `eval` | Imports domain only |
| Infra | the `file` subpackages over `jsonl.File[T]` | Implements the stores. Application code never imports one outside its tests |
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
- Commit messages follow the Go style `package: what changed`, for example `knowledge: sort dims in Scope.String`
- Say in the description what you ran. A pull request that touches a review prompt or a store format links the trace or the eval report that shows the effect
- New tools, commands and flags come with a line in the usage text and, when they change the plugin, in the skill template in `internal/diagnose/gen/main.go` followed by `go generate ./...`

## Security

See SECURITY.md for reporting a vulnerability privately.
