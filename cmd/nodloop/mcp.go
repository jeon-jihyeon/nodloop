package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
)

// The server starts whatever its config so the conversation can tell the user what to fix
// Every call opens the config of that moment so a setup run through the CLI needs no reconnect
func runMCP(
	args []string, getenv func(string) string, now func() time.Time,
	stdin io.Reader, stdout io.WriteCloser, stderr io.Writer,
) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var data dataFlags
	data.bind(fs)
	list := fs.Bool("list", false, "print the tool names and exit")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	cmd := mcpCommand{stdin: stdin, out: stdout, log: stderr}
	if *list {
		cmd.list()
		return 0
	}
	open := mcpOpen{flags: data, getenv: getenv, now: now, session: mcp.NewSession(now())}.open
	instructions := pluginVersion(getenv(envPluginVersion)).mismatch(buildVersion(), executable())
	if instructions != "" {
		instructions += ". Tell the user this before the first review"
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
	flags  dataFlags
	getenv func(string) string
	now    func() time.Time
	// Made once at start so a change of record dir between calls keeps one session
	session diagnose.Session
}

// The cause comes first and then the way out
// 1. without a data dir the server works on the records alone and every data tool answers how to set one up
// 2. any other cause asks to fix what it names first
// 3. the next call reads the saved config so no reconnect is ever asked
func (o mcpOpen) open(context.Context) (*mcp.Server, error) {
	a, err := o.flags.app(o.getenv, o.now)
	if errors.Is(err, errDataDirUnset) {
		return o.records(fmt.Errorf("%w. Run %s setup --data-dir <dir>", err, executable()))
	}
	if err == nil {
		var s *mcp.Server
		if s, err = a.server(o.session); err == nil {
			return s, nil
		}
	}
	return nil, fmt.Errorf("%w. Fix what this names or run %s setup --data-dir <dir> again", err, executable())
}

// The server of runs, nods and knowledge over the record directory
func (o mcpOpen) records(missing error) (*mcp.Server, error) {
	a, err := o.flags.records(o.getenv, o.now)
	if err != nil {
		return nil, err
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
	return mcp.NewRecords(traces, verdicts, outcomes, ledger, o.now, o.session, executable(), a.cfg.recordArgs(), missing), nil
}
