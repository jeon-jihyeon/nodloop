## What changed

## Why

## Checks

- [ ] `gofmt -l .`, `go vet ./... ./server/...`, `go test ./... ./server/...` and `golangci-lint run ./...` in the root and in `server/` are green
- [ ] JSON values and record formats are unchanged, or the change is described above
- [ ] A change to a store format or to what a prompt receives shows a trace or the `nodloop report loop` output before and after
