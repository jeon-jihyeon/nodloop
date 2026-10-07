package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/guard"
	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	"github.com/jeon-jihyeon/nodloop/internal/settings"
	settingsfile "github.com/jeon-jihyeon/nodloop/internal/settings/file"
	"github.com/jeon-jihyeon/nodloop/internal/veto"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// Any first argument other than an action runs the hook so a registered hook never depends on an action name
func runGuard(
	args []string, getenv func(string) string, executable func() (string, error), now func() time.Time,
	stdin io.Reader, stdout, stderr io.Writer,
) int {
	cmd := guardCommand{
		home: homeDir(getenv("HOME")), executable: executable, now: now, stdin: stdin, out: stdout, errOut: stderr,
	}
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
	case "call":
		fs := newFlagSet("guard call", stderr)
		tool := fs.String("tool", "", "the tool an agent is about to call such as Bash")
		input := fs.String("input", "{}", "the arguments of the call as JSON. A shell command goes under command")
		dir := fs.String("dir", "", "the directory whose project vetoes apply. The working directory when empty")
		if err := fs.Parse(args[1:]); err != nil {
			return parseFailed(err)
		}
		err = cmd.call(cmp.Or(*dir, currentDir(stderr)), *tool, *input)
	case "log":
		fs := newFlagSet("guard log", stderr)
		limit := fs.Int("limit", decisionLimit, "newest n decisions. 0 prints every one")
		if err := fs.Parse(args[1:]); err != nil {
			return parseFailed(err)
		}
		err = cmd.log(*limit)
	default:
		fs := newFlagSet("guard", stderr)
		vetoesPath := fs.String("vetoes", "", "path to a veto yaml file. Skips discovery")
		if err := fs.Parse(args); err != nil {
			return parseFailed(err)
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
	now        func() time.Time
	stdin      io.Reader
	// The hook writes the output that asks the person here
	out io.Writer
	// The hook writes its block reason here
	errOut io.Writer
}

// The decision log under the nodloop directory of home
const decisionLog = "guard.jsonl"

// Decisions guard log prints without a limit flag
// One screen of recent blocks and asks
const decisionLimit = 20

// Without a path the vetoes are discovered under the hook cwd and home on every call
// A file with a broken entry still blocks through its valid entries
// A named file is compared by its absolute path because Claude Code sends an absolute file_path
func (c guardCommand) hook(vetoesPath string) guard.Exit {
	load, named := guard.Loader(c.discover), ""
	if vetoesPath != "" {
		vetoes, err := vetofile.Load(vetoesPath)
		load = func(string) (veto.Vetoes, error) { return vetoes, err }
		if abs, absErr := filepath.Abs(vetoesPath); absErr == nil {
			named = abs
		}
	}
	d := guard.Run(c.stdin, c.out, c.errOut, load, named, c.now())
	if d.Entry != nil {
		c.record(*d.Entry)
	}
	return d.Exit
}

// Appends the decision of a matched veto to the log
// A failed append warns and never changes the exit because a broken log must neither open nor close a call
// Without a home nothing is written
func (c guardCommand) record(e guard.Entry) {
	if c.home == "" {
		return
	}
	err := os.MkdirAll(c.home.dir(), 0o700)
	if err == nil {
		var log jsonl.File[guard.Entry]
		if log, err = jsonl.Open[guard.Entry](c.home.dir(), decisionLog); err == nil {
			err = log.Append(e)
		}
	}
	if err != nil {
		fmt.Fprintf(c.errOut, "nodloop guard: decision not logged: %v\n", err)
	}
}

// Prints the logged decisions newest first one tab separated line each
// No log yet prints nothing
func (c guardCommand) log(limit int) error {
	if c.home == "" {
		return fmt.Errorf("%w: cannot locate the decision log", errHomeUnknown)
	}
	log, err := jsonl.Open[guard.Entry](c.home.dir(), decisionLog)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := log.Newest(func(guard.Entry) bool { return true }, nil, limit)
	if err != nil {
		return err
	}
	for _, e := range entries {
		fmt.Fprintf(c.out, "%s\t%s\t%s\t%s\t%s\n", e.Time.Format(time.RFC3339), e.Action, e.Veto, e.Tool, e.Cwd)
	}
	return nil
}

func (c guardCommand) discover(cwd string) (veto.Vetoes, error) {
	sources, err := vetofile.Discover(cwd, string(c.home))
	return sources.Vetoes(), err
}

// Loads veto files without evaluating them and says whether the hook enforces them
// 1. every file that loaded is listed even when another file is broken
// 2. a veto whose tool names no tool call carries is named because it blocks nothing
// 3. an approved file of a relative record directory is named on stderr and never changes the exit
// 4. the hook line always comes last and never changes the exit
// 5. any load error fails after the listing
func (c guardCommand) check(cwd string) error {
	sources, err := vetofile.Discover(cwd, string(c.home))
	if len(sources) == 0 && err == nil {
		fmt.Fprintf(c.out, "no veto file found (looked for %s from %s up to its project root and under %s)\n",
			vetofile.RelPath, cwd, c.home)
	}
	for _, s := range sources {
		fmt.Fprintf(c.out, "%s: %d vetoes\n", s.Path, len(s.Vetoes))
		if err := s.Orphan(); err != nil {
			fmt.Fprintf(c.errOut, "nodloop: %v\n", err)
		}
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

// Prints as JSON whether a call an agent outside Claude Code is about to make passes the vetoes the hook would apply in dir
// 1. the vetoes are those the hook discovers: project files, files under home and approved knowledge exported there
// 2. the answer is action allow, block or ask with the veto and its reason
func (c guardCommand) call(dir, tool, input string) error {
	if tool == "" {
		return fmt.Errorf("call: --tool %w", errRequired)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Errorf("%w: --input: %w", errCallInput, err)
	}
	vetoes, err := c.discover(dir)
	if err != nil {
		return err
	}
	answer := map[string]any{"action": "allow"}
	if matched := vetoes.Match(tool, args); matched != nil {
		answer = map[string]any{"action": matched.Action(), "veto": matched.ID(), "reason": matched.Reason()}
	}
	enc := json.NewEncoder(c.out)
	enc.SetEscapeHTML(false)
	return enc.Encode(answer)
}
