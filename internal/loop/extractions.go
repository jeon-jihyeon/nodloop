package loop

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// How the extractions of one plugin version and one path ended
type ExtractRow struct {
	// The plugin version of the corrected run
	Version string `json:"version"`
	// model or conversation
	Path        string `json:"path"`
	Extractions int    `json:"extractions"`
	// Extractions by conclusion such as proposed or refused
	Conclusions map[string]int `json:"conclusions"`
	// Drafts by what refused them such as code or critic
	Refusals map[string]int `json:"refusals"`
	// Critic questions answered false over every refused draft
	Questions map[string]int `json:"questions"`
}

// The output of an extract trace as the report reads it
type extractRecord struct {
	Attempts []struct {
		Refusal   string   `json:"refusal"`
		Questions []string `json:"questions"`
	} `json:"attempts"`
	Conclusion string `json:"conclusion"`
	Candidate  *struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	} `json:"candidate"`
}

// One row per plugin version and path in that order
// An extract trace whose output does not read is left out because nothing says how it ended
func (r Runs) Extractions(traces trace.Traces) []ExtractRow {
	type key struct{ version, path string }
	rows := map[key]*ExtractRow{}
	for _, tr := range traces {
		var rec extractRecord
		if tr.Name != trace.NameExtract || json.Unmarshal(tr.Output, &rec) != nil {
			continue
		}
		k := key{r.version(tr.Ref), tr.Subject}
		row, ok := rows[k]
		if !ok {
			row = &ExtractRow{Version: k.version, Path: k.path, Conclusions: map[string]int{}, Refusals: map[string]int{}, Questions: map[string]int{}}
			rows[k] = row
		}
		row.Extractions++
		row.Conclusions[rec.Conclusion]++
		for _, a := range rec.Attempts {
			if a.Refusal == "" {
				continue
			}
			row.Refusals[a.Refusal]++
			for _, q := range a.Questions {
				row.Questions[q]++
			}
		}
	}
	keys := slices.SortedFunc(maps.Keys(rows), func(a, b key) int {
		return cmp.Or(cmp.Compare(a.version, b.version), cmp.Compare(a.path, b.path))
	})
	out := make([]ExtractRow, 0, len(keys))
	for _, k := range keys {
		out = append(out, *rows[k])
	}
	return out
}
