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
	enabled bool
}

// Validate the required fields and split the tool list
// tool is one exact `tool_name` or a list such as `Edit|Write`
func New(id, tool string, when []Condition, reason string, enabled bool) (Veto, error) {
	if id == "" {
		return Veto{}, ErrIDMissing
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
	return Veto{id: id, tools: tools, when: when, reason: reason, enabled: enabled}, nil
}

// Unique kebab case identifier used for merging and stderr output
func (v Veto) ID() string { return v.id }

// Written to stderr on block so Claude sees it
func (v Veto) Reason() string { return v.reason }

// Every condition must match and a disabled veto never matches
func (v Veto) Matches(tool string, input map[string]any) bool {
	if !v.enabled || !slices.Contains(v.tools, tool) {
		return false
	}
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

// First veto that matches and nil when none does
func (vs Vetoes) Match(tool string, input map[string]any) *Veto {
	for i := range vs {
		if vs[i].Matches(tool, input) {
			return &vs[i]
		}
	}
	return nil
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
