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
)

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9/._+:@%=,-]+$`)

// Seconds so a hung hook cannot stall the session
// The Claude Code default is 600 seconds
const hookTimeout = 5

// Decoded settings.json
type Document map[string]any

// Register the PreToolUse hook
// 1. returns false without changes when already registered
// 2. returns ErrHooksInvalid when hooks is not an object or PreToolUse is not an array so no user value is overwritten
func (d Document) Install(exe string) (changed bool, err error) {
	if d.installed() {
		return false, nil
	}
	hooks, ok := d["hooks"].(map[string]any)
	if !ok && d["hooks"] != nil {
		return false, fmt.Errorf("%w: hooks is not an object", ErrHooksInvalid)
	}
	if hooks == nil {
		hooks = map[string]any{}
		d["hooks"] = hooks
	}
	pre, ok := hooks["PreToolUse"].([]any)
	if !ok && hooks["PreToolUse"] != nil {
		return false, fmt.Errorf("%w: PreToolUse is not an array", ErrHooksInvalid)
	}
	entry := map[string]any{"type": "command", "command": string(guardCommand(exe)), "timeout": hookTimeout}
	hooks["PreToolUse"] = append(pre, map[string]any{"matcher": "*", "hooks": []any{entry}})
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

func (d Document) installed() bool {
	hooks, _ := d["hooks"].(map[string]any)
	pre, _ := hooks["PreToolUse"].([]any)
	return slices.ContainsFunc(pre, ourGroup)
}

func ourGroup(g any) bool {
	group, _ := g.(map[string]any)
	entries, _ := group["hooks"].([]any)
	return slices.ContainsFunc(entries, ourEntry)
}

func ourEntry(e any) bool {
	return entryCommand(e).ours()
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

// How our hook is recognized
// 1. the last argument is guard
// 2. the executable basename starts with nodloop and holds no space so nodloop-darwin-arm64 matches too
// 3. a single quoted executable path is unquoted first
// 4. an unquoted path with spaces written by older installs still matches so uninstall can remove it
func (c command) ours() bool {
	exe, found := strings.CutSuffix(string(c), " "+hookArg)
	if !found {
		return false
	}
	if len(exe) >= 2 && exe[0] == '\'' && exe[len(exe)-1] == '\'' {
		exe = strings.ReplaceAll(exe[1:len(exe)-1], `'\''`, "'")
	}
	base := filepath.Base(exe)
	return strings.HasPrefix(base, exePrefix) && !strings.Contains(base, " ")
}
