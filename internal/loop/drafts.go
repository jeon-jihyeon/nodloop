package loop

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// How the drafted candidates of one plugin version and one path were decided
type DraftRow struct {
	// The plugin version of the first run the candidate cites
	Version string `json:"version"`
	// The path of the extraction that proposed it and unknown before 0.7
	Path     string `json:"path"`
	Drafted  int    `json:"drafted"`
	Approved int    `json:"approved"`
	// Retired or superseded before any approval
	Dropped int `json:"dropped"`
	Waiting int `json:"waiting"`
	// Median time from the candidate to its first decision
	// Zero when none was decided
	Decide time.Duration `json:"decide_ns"`
}

// One drafted version as its records tell it
type draft struct {
	version, path string
	proposed      time.Time
	approved      time.Time
	dropped       time.Time
}

// The first decision and when it came
func (d draft) decided() (time.Time, bool) {
	switch {
	case !d.approved.IsZero():
		return d.approved, true
	case !d.dropped.IsZero():
		return d.dropped, true
	}
	return time.Time{}, false
}

// One row per plugin version and path in that order over every version a model or the conversation drafted
func (r Runs) Drafts(items knowledge.Set, traces trace.Traces) []DraftRow {
	type key struct{ version, path string }
	rows := map[key]*DraftRow{}
	waits := map[key]samples{}
	for _, d := range r.drafted(items, proposedPaths(traces)) {
		k := key{d.version, d.path}
		row, ok := rows[k]
		if !ok {
			row = &DraftRow{Version: d.version, Path: d.path}
			rows[k] = row
		}
		row.Drafted++
		at, decided := d.decided()
		switch {
		case !decided:
			row.Waiting++
		case !d.approved.IsZero():
			row.Approved++
		default:
			row.Dropped++
		}
		if decided {
			waits[k] = append(waits[k], at.Sub(d.proposed).Seconds())
		}
	}
	keys := slices.SortedFunc(maps.Keys(rows), func(a, b key) int {
		return cmp.Or(cmp.Compare(a.version, b.version), cmp.Compare(a.path, b.path))
	})
	out := make([]DraftRow, 0, len(keys))
	for _, k := range keys {
		row := *rows[k]
		if m := waits[k].median(); m != nil {
			row.Decide = time.Duration(*m * float64(time.Second))
		}
		out = append(out, row)
	}
	return out
}

// Every drafted version with its decisions read oldest first
// 1. an approval decides a version even when it was retired later
// 2. a retire or a supersede before any approval drops it
func (r Runs) drafted(items knowledge.Set, paths map[knowledge.Ref]string) map[knowledge.Ref]*draft {
	drafts := map[knowledge.Ref]*draft{}
	for _, k := range slices.Backward(items) {
		ref := knowledge.Ref{ID: k.ID, Version: k.Version}
		d, ok := drafts[ref]
		switch {
		case !ok && (!k.Drafted || k.Status != knowledge.StatusCandidate):
			continue
		case !ok:
			drafts[ref] = &draft{version: r.version(k.Evidence.FirstFeedback()), path: cmp.Or(paths[ref], unknown), proposed: k.Time}
		case k.Status == knowledge.StatusApproved && d.approved.IsZero():
			d.approved = k.Time
		case (k.Status == knowledge.StatusRetired || k.Status == knowledge.StatusSuperseded) && d.dropped.IsZero():
			d.dropped = k.Time
		}
	}
	return drafts
}

// The path of the extraction that proposed each candidate version
func proposedPaths(traces trace.Traces) map[knowledge.Ref]string {
	out := map[knowledge.Ref]string{}
	for _, tr := range traces {
		var rec extractRecord
		if tr.Name != trace.NameExtract || json.Unmarshal(tr.Output, &rec) != nil || rec.Candidate == nil {
			continue
		}
		out[knowledge.Ref{ID: rec.Candidate.ID, Version: rec.Candidate.Version}] = tr.Subject
	}
	return out
}
