package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jeon-jihyeon/nodloop/internal/guard"
	settingsfile "github.com/jeon-jihyeon/nodloop/internal/settings/file"
	"github.com/jeon-jihyeon/nodloop/internal/veto"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// Any first argument other than an action runs the hook so a registered hook never depends on an action name
func runGuard(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := guardCommand{home: homeDir(getenv("HOME")), stdin: stdin, out: stdout, errOut: stderr}
	var action string
	if len(args) > 0 {
		action = args[0]
	}
	var err error
	switch action {
	case "check":
		err = cmd.check(currentDir(stderr))
	case "install":
		err = cmd.install()
	case "uninstall":
		err = cmd.uninstall()
	default:
		fs := flag.NewFlagSet("guard", flag.ContinueOnError)
		fs.SetOutput(stderr)
		vetoesPath := fs.String("vetoes", "", "path to a veto yaml file. Skips discovery")
		if err := fs.Parse(args); err != nil {
			return 1
		}
		exit, err := cmd.hook(*vetoesPath)
		if err != nil {
			return fail(stderr, "guard", err)
		}
		return int(exit)
	}
	if err != nil {
		return fail(stderr, "guard "+action, err)
	}
	return 0
}

func currentDir(stderr io.Writer) string {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "nodloop: working directory unknown, project vetoes skipped: %v\n", err)
	}
	return cwd
}

// The PreToolUse hook and the actions that set it up
// The hook command install registers is the absolute path of the current executable
// Hooks do not run in a login shell so PATH cannot be trusted
type guardCommand struct {
	home  homeDir
	stdin io.Reader
	out   io.Writer
	// The hook writes its block reason here
	errOut io.Writer
}

// Without a path the vetoes are discovered under the hook cwd and home on every call
func (c guardCommand) hook(vetoesPath string) (guard.Exit, error) {
	if vetoesPath == "" {
		return guard.Run(c.stdin, c.errOut, c.discover), nil
	}
	vetoes, err := vetofile.Load(vetoesPath)
	if err != nil {
		return guard.ExitFail, err
	}
	return guard.Run(c.stdin, c.errOut, func(string) (veto.Vetoes, error) { return vetoes, nil }), nil
}

func (c guardCommand) discover(cwd string) (veto.Vetoes, error) {
	sources, err := vetofile.Discover(cwd, string(c.home))
	return sources.Vetoes(), err
}

// Loads veto files without evaluating them
// 1. every file that loaded is listed even when another file is broken
// 2. any load error fails after the listing
func (c guardCommand) check(cwd string) error {
	sources, err := vetofile.Discover(cwd, string(c.home))
	if len(sources) == 0 && err == nil {
		fmt.Fprintf(c.out, "no veto file found (looked for %s under %s and %s)\n", vetofile.RelPath, cwd, c.home)
		return nil
	}
	for _, s := range sources {
		fmt.Fprintf(c.out, "%s: %d vetoes\n", s.Path, len(s.Vetoes))
	}
	fmt.Fprintf(c.out, "merged: %d vetoes\n", len(sources.Vetoes()))
	return err
}

func (c guardCommand) install() error {
	path, err := c.settingsPath()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot resolve executable: %w", err)
	}
	changed, err := settingsfile.Install(path, exe)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(c.out, "already installed in %s\n", path)
		return nil
	}
	fmt.Fprintf(c.out, "installed PreToolUse hook for %s in %s\n", exe, path)
	return nil
}

func (c guardCommand) uninstall() error {
	path, err := c.settingsPath()
	if err != nil {
		return err
	}
	changed, err := settingsfile.Uninstall(path)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(c.out, "not installed in %s\n", path)
		return nil
	}
	fmt.Fprintf(c.out, "removed PreToolUse hook from %s\n", path)
	return nil
}

// Claude Code settings.json under home
func (c guardCommand) settingsPath() (string, error) {
	if c.home == "" {
		return "", fmt.Errorf("%w: cannot locate settings.json", errHomeUnknown)
	}
	return filepath.Join(string(c.home), ".claude", "settings.json"), nil
}
