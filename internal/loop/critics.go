package loop

import (
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The critic report as one value
type CriticReport struct {
	Critics   []CriticRow   `json:"critics"`
	Reactions []ReactionRow `json:"reactions"`
}

// How often one critic agreed with what a person later decided on the same run
type CriticRow struct {
	// extract model and extract conversation for the critique an extraction kept
	// classifier and the member name for each member a classify trace asked
	Critic string `json:"critic"`
	Judged int    `json:"judged"`
	// Passed and a person approved, or refused and a person dropped every candidate
	Agree int `json:"agree"`
	// Passed and a person dropped every candidate of the run
	FalsePass int `json:"false_pass"`
	// Refused and a person approved a candidate of the run anyway
	FalseRefuse int `json:"false_refuse"`
	// No person decided on a candidate of the run yet
	Open int `json:"open"`
}

// What people decided on the candidates that cite a run
type decision int

const (
	undecided decision = iota // no candidate or none decided yet
	approved                  // a candidate citing the run was approved
	dropped                   // every candidate citing the run was retired or superseded
)

// The output of a classify trace as the report reads it
type classifyRecord struct {
	Members []classifyMember `json:"members"`
	// The answers the point acted on and none when every member failed or deferred
	Answers answers `json:"answers"`
}

type classifyMember struct {
	Name    string  `json:"name"`
	Answers answers `json:"answers"`
	Error   string  `json:"error"`
}

// Answers by question name as a classify trace keeps them
type answers map[string]classify.Answer

// Every answer is a yes as the critic point reads it
func (as answers) passed() bool {
	for _, a := range as {
		if !a.True() {
			return false
		}
	}
	return true
}

// One row per critic in name order
// A judgment counts once per draft so an extraction that redrafted counts twice
func (r Runs) Critics(items knowledge.Set, traces trace.Traces) []CriticRow {
	decided := decisions(items)
	rows := map[string]*CriticRow{}
	count := func(critic string, passed bool, run string) {
		row, ok := rows[critic]
		if !ok {
			row = &CriticRow{Critic: critic}
			rows[critic] = row
		}
		row.count(passed, decided[run])
	}
	for _, tr := range traces {
		switch tr.Name {
		case trace.NameExtract:
			var rec extractRecord
			if json.Unmarshal(tr.Output, &rec) != nil {
				continue
			}
			for _, a := range rec.Attempts {
				if len(a.Critique) > 0 {
					count("extract "+tr.Subject, a.Refusal != "critic", tr.Ref)
				}
			}
		case trace.NameClassify:
			var rec classifyRecord
			if tr.Subject != string(classify.PointCritic) || json.Unmarshal(tr.Output, &rec) != nil {
				continue
			}
			for _, m := range rec.Members {
				if m.Error == "" {
					count("classifier "+m.Name, m.Answers.passed(), tr.Ref)
				}
			}
		}
	}
	out := make([]CriticRow, 0, len(rows))
	for _, name := range slices.Sorted(maps.Keys(rows)) {
		out = append(out, *rows[name])
	}
	return out
}

func (row *CriticRow) count(passed bool, d decision) {
	row.Judged++
	switch {
	case d == undecided:
		row.Open++
	case passed == (d == approved):
		row.Agree++
	case passed:
		row.FalsePass++
	default:
		row.FalseRefuse++
	}
}

// How often one member of the reaction point agreed with the verdict the conversation recorded on the same run
type ReactionRow struct {
	// reaction and the member name
	Critic string `json:"critic"`
	// Answers the member gave
	Asked int `json:"asked"`
	// Answers on a run the conversation judged where every probability reached 0.8 on its side
	Sure int `json:"sure"`
	// A sure call of a correction or an approval that the session verdict matches
	Agree int `json:"agree"`
	// A sure call opposite to the session verdict
	Disagree int `json:"disagree"`
	// A sure answer that called no correction where the conversation recorded an edit or a reject
	MissedCorrection int `json:"missed_correction"`
	// A sure answer that called no approval where the conversation recorded an approve
	MissedApproval int `json:"missed_approval"`
	// An answer short of sure that the conversation then decided
	Deferred int `json:"deferred"`
}

// One row per member of the reaction point in name order
// 1. each answer is held against the latest verdict a session inferred on its run
// 2. a run without one counts only in asked
// 3. a run whose latest session verdict the point recorded itself counts only in asked since it cannot disagree with itself
func (r Runs) Reactions(traces trace.Traces) []ReactionRow {
	rows := map[string]*ReactionRow{}
	for _, tr := range traces {
		var rec classifyRecord
		if tr.Name != trace.NameClassify || tr.Subject != string(classify.PointReaction) || json.Unmarshal(tr.Output, &rec) != nil {
			continue
		}
		verdict, judged := r.conversationVerdict(tr.Ref, tr.Time, classify.Answers(rec.Answers).Verdict())
		for _, m := range rec.Members {
			if m.Error != "" {
				continue
			}
			name := "reaction " + m.Name
			row, ok := rows[name]
			if !ok {
				row = &ReactionRow{Critic: name}
				rows[name] = row
			}
			row.count(m.Answers, verdict, judged)
		}
	}
	out := make([]ReactionRow, 0, len(rows))
	for _, name := range slices.Sorted(maps.Keys(rows)) {
		out = append(out, *rows[name])
	}
	return out
}

// The latest verdict a session inferred on the run and whether the conversation gave it
// 1. the point records its own verdict right after the classify trace of the time asked
// 2. so the first session record from then on with the verdict the point called is its own
// 3. that record still being the latest leaves nothing the conversation decided
func (r Runs) conversationVerdict(run string, asked time.Time, recorded feedback.Verdict) (feedback.Feedback, bool) {
	latest := r.inferred[run].Latest()
	if len(latest) == 0 {
		return feedback.Feedback{}, false
	}
	if recorded == "" {
		return latest[0], true
	}
	var own feedback.Feedback
	for _, fb := range r.inferred[run] {
		if !fb.Time.Before(asked) && (own.TraceID == "" || fb.Time.Before(own.Time)) {
			own = fb
		}
	}
	if own.Verdict == recorded && own.Time.Equal(latest[0].Time) {
		return feedback.Feedback{}, false
	}
	return latest[0], true
}

func (row *ReactionRow) count(as answers, verdict feedback.Feedback, judged bool) {
	row.Asked++
	if !judged {
		return
	}
	if !classify.Answers(as).Sure() {
		row.Deferred++
		return
	}
	row.Sure++
	call := classify.Answers(as).Verdict()
	switch {
	case call == "" && verdict.Corrects():
		row.MissedCorrection++
	case call == "":
		row.MissedApproval++
	case (call == feedback.VerdictReject) == verdict.Corrects():
		row.Agree++
	default:
		row.Disagree++
	}
}

// What people decided per run over every record that cites it as feedback
// 1. an approval of any version decides the run as approved even when it was retired later
// 2. a version still a candidate keeps the run open
// 3. otherwise a retire or a supersede drops it
func decisions(items knowledge.Set) map[string]decision {
	approvedRuns, droppedRuns, openRuns := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, k := range items {
		for _, run := range k.Evidence.FeedbackTraceIDs {
			switch k.Status {
			case knowledge.StatusApproved:
				approvedRuns[run] = true
			case knowledge.StatusRetired, knowledge.StatusSuperseded:
				droppedRuns[run] = true
			}
		}
	}
	for _, k := range items.Versions() {
		if k.Status == knowledge.StatusCandidate {
			for _, run := range k.Evidence.FeedbackTraceIDs {
				openRuns[run] = true
			}
		}
	}
	out := map[string]decision{}
	for run := range droppedRuns {
		out[run] = dropped
	}
	for run := range openRuns {
		out[run] = undecided
	}
	for run := range approvedRuns {
		out[run] = approved
	}
	return out
}
