// Package settings edits only the nodloop hook of a decoded Claude Code settings.json and keeps every other key as is
package settings

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	hookArg   = "guard"
	exePrefix = "nodloop"
	// Matches every tool so a veto on any tool runs
	allTools = "*"
)

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9/._+:@%=,-]+$`)

// Seconds so a hung hook cannot stall the session
// The Claude Code default is 600 seconds
const hookTimeout = 5

// Decoded settings.json
type Document map[string]any

// One nodloop hook registered under PreToolUse
type Hook struct {
	Command string
	// Empty when the group names none
	Matcher string
}

// Unquoted executable of the hook command
func (h Hook) Exe() string {
	exe, _ := command(h.Command).exe()
	return exe
}

// A matcher that leaves tools out never runs the vetoes of those tools
// Claude Code reads an empty matcher as every tool
func (h Hook) CheckMatcher() error {
	if h.Matcher != allTools && h.Matcher != "" {
		return fmt.Errorf("%w: %q", ErrHookNarrow, h.Matcher)
	}
	return nil
}

// Claude Code runs no hook at all while disableAllHooks is true
func (d Document) CheckHooksOn() error {
	if off, _ := d["disableAllHooks"].(bool); off {
		return ErrHooksOff
	}
	return nil
}

// Register the PreToolUse hook of exe as the only nodloop hook
// 1. returns false without changes when exactly that hook is the only one registered
// 2. drops every other nodloop hook first so a stale path or a narrow matcher is replaced and never left beside it
// 3. returns ErrHooksInvalid when hooks is not an object or PreToolUse is not an array so no user value is overwritten
func (d Document) Install(exe string) (changed bool, err error) {
	want := Hook{Command: string(guardCommand(exe)), Matcher: allTools}
	if slices.Equal(d.Hooks(), []Hook{want}) {
		return false, nil
	}
	hooks, ok := d["hooks"].(map[string]any)
	if !ok && d["hooks"] != nil {
		return false, fmt.Errorf("%w: hooks is not an object", ErrHooksInvalid)
	}
	if _, ok := hooks["PreToolUse"].([]any); !ok && hooks["PreToolUse"] != nil {
		return false, fmt.Errorf("%w: PreToolUse is not an array", ErrHooksInvalid)
	}
	d.Uninstall()
	hooks, _ = d["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		d["hooks"] = hooks
	}
	pre, _ := hooks["PreToolUse"].([]any)
	entry := map[string]any{"type": "command", "command": want.Command, "timeout": hookTimeout}
	hooks["PreToolUse"] = append(pre, map[string]any{"matcher": want.Matcher, "hooks": []any{entry}})
	return true, nil
}

// Remove our hook and report whether one was there
// 1. drops matcher groups and the PreToolUse and hooks keys that become empty
// 2. leaves the document as is when our hook is absent
func (d Document) Uninstall() (changed bool) {
	hooks, _ := d["hooks"].(map[string]any)
	pre, _ := hooks["PreToolUse"].([]any)
	var groups []any
	for _, g := range pre {
		group, ok := g.(map[string]any)
		if !ok {
			groups = append(groups, g)
			continue
		}
		entries, _ := group["hooks"].([]any)
		kept := slices.DeleteFunc(entries, ourEntry)
		changed = changed || len(kept) < len(entries)
		if len(kept) == 0 {
			continue
		}
		group["hooks"] = kept
		groups = append(groups, group)
	}
	if !changed {
		return false
	}
	hooks["PreToolUse"] = groups
	if len(groups) == 0 {
		delete(hooks, "PreToolUse")
	}
	if len(hooks) == 0 {
		delete(d, "hooks")
	}
	return true
}

// Every nodloop hook under PreToolUse in file order
func (d Document) Hooks() []Hook {
	hooks, _ := d["hooks"].(map[string]any)
	pre, _ := hooks["PreToolUse"].([]any)
	var found []Hook
	for _, g := range pre {
		group, _ := g.(map[string]any)
		matcher, _ := group["matcher"].(string)
		entries, _ := group["hooks"].([]any)
		for _, e := range entries {
			if ourEntry(e) {
				found = append(found, Hook{Command: string(entryCommand(e)), Matcher: matcher})
			}
		}
	}
	return found
}

func ourEntry(e any) bool {
	_, ok := entryCommand(e).exe()
	return ok
}

// Shell command of a hook entry
type command string

// Command that runs guard through the executable
// The path is single quoted only when the shell would split or expand it
func guardCommand(exe string) command {
	if shellSafe.MatchString(exe) {
		return command(exe + " " + hookArg)
	}
	return command("'" + strings.ReplaceAll(exe, "'", `'\''`) + "' " + hookArg)
}

// A non object entry or an entry without a command yields an empty command
func entryCommand(e any) command {
	entry, _ := e.(map[string]any)
	c, _ := entry["command"].(string)
	return command(c)
}

// The unquoted executable and whether the command is our hook
// 1. the last argument is guard
// 2. the executable basename starts with nodloop and holds no space so nodloop-darwin-arm64 matches too
// 3. a single quoted executable path is unquoted first
// 4. an unquoted path with spaces written by older installs still matches so uninstall can remove it
func (c command) exe() (string, bool) {
	exe, found := strings.CutSuffix(string(c), " "+hookArg)
	if !found {
		return "", false
	}
	if len(exe) >= 2 && exe[0] == '\'' && exe[len(exe)-1] == '\'' {
		exe = strings.ReplaceAll(exe[1:len(exe)-1], `'\''`, "'")
	}
	base := filepath.Base(exe)
	if !strings.HasPrefix(base, exePrefix) || strings.Contains(base, " ") {
		return "", false
	}
	return exe, true
}
