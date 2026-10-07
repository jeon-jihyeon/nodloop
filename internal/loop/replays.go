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
	Passed  bool   `json:"passed"`
	// Corrected outputs it cites and the ones it does not catch
	Corrected int `json:"corrected"`
	Missed    int `json:"missed"`
	// Approved outputs of its scope judged and the ones it would have changed
	Approved  int       `json:"approved"`
	Overreach int       `json:"overreach"`
	Time      time.Time `json:"time"`
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

// One row per version replayed, from its newest replay, in the order of the traces
// A replay trace whose output does not decode is left out
func Replays(traces trace.Traces) []ReplayRow {
	type key struct {
		id      string
		version int
	}
	seen := map[key]bool{}
	rows := []ReplayRow{}
	for _, tr := range traces {
		var rec replayRecord
		if tr.Name != trace.NameReplay || json.Unmarshal(tr.Output, &rec) != nil || seen[key{rec.ID, rec.Version}] {
			continue
		}
		seen[key{rec.ID, rec.Version}] = true
		row := ReplayRow{
			ID: rec.ID, Version: rec.Version, Passed: rec.Missed == 0 && rec.Overreach == 0,
			Missed: rec.Missed, Overreach: rec.Overreach, Time: tr.Time,
		}
		for _, c := range rec.Cases {
			if c.Expect == "breaks" {
				row.Corrected++
			} else {
				row.Approved++
			}
		}
		rows = append(rows, row)
	}
	return rows
}
