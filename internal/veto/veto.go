// Package veto defines the interlock conditions that guard enforces
package veto

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Interlock condition that blocks one tool call
// 1. born from one correction and accumulates per correction
// 2. enforced regardless of what Claude decides
// 3. built through New only so the tool list and the conditions are always compiled
type Veto struct {
	id      string
	tools   []string
	when    []Condition
	reason  string
	action  Action
	enabled bool
}

// What the guard does with a call the veto matches
type Action string

const (
	ActionBlock Action = "block" // the call never runs and Claude reads the reason
	ActionAsk   Action = "ask"   // Claude Code asks the person and shows the reason
)

// Validate the required fields and split the tool list
// 1. tool is one exact `tool_name` or a list such as `Edit|Write`
// 2. an empty action blocks
func New(id, tool string, when []Condition, reason string, action Action, enabled bool) (Veto, error) {
	if id == "" {
		return Veto{}, ErrIDMissing
	}
	switch action {
	case "":
		action = ActionBlock
	case ActionBlock, ActionAsk:
	default:
		return Veto{}, fmt.Errorf("%w: %q", ErrActionUnknown, action)
	}
	var tools []string
	for t := range strings.SplitSeq(tool, "|") {
		if t = strings.TrimSpace(t); t != "" {
			tools = append(tools, t)
		}
	}
	if len(tools) == 0 {
		return Veto{}, ErrToolMissing
	}
	if len(when) == 0 {
		return Veto{}, ErrWhenMissing
	}
	if reason == "" {
		return Veto{}, ErrReasonMissing
	}
	return Veto{id: id, tools: tools, when: when, reason: reason, action: action, enabled: enabled}, nil
}

// Shape of a PreToolUse `tool_name`
// 1. a built in tool such as Bash or NotebookEdit
// 2. an MCP tool as mcp__server__tool
// A shape and not a list so a tool Claude Code adds later is still accepted
var toolName = regexp.MustCompile(`^([A-Z][A-Za-z0-9]*|mcp__[A-Za-z0-9_-]+__[A-Za-z0-9_.-]+)$`)

// Shape of a tool name any agent may give its tools such as bash or search_docs
// A permission rule such as `Bash(sed:*)` or a name with spaces is no tool name anywhere
var agentToolName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]*$`)

// Tool names no agent calls a tool by such as a permission rule like `Bash(sed:*)`
func (v Veto) MalformedTools() []string {
	var malformed []string
	for _, t := range v.tools {
		if !agentToolName.MatchString(t) {
			malformed = append(malformed, t)
		}
	}
	return malformed
}

// Tool names no Claude Code tool call carries such as bash or a permission rule like `Bash(sed:*)`
// Matching is exact so in Claude Code a veto that lists only these blocks nothing while another agent may call such a tool
func (v Veto) UnknownTools() []string {
	var unknown []string
	for _, t := range v.tools {
		if !toolName.MatchString(t) {
			unknown = append(unknown, t)
		}
	}
	return unknown
}

// Unique kebab case identifier used for merging and stderr output
func (v Veto) ID() string { return v.id }

// Written to stderr on block so Claude sees it
func (v Veto) Reason() string { return v.reason }

func (v Veto) Action() Action { return v.action }

// Every condition must match and a disabled veto never matches
func (v Veto) Matches(tool string, input map[string]any) bool {
	return slices.Contains(v.tools, tool) && v.Blocks(input)
}

// The conditions never depend on the tool so an input is blocked for every tool the veto lists
// A condition on commands reads the simple commands derived from command
func (v Veto) Blocks(input map[string]any) bool {
	if !v.enabled {
		return false
	}
	input = Input(input).withCommands()
	for _, c := range v.when {
		if !c.matches(input[c.field]) {
			return false
		}
	}
	return true
}

// Vetoes in the order they apply
type Vetoes []Veto

func (vs Vetoes) Has(id string) bool {
	return slices.ContainsFunc(vs, func(v Veto) bool { return v.id == id })
}

// The veto that decides the call and nil when none matches
// 1. the first matching veto that blocks
// 2. else the first matching veto that asks
// So a block never loses to an ask that comes earlier in the merge order
// commands is derived once here so every veto reads the same parse
func (vs Vetoes) Match(tool string, input map[string]any) *Veto {
	input = Input(input).withCommands()
	var asks *Veto
	for i := range vs {
		if !vs[i].Matches(tool, input) {
			continue
		}
		if vs[i].action == ActionBlock {
			return &vs[i]
		}
		if asks == nil {
			asks = &vs[i]
		}
	}
	return asks
}

// Regexp condition on one `tool_input` field
type Condition struct {
	field  string
	match  *regexp.Regexp
	unless *regexp.Regexp
}

// Compile the RE2 patterns once at load
// 1. field is the key inside `tool_input` and a non string value never matches
// 2. match is a partial match
// 3. a match of unless turns the condition into a non match and an empty unless never fires
func NewCondition(field, match, unless string) (Condition, error) {
	if field == "" {
		return Condition{}, ErrFieldMissing
	}
	if match == "" {
		return Condition{}, ErrMatchMissing
	}
	c := Condition{field: field}
	var err error
	if c.match, err = regexp.Compile(match); err != nil {
		return Condition{}, fmt.Errorf("%w: %w", ErrMatchInvalid, err)
	}
	if unless == "" {
		return c, nil
	}
	if c.unless, err = regexp.Compile(unless); err != nil {
		return Condition{}, fmt.Errorf("%w: %w", ErrUnlessInvalid, err)
	}
	return c, nil
}

func (c Condition) matches(value any) bool {
	s, ok := value.(string)
	if !ok || !c.match.MatchString(s) {
		return false
	}
	return c.unless == nil || !c.unless.MatchString(s)
}
