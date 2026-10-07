// Command nodloop-server serves the nodloop MCP tools over HTTP to the keys of many tenants
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/userconfig"
)

const usage = `usage: nodloop-server <command> [flags]

  key add <name> --tenant <t> --role producer|reviewer|approver
                            Print a new key once and save only its SHA-256 under server in ~/.nodloop/config.json.
                            A producer records runs and verdicts, a reviewer also drafts and proposes knowledge and
                            reads reports, and an approver also approves. Approvals through a key are recorded under its name
  key list                  Keys with their tenant and role
  key remove <name>         Remove a key
  serve [--addr <host:port>] [--postgres <url>] [--record-dir <dir>]
                            Serve the MCP tools over streamable HTTP at /mcp for the keys, each tenant on its own records
                            under tenants of the record directory, or in one PostgreSQL database with --postgres or
                            NODLOOP_POSTGRES. 127.0.0.1:8787 by default. GET /v1/reports/<name> answers loop, extract,
                            critic, effect, replay or health as JSON to a reviewer or approver key and GET /healthz answers without one
  version                   Print the build version

The record directory is --record-dir, then NODLOOP_RECORD_DIR, then record_dir of ~/.nodloop/config.json, then
~/.nodloop/records
`

const (
	envRecordDir = userconfig.EnvRecordDir
	// A PostgreSQL URL serve keeps the records of every tenant in
	envPostgres = "NODLOOP_POSTGRES"
)

// Set by goreleaser through ldflags
// dev in a source build
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, time.Now, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	switch args[0] {
	case "key":
		return runKey(args[1:], getenv, stdout, stderr)
	case "serve":
		return runServe(args[1:], getenv, now, stderr)
	case "version":
		fmt.Fprintln(stdout, buildVersion())
		return 0
	}
	fmt.Fprintf(stderr, "nodloop-server: unknown command %q\n\n%s", args[0], usage)
	return 1
}

// The ldflags version when set and otherwise the module version go install recorded
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

// One line on stderr per failed command and the usage after a usage error
func fail(stderr io.Writer, command string, err error) int {
	fmt.Fprintf(stderr, "nodloop-server %s: %s\n", command, err)
	if errors.Is(err, errUnknownAction) || errors.Is(err, errRequired) {
		fmt.Fprintf(stderr, "\n%s", usage)
	}
	return 1
}

// A flag set that prints the usage on -h and reports a parse error once
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	return fs
}

// flag stops at the first positional argument so a name before the flags is taken out first
func parseName(fs *flag.FlagSet, args []string) (string, error) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], fs.Parse(args[1:])
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	return fs.Arg(0), nil
}

// -h is no failure and any other parse error was already printed by the flag set
func parseFailed(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 1
}
