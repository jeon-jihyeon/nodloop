package loop

import (
	"encoding/json"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// How the newest replay of one knowledge version went
type ReplayRow struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	// Both axes held a case and none went wrong
	Passed bool `json:"passed"`
	// Corrected outputs it cites and the ones it does not catch
	Corrected int `json:"corrected"`
	Missed    int `json:"missed"`
	// Approved outputs of its scope judged and the ones it would have changed
	Approved  int       `json:"approved"`
	Overreach int       `json:"overreach"`
	Time      time.Time `json:"time"`
}

// How a report line shows the replay by the rule of the replay package
// loop never imports replay so the rule is read again from the counts here
// 1. failed when a case went wrong
// 2. partial when the corrected or the approved outputs judged number zero since that axis is untested
// 3. passed otherwise
func (r ReplayRow) Outcome() string {
	switch {
	case r.Missed > 0 || r.Overreach > 0:
		return "failed"
	case r.Corrected == 0 || r.Approved == 0:
		return "partial"
	}
	return "passed"
}

// The output of a replay trace as the replay package writes it
// loop reads it as a record and never imports replay
type replayRecord struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Cases   []struct {
		Expect string `json:"expect"`
	} `json:"cases"`
	Missed    int `json:"missed"`
	Overreach int `json:"overreach"`
}

// One row per version replayed from its newest replay in the order of the traces
// A failed replay and a replay trace whose output does not decode are left out
func (s Snapshot) Replays() []ReplayRow {
	type key struct {
		id      string
		version int
	}
	seen := map[key]bool{}
	rows := []ReplayRow{}
	for _, tr := range s.traces {
		var rec replayRecord
		if tr.Name != trace.NameReplay || tr.Error != "" || json.Unmarshal(tr.Output, &rec) != nil || seen[key{rec.ID, rec.Version}] {
			continue
		}
		seen[key{rec.ID, rec.Version}] = true
		row := ReplayRow{ID: rec.ID, Version: rec.Version, Missed: rec.Missed, Overreach: rec.Overreach, Time: tr.Time}
		for _, c := range rec.Cases {
			if c.Expect == "breaks" {
				row.Corrected++
			} else {
				row.Approved++
			}
		}
		row.Passed = row.Outcome() == "passed"
		rows = append(rows, row)
	}
	return rows
}
