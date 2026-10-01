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

	"github.com/jeon-jihyeon/nodloop/internal/mcp"
)

// A server whose config or data cannot be opened still starts so the conversation can tell the user what to fix
// Every tool of that server answers the error because a client never shows the model what a server printed on stderr
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
	ctx := context.Background()
	a, err := data.app(getenv, now)
	var s *mcp.Server
	if err == nil {
		s, err = a.server()
	}
	if err != nil {
		err = cmd.serveUnconfigured(ctx, err)
	} else {
		err = s.ServeTransport(ctx, cmd.transport())
	}
	if err != nil {
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

// The cause comes first and then the way out
// Only a missing data dir is fixed by setup alone so any other cause asks to fix what it names first
func (c mcpCommand) serveUnconfigured(ctx context.Context, cause error) error {
	reason := fmt.Errorf("%w. Run %s setup --data-dir <dir> and reconnect the nodloop server", cause, executable())
	if !errors.Is(cause, errDataDirUnset) {
		reason = fmt.Errorf("%w. Fix what this names or run %s setup --data-dir <dir> again and reconnect the nodloop server",
			cause, executable())
	}
	fmt.Fprintf(c.log, "nodloop mcp: %v\n", reason)
	return mcp.NewUnconfigured(reason, buildVersion()).ServeTransport(ctx, c.transport())
}
