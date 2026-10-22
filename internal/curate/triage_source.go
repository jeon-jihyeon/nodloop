package curate

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

type triageExtraction struct {
	Triage     *Triage         `json:"triage"`
	Candidate  knowledge.Ref   `json:"candidate"`
	Conclusion string          `json:"conclusion"`
	Attempts   []triageAttempt `json:"attempts"`
}

type triageAttempt struct {
	Classify string `json:"classify"`
	Refusal  string `json:"refusal"`
	Error    string `json:"error"`
	Critique *struct {
		States bool `json:"states"`
		Holds  bool `json:"holds"`
		Fits   bool `json:"fits"`
	} `json:"critique"`
	Draft struct {
		Content  string         `json:"content"`
		Kind     knowledge.Kind `json:"kind"`
		Relation string         `json:"relation"`
	} `json:"draft"`
}

func (e triageExtraction) names(id string, version int) bool {
	return e.Triage != nil && e.Triage.valid() && e.Candidate.ID == id && e.Candidate.Version == version &&
		e.Conclusion == "proposed" && len(e.Attempts) > 0
}

func (a triageAttempt) matches(content string, kind knowledge.Kind, classifier string) bool {
	return a.Draft.Content == content && a.Draft.Kind == kind && a.Draft.Relation == "add" &&
		(classifier == "" || a.Classify == classifier) && a.Critique != nil &&
		a.Critique.States && a.Critique.Holds && a.Critique.Fits && a.Refusal == "" && a.Error == ""
}

// The triage of the candidate as its own model extraction recorded it with the source curate proved
// 1. no extraction or one not proposing this exact add of the final attempt leaves every label unknown
// 2. a person lesson of the place newer than the extraction makes its conflict check stale so protection turns unknown
func (p *pass) triaged(ctx context.Context, k knowledge.Knowledge, place AnchorPlace) (*Triage, error) {
	t := &Triage{Triage: classify.Triage{Policy: classify.TriagePolicy, Nature: classify.NatureUnknown,
		Reversibility: classify.ReversibilityUnknown, Source: classify.SourceMissing, Protected: classify.ProtectionUnknown}}
	if k.Extract == "" {
		return t, nil
	}
	found, err := p.traces.Get(ctx, k.Extract)
	if errors.Is(err, record.ErrTraceNotFound) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	var out triageExtraction
	if found.Name != string(trace.NameExtract) || found.Subject != "model" || found.Error != "" ||
		json.Unmarshal(found.Output, &out) != nil || !out.names(k.ID, k.Version) {
		return t, nil
	}
	last := out.Attempts[len(out.Attempts)-1]
	if !last.matches(k.Content, k.Kind, out.Triage.Classify) {
		return t, nil
	}
	t = out.Triage
	t.Grant = ""
	if slices.ContainsFunc(place.Items, func(item knowledge.Knowledge) bool { return item.Time.After(found.Time) }) {
		t.Protected = classify.ProtectionUnknown
	}
	var input struct {
		VerdictID string `json:"verdict_id"`
		Reviewer  string `json:"reviewer"`
	}
	if json.Unmarshal(found.Input, &input) != nil || input.VerdictID == "" {
		t.Source = classify.SourceMissing
		return t, nil
	}
	t.Source, err = p.source(ctx, k, found.Ref, input.VerdictID, input.Reviewer)
	return t, err
}

// Whether every cited correction is a complete direct one within the place of its run
// The correction the extraction read must be the one the reconciled verdict still selects
func (p *pass) source(ctx context.Context, k knowledge.Knowledge, origin, verdict, reviewer string) (classify.Source, error) {
	source := classify.SourceMissing
	bound := false
	for _, id := range k.Evidence.FeedbackTraceIDs {
		rows, err := p.verdicts.List(ctx, record.FeedbackFilter{TraceID: id})
		if err != nil {
			return classify.SourceMissing, err
		}
		f, ok := corrections(feedback.RestoreAll(rows)).direct(id)
		if !ok {
			return classify.SourceMissing, nil
		}
		if id == origin {
			if f.RecordID() != verdict || f.Reviewer != reviewer {
				return classify.SourceMissing, nil
			}
			bound = true
		}
		if f.Reason == "" || utf8.RuneCountInString(f.Reason) >= 500 || strings.Contains(f.Reason, "[redacted]") {
			return classify.SourceIncomplete, nil
		}
		run, err := p.run(ctx, id)
		if err != nil {
			return classify.SourceMissing, err
		}
		if !k.Run.Within(knowledge.RunScope{Producer: run.Producer, Labels: run.Labels.Place()}) {
			return classify.SourceScope, nil
		}
		source = classify.SourceComplete
	}
	if !bound {
		return classify.SourceMissing, nil
	}
	return source, nil
}

type corrections feedback.Records

// The direct correction of the run that the reconciled verdict selects and false for none
// A person's proven verdict comes first and else the capture of the transcript, never a summary of the session
func (cs corrections) direct(id string) (feedback.Feedback, bool) {
	verdicts := feedback.Records(cs)
	current := verdicts.Reconciled()
	if len(current) == 0 || !current[0].Corrects() {
		return feedback.Feedback{}, false
	}
	var direct feedback.Feedback
	if human := verdicts.Human().Latest(); len(human) > 0 {
		if human[0].Proven() {
			direct = human[0]
		}
	}
	if direct.TraceID == "" {
		direct = verdicts.Reader(feedback.ReviewerNodloop)[id]
	}
	return direct, direct.Corrects() && current[0].RecordID() == direct.RecordID() &&
		current[0].Reviewer == direct.Reviewer
}
