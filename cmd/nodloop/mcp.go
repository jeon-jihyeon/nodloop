package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
)

// The server starts whatever its config so the conversation can tell the user what to fix
// Every call opens the config of that moment so a change of record dir needs no reconnect
func runMCP(
	args []string, getenv func(string) string, now func() time.Time,
	stdin io.Reader, stdout io.WriteCloser, stderr io.Writer,
) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var records recordFlags
	records.bind(fs)
	list := fs.Bool("list", false, "print the tool names and exit")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	cmd := mcpCommand{stdin: stdin, out: stdout, log: stderr}
	if *list {
		cmd.list()
		return 0
	}
	open := mcpOpen{flags: records, getenv: getenv, now: now, session: mcp.NewSession(now())}.open
	instructions := pluginVersion(getenv(envPluginVersion)).mismatch(buildVersion(), executable())
	if instructions != "" {
		instructions += ". Tell the user this before the first answer"
	}
	if err := mcp.NewHost(open, buildVersion(), instructions).ServeTransport(context.Background(), cmd.transport()); err != nil {
		return fail(stderr, "mcp", err)
	}
	return 0
}

// stdout is the protocol stream so every message goes to log
type mcpCommand struct {
	stdin io.Reader
	out   io.WriteCloser
	log   io.Writer
}

func (c mcpCommand) list() {
	fmt.Fprintln(c.out, strings.Join(mcp.Tools(), "\n"))
}

// The path of this binary as a command in a message names it for the user to paste into a shell
// nodloop stands in when the OS cannot tell its path
func executable() string {
	exe, err := os.Executable()
	if err != nil {
		return "nodloop"
	}
	return shellWord(exe)
}

// A path with any character outside the safe set in single quotes so a shell reads it as one word
// The safe set is listed rather than the metacharacters so no separator or glob or redirection is ever missed
func shellWord(path string) string {
	if path != "" && strings.Trim(path, shellSafe) == "" {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

const shellSafe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"

func (c mcpCommand) transport() sdk.Transport {
	return &sdk.IOTransport{Reader: io.NopCloser(c.stdin), Writer: c.out}
}

// What every tool call of one server process opens
// The flags and variables of the process still win over the saved config
type mcpOpen struct {
	flags  recordFlags
	getenv func(string) string
	now    func() time.Time
	// Made once at start so a change of record dir between calls keeps one session
	session string
}

// The server over the record directory of that moment
// A config that cannot be read is named with the way out
func (o mcpOpen) open(context.Context) (*mcp.Server, error) {
	a, err := o.flags.app(o.getenv, o.now)
	if err != nil {
		return nil, fmt.Errorf("%w. Fix what this names, or set --record-dir or %s", err, envRecordDir)
	}
	traces, err := a.traces()
	if err != nil {
		return nil, err
	}
	verdicts, err := a.feedback()
	if err != nil {
		return nil, err
	}
	outcomes, err := a.outcomes()
	if err != nil {
		return nil, err
	}
	ledger, err := a.ledger()
	if err != nil {
		return nil, err
	}
	compactor, err := a.compactor(ledger)
	if err != nil {
		return nil, err
	}
	return mcp.New(traces, verdicts, outcomes, ledger, compactor, extract.New(ledger, traces, verdicts), o.now, o.session, executable(), a.cfg.recordArgs()), nil
}
