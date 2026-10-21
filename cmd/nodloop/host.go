package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The agent a hook runs under
// It picks only how a hook reads its input and writes its answer so every host selects the same items
type host string

const (
	hostClaudeCode host = "claude-code" // the plugin hooks of hooks.json
	hostCodex      host = "codex"       // the plugin hooks of codex.json in the shapes of Claude Code
	hostCursor     host = "cursor"      // a hook file of the user whose input names the conversation conversation_id
	hostCopilot    host = "copilot"     // a hook file of the user whose input is in camel case
)

const hostHint = "Use claude-code, codex, cursor or copilot"

// The hooks each host runs
// 1. the prompt hook of Cursor carries no context and Copilot drops the output of its prompt hook
// The session start of both adds the items instead
// 2. only Claude Code reports the rules files it loads
var hostActions = map[host][]string{
	hostClaudeCode: {"start", "prompt", "stop", "subagent", "instructions"},
	hostCodex:      {"start", "prompt", "stop", "subagent"},
	hostCursor:     {"start", "stop"},
	hostCopilot:    {"start", "stop"},
}

// The action and the host of a hook command line
// The host is claude-code without --host so the hooks of earlier plugin versions keep working
func hookArgs(args []string, stderr io.Writer) (string, host, error) {
	if len(args) == 0 {
		return "", "", errNoAction
	}
	fs := newFlagSet("hook "+args[0], stderr)
	name := fs.String("host", string(hostClaudeCode), "the agent the hook runs under: claude-code, codex, cursor or copilot")
	if err := fs.Parse(args[1:]); err != nil {
		return "", "", err
	}
	h := host(*name)
	actions, ok := hostActions[h]
	switch {
	case !ok:
		return "", "", fmt.Errorf("%w %q. %s", errHostUnknown, *name, hostHint)
	case !slices.Contains(actions, args[0]):
		return "", "", fmt.Errorf("%w: %s runs no hook %s", errUnknownAction, h, args[0])
	}
	return args[0], h, nil
}

// The labels of the place with the host
// An item scoped without host reaches every host so the label only splits reports and lets a person narrow a lesson to one
func (h host) labels(place trace.Labels) trace.Labels {
	labels := maps.Clone(place)
	if labels == nil {
		labels = trace.Labels{}
	}
	labels[trace.LabelHost] = []string{string(h)}
	return labels
}

// Whether the input of its stop carries the answer so the stop records a run
func (h host) answers() bool {
	return h == hostClaudeCode || h == hostCodex
}

// Whether the transcript its stop names is the Claude Code JSONL the capture reads
// Codex names a transcript whose format it keeps no stable interface for hooks
func (h host) transcripts() bool {
	return h == hostClaudeCode
}

// Whether a prompt hook adds the items so a session start adds them only when the context lost them
func (h host) prompts() bool {
	return slices.Contains(hostActions[h], "prompt")
}

// The hook input of the host as the fields the hooks read
func (h host) input(r io.Reader) (hookInput, error) {
	switch h {
	case hostCursor:
		return decodeInput[cursorInput](r)
	case hostCopilot:
		return decodeInput[copilotInput](r)
	}
	return decodeInput[hookInput](r)
}

// The input shape of one host
type hostInput interface {
	fields() hookInput
}

func decodeInput[T hostInput](r io.Reader) (hookInput, error) {
	var in T
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return hookInput{}, err
	}
	return in.fields(), nil
}

// Claude Code and Codex send the same fields
func (in hookInput) fields() hookInput {
	return in
}

// The fields of the Cursor hook input the hooks read
type cursorInput struct {
	// Every hook of a conversation carries it while session_id comes with sessionStart alone
	ConversationID string   `json:"conversation_id"`
	WorkspaceRoots []string `json:"workspace_roots"`
	// completed, aborted or error on stop
	Status string `json:"status"`
	// How many followups in a row continued the conversation
	LoopCount int `json:"loop_count"`
}

// 1. the first workspace root is the place since the input has no cwd
// 2. a stop after a followup or after an aborted or failed loop counts as continued as stop_hook_active marks it on Claude Code
func (in cursorInput) fields() hookInput {
	out := hookInput{SessionID: in.ConversationID, StopHookActive: in.LoopCount > 0 || in.Status == "aborted" || in.Status == "error"}
	if len(in.WorkspaceRoots) > 0 {
		out.Cwd = in.WorkspaceRoots[0]
	}
	return out
}

// The fields of the Copilot CLI hook input the hooks read
// stop_hook_active keeps its snake case in the camel case input of agentStop
type copilotInput struct {
	SessionID      string `json:"sessionId"`
	Cwd            string `json:"cwd"`
	Source         string `json:"source"`
	StopHookActive bool   `json:"stop_hook_active"`
}

func (in copilotInput) fields() hookInput {
	return hookInput{SessionID: in.SessionID, Cwd: in.Cwd, Source: in.Source, StopHookActive: in.StopHookActive}
}

// The text as context of the hook event in the answer shape of the host
func (h host) context(w io.Writer, event, text string) error {
	switch h {
	case hostCursor:
		return h.write(w, map[string]string{"additional_context": text})
	case hostCopilot:
		return h.write(w, map[string]string{"additionalContext": text})
	}
	return h.write(w, map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": text}})
}

// Whether its stop shows the person a question or a notice
// 1. code reads a reply only from the transcript the capture reads
// A question elsewhere spends the person's attention with no way to record the answer so it stays due for a Claude Code session or the manual path
// 2. only Claude Code documents the common field systemMessage of a synchronous Stop hook
func (h host) talks() bool {
	return h.transcripts()
}

// The notice as the systemMessage of the stop alone so the turn ends and the model never repeats the answer
func (h host) notice(w io.Writer, line string) error {
	return h.write(w, map[string]string{"systemMessage": line})
}

// Items quote commands such as a && b so the text stays as written
func (host) write(w io.Writer, answer any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(answer)
}
