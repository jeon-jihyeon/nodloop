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

// On a first run with no config at all the demo set becomes the data and the records keep their default
// The config is resolved again after the demo config is written since that config is its input
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
	h := homeDir(getenv("HOME"))
	a, err := data.app(getenv, now)
	if errors.Is(err, errDataDirUnset) && !h.configured() {
		if err := cmd.setupDemo(h); err != nil {
			return fail(stderr, "mcp", err)
		}
		a, err = data.app(getenv, now)
	}
	if err != nil {
		return fail(stderr, "mcp", err)
	}
	if err := cmd.serve(context.Background(), a); err != nil {
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

// The demo is unpacked only when no config exists so a broken config is reported and never overwritten
// The message names this binary for the user to paste
// nodloop stands in when the OS cannot tell its path
func (c mcpCommand) setupDemo(h homeDir) error {
	if h == "" {
		return fmt.Errorf("%w: cannot set up the demo", errHomeUnknown)
	}
	uc, err := h.setupDemo("")
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		exe = "nodloop"
	}
	fmt.Fprintf(c.log, "nodloop mcp: no data configured. Unpacked the demo set to %s and wrote %s. "+
		"Run %s setup --data-dir <dir> for your own data\n", uc.DataDir, h.configPath(), exe)
	return nil
}

func (c mcpCommand) serve(ctx context.Context, a app) error {
	s, err := a.server()
	if err != nil {
		return err
	}
	return s.ServeTransport(ctx, &sdk.IOTransport{Reader: io.NopCloser(c.stdin), Writer: c.out})
}
