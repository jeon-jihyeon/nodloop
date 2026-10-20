package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/textkey"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// The person's judgment on record that curate reads and the files the person designates as direction
// 1. anchor alone lists the anchor of every place and records its version when it changed
// 2. add and remove save the designated files in config.json only with the terminal proof since the files are the person's direction
func runAnchor(args []string, getenv func(string) string, now func() time.Time, proof trace.Proof, stdout, stderr io.Writer) int {
	action := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, args = args[0], args[1:]
	}
	fs := newFlagSet(strings.TrimSpace("anchor "+action), stderr)
	var records recordFlags
	records.bind(fs)
	path, err := parseID(fs, args)
	if err != nil {
		return parseFailed(err)
	}
	a, err := records.app(getenv, now)
	if err != nil {
		return fail(stderr, "anchor", err)
	}
	cmd := anchorCommand{app: a, proof: proof, out: stdout}
	ctx := context.Background()
	switch action {
	case "":
		err = cmd.list(ctx)
	case "add":
		err = cmd.add(ctx, path)
	case "remove":
		err = cmd.remove(ctx, path)
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, action)
	}
	if err != nil {
		return fail(stderr, "anchor", err)
	}
	return 0
}

type anchorCommand struct {
	app app
	// The terminal proof of the process
	// add and remove change the person's direction only with it
	proof trace.Proof
	out   io.Writer
}

// The anchor as it stands with its newest version
type anchorState struct {
	anchor  curate.Anchor
	version curate.AnchorVersion
	// The designated files that cannot be read
	absent []string
	// The start of the window the sessions and their rules files are read from
	since time.Time
	// When the newest version was recorded and zero before the first
	at time.Time
}

// The anchor of every place the runs of the window carry
// A place lists the lessons a person approved that reach it and the rules files its sessions loaded
func (c anchorCommand) list(ctx context.Context) error {
	state, err := c.refresh(ctx)
	if err != nil {
		return err
	}
	c.head(state)
	store, err := c.app.traces()
	if err != nil {
		return err
	}
	runs, err := store.List(ctx, record.TraceFilter{Name: string(trace.NameRun), Since: state.since})
	if err != nil {
		return err
	}
	for _, p := range state.anchor.Places(trace.RestoreAll(runs)) {
		fmt.Fprintf(c.out, "place\t%s\n", p.Place)
		for _, k := range p.Items {
			fmt.Fprintf(c.out, "lesson\t%s\tv%d\t%s\t%s\n", k.ID, k.Version, k.Kind, k.Content)
		}
		for _, f := range p.Loaded {
			fmt.Fprintf(c.out, "file\t%s\t%s\n", f.Path, cmp.Or(f.MemoryType, "rules"))
		}
	}
	return nil
}

