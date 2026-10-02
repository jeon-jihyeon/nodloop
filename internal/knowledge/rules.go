package knowledge

import (
	"fmt"
	"strings"
)

// The sections of the rules in the order they print
// Meaning comes first so the facts lead the decisions that rest on them
var ruleSections = []struct {
	kind    Kind
	heading string
}{{KindMeaning, "Meaning"}, {KindJudgment, "Judgment"}}

// The approved items as Claude Code rules in id order
// 1. one line per item with its content on one line and its scope and exceptions and approver
// 2. a judgment with a veto says the guard enforces it
// 3. a record of the data review written before 0.6.0 is left out because it reaches no run
// Empty for a set without an approved item
func (s Set) Rules() string {
	var b strings.Builder
	approved := s.Approved()
	for _, section := range ruleSections {
		var lines []string
		for _, k := range approved {
			if k.Kind == section.kind && k.Run != nil {
				lines = append(lines, k.rule())
			}
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "\n## %s\n\n%s\n", section.heading, strings.Join(lines, "\n"))
		}
	}
	return b.String()
}

func (k Knowledge) rule() string {
	line := fmt.Sprintf("- %s v%d: %s. Scope: %s. Approved by %s",
		k.ID, k.Version, strings.TrimSuffix(strings.Join(strings.Fields(k.Content), " "), "."), k.reachText(), k.Approver)
	if k.Veto != nil {
		line += ". The guard blocks the call it forbids"
	}
	return line
}
