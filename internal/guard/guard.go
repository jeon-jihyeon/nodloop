// Package guard is the PreToolUse interlock that blocks tool calls matching a veto
package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// Fields of the PreToolUse hook input used for evaluation
// Other fields vary across Claude Code versions and are ignored
type input struct {
	SessionID string         `json:"session_id"`
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
	Cwd       string         `json:"cwd"`
}

// One line of the decision log
// The tool input never enters it so a secret in a command never lands in the log
type Entry struct {
	Time    time.Time   `json:"time"`
	Session string      `json:"session_id,omitempty"`
	Cwd     string      `json:"cwd"`
	Tool    string      `json:"tool"`
	Veto    string      `json:"veto"`
	Action  veto.Action `json:"action"`
}

// What the hook decided on one call
type Decision struct {
	Exit Exit
	// The entry of the veto that decided
	// Nil when no veto matched so a call that passed or failed writes no log line
	Entry *Entry
}

// The PreToolUse output that hands the call to the person
// Claude Code reads it only on exit 0 and shows the reason to the person
type askOutput struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// Returns the vetoes to apply for the cwd of the hook input
// A non nil error may come with the vetoes that did load and those still apply
type Loader func(cwd string) (veto.Vetoes, error)

// Exit code of the hook process as Claude Code reads it
type Exit int

const (
	ExitPass  Exit = 0 // the normal permission flow decides or the hook output asks the person
	ExitFail  Exit = 1 // hook input or veto load failure without a match
	ExitBlock Exit = 2 // a veto matched and its reason went to stderr
)

// Match hook input against vetoes and decide the exit code
// 1. no matching veto: ExitPass
// 2. matching veto that blocks: ExitBlock with the id and reason on stderr
// 3. matching veto that asks and none that blocks: ExitPass with the ask output on stdout
// 4. input parse failure: ExitFail
// 5. a veto file that was read but is not valid YAML: ExitBlock for every call but one that reads or edits a veto file or the file named on the command line
// 6. any other load failure without a match: ExitFail after a warning on stderr
// 7. the vetoes that did load decide first in every case
// Rule 5 keeps a file broken by one write from turning every veto in it off
// Exit 1 lets the call run so failing open there would do exactly that
// A file no Edit or Write can repair such as one without permission or a directory gets rule 6 so it never locks the session
// Only rules 2 and 3 carry a log entry stamped with now
func Run(stdin io.Reader, stdout, stderr io.Writer, load Loader, named string, now time.Time) Decision {
	var in input
	err := json.NewDecoder(stdin).Decode(&in)
	if err == nil && in.ToolName == "" {
		err = ErrToolNameMissing
	}
	if err != nil {
		fmt.Fprintf(stderr, "nodloop guard: failed to parse hook input: %v\n", err)
		return Decision{Exit: ExitFail}
	}
	vetoes, err := load(in.Cwd)
	if err != nil {
		fmt.Fprintf(stderr, "nodloop guard: failed to load vetoes, skipped: %v\n", err)
	}
	if v := vetoes.Match(in.ToolName, in.ToolInput); v != nil {
		entry := &Entry{Time: now.UTC(), Session: in.SessionID, Cwd: in.Cwd, Tool: in.ToolName, Veto: v.ID(), Action: v.Action()}
		if v.Action() == veto.ActionAsk {
			var out askOutput
			out.HookSpecificOutput.HookEventName, out.HookSpecificOutput.PermissionDecision = "PreToolUse", "ask"
			out.HookSpecificOutput.PermissionDecisionReason = "nodloop veto " + v.ID() + ": " + v.Reason()
			// A struct of strings always encodes
			b, _ := json.Marshal(out)
			fmt.Fprintln(stdout, string(b))
			return Decision{Exit: ExitPass, Entry: entry}
		}
		fmt.Fprintf(stderr, "nodloop guard: %s call blocked by veto %s\n%s\n", in.ToolName, v.ID(), v.Reason())
		return Decision{Exit: ExitBlock, Entry: entry}
	}
	if errors.Is(err, veto.ErrYAMLInvalid) && !in.repairs(named) {
		fmt.Fprintf(stderr, "nodloop guard: %s call blocked because a veto file is not valid YAML\n"+
			"Fix the file named above. Reading and editing a veto file still pass\n", in.ToolName)
		return Decision{Exit: ExitBlock}
	}
	if err != nil {
		return Decision{Exit: ExitFail}
	}
	return Decision{Exit: ExitPass}
}

// Whether the call reads or edits a veto file by its file_path so a broken file can be repaired
// A file named on the command line counts under any name because the lockout covers it too
func (in input) repairs(named string) bool {
	path, _ := in.ToolInput["file_path"].(string)
	return vetofile.Named(path) || (named != "" && filepath.Clean(path) == named)
}
