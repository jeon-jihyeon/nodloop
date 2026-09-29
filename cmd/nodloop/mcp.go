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

// Without a data directory the server still starts so the conversation can tell the user how to set one up
// Every tool of that server answers the setup error while any other config error fails the start
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
	switch {
	case errors.Is(err, errDataDirUnset):
		err = cmd.serveUnconfigured(ctx, err)
	case err == nil:
		err = cmd.serve(ctx, a)
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

// A path with a space or a quote in single quotes so a shell reads it as one word
func shellWord(path string) string {
	if !strings.ContainsAny(path, " \t'\"$`") {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

func (c mcpCommand) serveUnconfigured(ctx context.Context, unset error) error {
	reason := fmt.Errorf("%w. Run %s setup --data-dir <dir> and reconnect the nodloop server", unset, executable())
	fmt.Fprintf(c.log, "nodloop mcp: %v\n", reason)
	s := mcp.NewUnconfigured(reason, buildVersion())
	return s.ServeTransport(ctx, &sdk.IOTransport{Reader: io.NopCloser(c.stdin), Writer: c.out})
}

func (c mcpCommand) serve(ctx context.Context, a app) error {
	s, err := a.server()
	if err != nil {
		return err
	}
	return s.ServeTransport(ctx, &sdk.IOTransport{Reader: io.NopCloser(c.stdin), Writer: c.out})
}
