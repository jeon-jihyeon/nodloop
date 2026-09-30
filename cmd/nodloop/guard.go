package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/guard"
	"github.com/jeon-jihyeon/nodloop/internal/settings"
	settingsfile "github.com/jeon-jihyeon/nodloop/internal/settings/file"
	"github.com/jeon-jihyeon/nodloop/internal/veto"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// Any first argument other than an action runs the hook so a registered hook never depends on an action name
func runGuard(
	args []string, getenv func(string) string, executable func() (string, error), stdin io.Reader, stdout, stderr io.Writer,
) int {
	cmd := guardCommand{home: homeDir(getenv("HOME")), executable: executable, stdin: stdin, out: stdout, errOut: stderr}
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
		return int(cmd.hook(*vetoesPath))
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
// The hook command install registers is an absolute path
// Hooks do not run in a login shell so PATH cannot be trusted
type guardCommand struct {
	home homeDir
	// The running binary as the process resolves it
	executable func() (string, error)
	stdin      io.Reader
	out        io.Writer
	// The hook writes its block reason here
	errOut io.Writer
}

// Without a path the vetoes are discovered under the hook cwd and home on every call
// A file with a broken entry still blocks through its valid entries
func (c guardCommand) hook(vetoesPath string) guard.Exit {
	if vetoesPath == "" {
		return guard.Run(c.stdin, c.errOut, c.discover)
	}
	vetoes, err := vetofile.Load(vetoesPath)
	return guard.Run(c.stdin, c.errOut, func(string) (veto.Vetoes, error) { return vetoes, err })
}

func (c guardCommand) discover(cwd string) (veto.Vetoes, error) {
	sources, err := vetofile.Discover(cwd, string(c.home))
	return sources.Vetoes(), err
}

// Loads veto files without evaluating them and says whether the hook enforces them
// 1. every file that loaded is listed even when another file is broken
// 2. a veto whose tool names no tool call carries is named because it blocks nothing
// 3. the hook line always comes last and never changes the exit
// 4. any load error fails after the listing
func (c guardCommand) check(cwd string) error {
	sources, err := vetofile.Discover(cwd, string(c.home))
	if len(sources) == 0 && err == nil {
		fmt.Fprintf(c.out, "no veto file found (looked for %s from %s up to its project root and under %s)\n",
			vetofile.RelPath, cwd, c.home)
	}
	for _, s := range sources {
		fmt.Fprintf(c.out, "%s: %d vetoes\n", s.Path, len(s.Vetoes))
		for _, v := range s.Vetoes {
			if unknown := v.UnknownTools(); len(unknown) > 0 {
				fmt.Fprintf(c.out, "%s: veto %s blocks nothing on %q: %v\n", s.Path, v.ID(), unknown, veto.ErrToolUnknown)
			}
		}
	}
	if len(sources) > 0 || err != nil {
		fmt.Fprintf(c.out, "merged: %d vetoes\n", len(sources.Vetoes()))
	}
	hook := "guard hook unknown: " + errHomeUnknown.Error()
	if c.home != "" {
		hook = hookState(c.home.settingsPath(), c.home.stableBinary())
	}
	fmt.Fprintln(c.out, hook)
	return err
}

// Whether guard enforces the vetoes as one line
func hookState(settingsPath, stable string) string {
	installed, err := settingsfile.Installed(settingsPath, stable)
	switch {
	case errors.Is(err, settings.ErrHooksOff):
		return "guard hook off: " + settings.ErrHooksOff.Error() + " in " + settingsPath + " so no hook runs. Remove it to enforce them"
	case errors.Is(err, settingsfile.ErrHookMissing) || errors.Is(err, settings.ErrHookNarrow):
		return "guard hook broken: " + strings.ReplaceAll(err.Error(), "\n", "; ") + ". Run nodloop guard install to repair it"
	case errors.Is(err, settingsfile.ErrHookStale):
		return "guard hook stale: " + strings.ReplaceAll(err.Error(), "\n", "; ") + ". Run " + stable + " guard install so the hook follows the plugin"
	case err != nil:
		return "guard hook unknown: " + err.Error()
	case !installed:
		return "guard hook not installed. Run nodloop guard install to enforce them"
	}
	return "guard hook installed"
}

// The binary the hook runs
// 1. the stable link under `~/.nodloop/bin` when it is the running binary so the hook follows the plugin through upgrades
// 2. a go run build is refused because go deletes it on exit
func (c guardCommand) hookExecutable() (string, error) {
	exe, err := c.executable()
	if err != nil {
		return "", fmt.Errorf("cannot resolve executable: %w", err)
	}
	stable := c.home.stableBinary()
	if running, err := os.Stat(exe); err == nil {
		if linked, err := os.Stat(stable); err == nil && os.SameFile(running, linked) {
			return stable, nil
		}
	}
	if slices.ContainsFunc(strings.Split(filepath.ToSlash(exe), "/"), goBuildDir) {
		return "", fmt.Errorf("%w: %s", errExecutableTemporary, exe)
	}
	return exe, nil
}

// Where go build and go run place their binaries
func goBuildDir(element string) bool {
	return strings.HasPrefix(element, "go-build")
}

func (c guardCommand) install() error {
	path, err := c.settingsPath()
	if err != nil {
		return err
	}
	exe, err := c.hookExecutable()
	if err != nil {
		return err
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
	return c.home.settingsPath(), nil
}
