package loop

import (
	"cmp"
	"maps"
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// How the scopes of the current run items of one plugin version fit the places runs come from
type ScopeRow struct {
	// The plugin version of the first run the item cites
	Version string `json:"version"`
	// Current candidates and approved items with a run scope
	Items int `json:"items"`
	// Items whose scope admits the runs of one session at most
	// The mark of a place such as a scratchpad that no later session works in
	SingleSession int `json:"single_session"`
	// Approved items no run applied
	NeverApplied int `json:"never_applied"`
}

// One row per plugin version in version order
// A run recorded without a session counts as a session of its own
func (r Runs) Scopes(items knowledge.Set, traces trace.Traces) []ScopeRow {
	rows := map[string]*ScopeRow{}
	for _, k := range items.Current() {
		if k.Run == nil || k.Veto != nil {
			continue
		}
		version := r.version(k.Evidence.FirstFeedback())
		row, ok := rows[version]
		if !ok {
			row = &ScopeRow{Version: version}
			rows[version] = row
		}
		row.Items++
		if sessionsAdmitted(*k.Run, traces) <= 1 {
			row.SingleSession++
		}
		if k.Status == knowledge.StatusApproved && len(r.applied[knowledge.Ref{ID: k.ID, Version: k.Version}]) == 0 {
			row.NeverApplied++
		}
	}
	out := make([]ScopeRow, 0, len(rows))
	for _, v := range slices.SortedFunc(maps.Keys(rows), cmp.Compare[string]) {
		out = append(out, *rows[v])
	}
	return out
}

// Distinct sessions among the runs the scope admits
func sessionsAdmitted(scope knowledge.RunScope, traces trace.Traces) int {
	sessions := map[string]bool{}
	for _, tr := range traces {
		if tr.Name == trace.NameRun && scope.Admits(tr.Producer, tr.Labels) {
			sessions[cmp.Or(tr.SessionID, tr.ID)] = true
		}
	}
	return len(sessions)
}
