package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/retain"
)

// The server starts whatever its config so the conversation can tell the user what to fix
// Every call opens the config of that moment so a change of record dir needs no reconnect
func runMCP(
	args []string, getenv func(string) string, start starter, now func() time.Time,
	stdin io.Reader, stdout io.WriteCloser, stderr io.Writer,
) int {
	fs := newFlagSet("mcp", stderr)
	var records recordFlags
	records.bind(fs)
	list := fs.Bool("list", false, "print the tool names and exit")
	agent := fs.Bool("agent", false,
		"serve the agent server the plugin starts: no tool that decides for a person, and every verdict and outcome recorded as the session's")
	if err := fs.Parse(args); err != nil {
		return parseFailed(err)
	}
	cmd := mcpCommand{stdin: stdin, out: stdout, log: stderr}
	o := mcpOpen{flags: records, getenv: getenv, now: now, session: mcp.NewSession(now()), caller: mcp.Caller{Agent: *agent}, log: stderr, export: start}
	instructions := pluginVersion(getenv(envPluginVersion)).mismatch(buildVersion(), executable())
	if instructions != "" {
		instructions += ". Tell the user this before the first answer"
	}
	host := mcp.NewHost(o.open, buildVersion(), instructions)
	tools, serve := mcp.Tools(), host.ServeTransport
	if *agent {
		tools, serve = mcp.AgentTools(), host.ServeAgent
	}
	if *list {
		cmd.list(tools)
		return 0
	}
	if err := serve(context.Background(), cmd.transport()); err != nil {
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

func (c mcpCommand) list(tools []string) {
	fmt.Fprintln(c.out, strings.Join(tools, "\n"))
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
	// Over stdio no key names a person
	// The agent server records every verdict as the session's
	caller mcp.Caller
	// Where a failed prune after a run is written since stdout is the protocol stream
	log io.Writer
	// Starts the OTLP export after every record of the server when config.json holds an otel target at that call
	export starter
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
	source, err := a.source()
	if err != nil {
		return nil, err
	}
	// The record directory exists once the stores above opened
	dir := a.cfg.recordDir
	policy := mcp.Policy{
		Holdout: a.cfg.holdout, Retention: a.cfg.retention, Marks: retain.NewMarker(dir), AfterRun: a.afterRun(o.log),
		Approver: a.cfg.approver, Curate: curate.NewMarker(dir), Build: buildVersion(),
	}
	if o.export != nil && a.cfg.otel != nil {
		policy.AfterRun = a.afterRunExport(o.export, o.log)
		policy.AfterVerdict = a.afterVerdict(o.export, o.log)
	}
	return mcp.New(
		traces, verdicts, outcomes, ledger, source, compact.New(ledger, traces, verdicts, traces, o.now), extract.New(ledger, traces, verdicts, o.now),
		o.now, o.session, executable(), a.cfg.recordArgs(), o.caller, policy,
	), nil
}
