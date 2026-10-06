package loop

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

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
}

type classifyMember struct {
	Name    string `json:"name"`
	Answers map[string]struct {
		Yes float64 `json:"yes"`
	} `json:"answers"`
	Error string `json:"error"`
}

// Every answer is a yes from one half up as the critic point reads it
func (m classifyMember) passed() bool {
	for _, a := range m.Answers {
		if a.Yes < 0.5 {
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
			if json.Unmarshal(tr.Output, &rec) != nil {
				continue
			}
			for _, m := range rec.Members {
				if m.Error == "" {
					count("classifier "+m.Name, m.passed(), tr.Ref)
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
