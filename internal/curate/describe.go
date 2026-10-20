package curate

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The action and its target in plain words for the form that asks the person to confirm it
// The request is resolved first so a form never asks about what the records cannot carry out
func (iv *Intervener) Describe(ctx context.Context, r Request) (string, error) {
	s, err := iv.read(ctx)
	if err != nil {
		return "", err
	}
	in, err := iv.resolve(ctx, r, s)
	if err != nil {
		return "", err
	}
	return s.describe(in), nil
}

// What the intervention does with no id, version or number the person cannot see
func (s state) describe(in trace.Intervention) string {
	lesson := s.lesson(in)
	switch in.Action {
	case trace.ActionJudge:
		return judged(in.Choice, lesson, in.Labels)
	case trace.ActionAnswer:
		return fmt.Sprintf("Answer %s to the question about %s", in.Choice, lesson)
	case trace.ActionDismiss:
		return fmt.Sprintf("Close the question about %s with no decision, so nodloop never carries it out", lesson)
	case trace.ActionPause:
		return "Pause what nodloop carries out" + paused(in.Target, in.Labels) + ", so every such change is asked of you"
	case trace.ActionResume:
		return "End the pause you set, so nodloop carries out again only what you had opened"
	case trace.ActionKnob:
		return fmt.Sprintf("Set %s to %g from the next cycle", knobWords(KnobName(in.Knob)), *in.Value)
	case trace.ActionReanchor:
		return fmt.Sprintf("Take the lessons nodloop approved for %s as your own", knowledge.RunScope{Producer: in.Target, Labels: in.Labels})
	case trace.ActionReverse:
		x, _ := s.holds(in.Reverses)
		return "Take back your earlier decision: " + s.describe(x.In)
	}
	return "Show what nodloop prepared since your last decision"
}

// The lesson a judgment or a question names as its content in quotes
func (s state) lesson(in trace.Intervention) string {
	if k, err := s.all.Version(in.ID, in.Version); err == nil {
		return fmt.Sprintf("the lesson %q", k.Content)
	}
	if in.Compaction != "" {
		return "the merge of lessons that was prepared"
	}
	return "a change nodloop prepared"
}

func judged(choice trace.Choice, lesson string, labels trace.Labels) string {
	switch choice {
	case trace.ChoiceUndo:
		return "Undo " + lesson
	case trace.ChoiceScope:
		return fmt.Sprintf("Keep %s for %s", lesson, labelWords(labels))
	}
	return "Keep " + lesson
}

func paused(kind string, labels trace.Labels) string {
	out := ""
	if kind != "" {
		out = " for the kind " + kind
	}
	if len(labels) > 0 {
		out += " in " + labelWords(labels)
	}
	return out
}

func labelWords(labels trace.Labels) string {
	if len(labels) == 0 {
		return "every place"
	}
	parts := make([]string, 0, len(labels))
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		parts = append(parts, key+" "+strings.Join(labels[key], " or "))
	}
	return strings.Join(parts, " and ")
}

func knobWords(n KnobName) string {
	switch n {
	case KnobAuditTier0:
		return "the share of rule decisions checked with you"
	case KnobAuditTier1:
		return "the share of judged decisions checked with you"
	case KnobItemHold:
		return "the share of sessions that go without each lesson nodloop approved"
	}
	return "the weeks of one cycle"
}