// Saves a file the person designates and records the anchor it changes
// 1. a relative path is taken from the working directory and saved absolute
// 2. a path that names no regular file is refused so a typo never designates nothing
func (c anchorCommand) add(ctx context.Context, path string) error {
	file, err := c.designation("add", path)
	if err != nil {
		return err
	}
	info, err := os.Stat(file)
	if err != nil {
		return fmt.Errorf("add: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("add: %w: %s", errAnchorNoFile, file)
	}
	return c.save(ctx, func(files []string) ([]string, error) {
		if slices.Contains(files, file) {
			return files, nil
		}
		return append(files, file), nil
	})
}

// Takes a file out of the designated ones and records the anchor it changes
// A path that names none of them is refused so a typo never leaves one in place unnoticed
func (c anchorCommand) remove(ctx context.Context, path string) error {
	file, err := c.designation("remove", path)
	if err != nil {
		return err
	}
	return c.save(ctx, func(files []string) ([]string, error) {
		if !slices.Contains(files, file) {
			return nil, fmt.Errorf("remove: %w: %s", errAnchorUnknown, file)
		}
		return slices.DeleteFunc(files, func(f string) bool { return f == file }), nil
	})
}

// The absolute path of a change of the designated files
// Only with the terminal proof since the files are the person's direction
func (c anchorCommand) designation(action, path string) (string, error) {
	switch {
	case c.proof == trace.ProofNone:
		return "", fmt.Errorf("%s: %w", action, errPersonOnly)
	case path == "":
		return "", fmt.Errorf("%s: a path %w", action, errRequired)
	case c.app.cfg.home == "":
		return "", fmt.Errorf("%s: %w", action, errHomeUnknown)
	}
	return filepath.Abs(path)
}

// Writes the designated files change returns under the lock of config.json and then records and prints the anchor
// No file left removes the key
func (c anchorCommand) save(ctx context.Context, change func(files []string) ([]string, error)) error {
	err := c.app.cfg.home.config().Update(func(read func(string, any) error) (map[string]any, error) {
		var saved anchorConfig
		if err := read("anchor", &saved); err != nil {
			return nil, err
		}
		files, err := change(saved.Files)
		if err != nil {
			return nil, err
		}
		c.app.cfg.anchor = files
		if len(files) == 0 {
			return map[string]any{"anchor": nil}, nil
		}
		return map[string]any{"anchor": anchorConfig{Files: files}}, nil
	})
	if err != nil {
		return err
	}
	state, err := c.refresh(ctx)
	if err != nil {
		return err
	}
	c.head(state)
	return nil
}

// The anchor of the records and the files as its newest version
// A version is appended only when a ref or the HMAC of a file differs from the newest one
func (c anchorCommand) refresh(ctx context.Context) (anchorState, error) {
	ledger, err := c.app.ledger()
	if err != nil {
		return anchorState{}, err
	}
	all, err := ledger.All(ctx)
	if err != nil {
		return anchorState{}, err
	}
	store, err := c.app.traces()
	if err != nil {
		return anchorState{}, err
	}
	key, err := textkey.Load(c.app.cfg.recordDir)
	if err != nil {
		return anchorState{}, err
	}
	var state anchorState
	if state.since, err = c.app.window(ctx); err != nil {
		return anchorState{}, err
	}
	loaded, newest, err := c.recorded(ctx, store, state.since)
	if err != nil {
		return anchorState{}, err
	}
	designated, absent := c.designated(key)
	state.anchor, state.absent, state.at = curate.NewAnchor(all, designated, loaded), absent, newest.at
	next, changed := state.anchor.Next(newest.version)
	state.version = next
	if !changed {
		return state, nil
	}
	tr, err := next.Trace(c.app.now())
	if err != nil {
		return anchorState{}, err
	}
	state.at = tr.Time
	return state, store.Append(ctx, tr.Record())
}

// The newest anchor version with the time it was recorded
type recordedAnchor struct {
	version curate.AnchorVersion
	at      time.Time
}

// The rules files sessions loaded since the start of the window and the newest anchor version
func (c anchorCommand) recorded(ctx context.Context, store *tracefile.Store, since time.Time) ([]curate.Loaded, recordedAnchor, error) {
	found, err := store.List(ctx, record.TraceFilter{Name: string(trace.NameInstructions), Since: since})
	if err != nil {
		return nil, recordedAnchor{}, err
	}
	loaded, err := instructionsTraces(trace.RestoreAll(found)).loaded()
	if err != nil {
		return nil, recordedAnchor{}, err
	}
	versions, err := store.List(ctx, record.TraceFilter{Name: string(trace.NameAnchor), Limit: 1})
	if err != nil {
		return nil, recordedAnchor{}, err
	}
	tr := trace.RestoreAll(versions).Newest()
	newest, err := curate.RestoreAnchor(tr)
	return loaded, recordedAnchor{version: newest, at: tr.Time}, err
}

// The designated files that can be read with the HMAC of their content and the paths of those that cannot
// A file that is gone or unreadable counts as absent and its lines decide nothing
func (c anchorCommand) designated(key textkey.Key) ([]curate.AnchorFile, []string) {
	var files []curate.AnchorFile
	var absent []string
	for _, path := range c.app.cfg.anchor {
		content, err := os.ReadFile(path)
		if err != nil {
			absent = append(absent, path)
			continue
		}
		files = append(files, curate.AnchorFile{Path: path, HMAC: key.Sum(string(content))})
	}
	return files, absent
}

// The newest version with what it holds and the files every place reads
func (c anchorCommand) head(state anchorState) {
	if state.version.Version == 0 {
		fmt.Fprintln(c.out, "anchor\tnone\tno lesson you approved and no file you keep yet")
	} else {
		fmt.Fprintf(c.out, "anchor\tv%d\tcause %s\t%d lessons you approved\t%d files\n",
			state.version.Version, state.version.Cause, len(state.version.Refs), len(state.version.Files))
	}
	for _, f := range state.anchor.Designated() {
		fmt.Fprintf(c.out, "designated\t%s\tevery place\n", f.Path)
	}
	for _, path := range state.absent {
		fmt.Fprintf(c.out, "absent\t%s\tcannot be read so it decides nothing\n", path)
	}
}
