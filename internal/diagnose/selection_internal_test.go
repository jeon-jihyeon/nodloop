package diagnose

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
)

// The lead of an example names the paragraphs its correction cites that the review under way cannot cite
// The line is in the lead so a cap never cuts it
func TestExampleRenderUnlisted(t *testing.T) {
	const (
		segment = "metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"
		outcome = "metric-anomaly-investigation#Metric anomaly investigation/Check downstream outcomes#1"
		renamed = "metric-anomaly-investigation#Metric anomaly investigation/Check conversions#1"
	)
	edited := json.RawMessage(`{"status":"ready_for_review","causes":[{"summary":"s","paragraph_ids":["` + outcome + `"]}],` +
		`"checks":[{"step":"segment","purpose":"p","paragraph_ids":["` + segment + `"]}]}`)
	original := json.RawMessage(`{"status":"ready_for_review","causes":[{"summary":"s","paragraph_ids":["` + renamed + `"]}]}`)
	known := func(ids ...string) citable {
		return Context{ParagraphIDs: ids}.citable()
	}
	type args struct {
		example example
		known   citable
	}
	tcs := []struct {
		name string
		args args
		// The unlisted line or empty
		want string
	}{
		{
			"an edit citing a paragraph the review lists carries no line",
			args{example{Verdict: feedback.VerdictEdit, Original: original, Edited: edited}, known(segment, outcome)},
			"",
		},
		{
			"an edit citing a cause paragraph a rename removed names it",
			args{example{Verdict: feedback.VerdictEdit, Original: original, Edited: edited}, known(segment, renamed)},
			"Paragraphs this review does not list: " + outcome + ". Follow the correction through the listed paragraph that states the same finding\n",
		},
		{
			"the same edit read by an event whose procedures list none of its paragraphs names the check paragraph too",
			args{example{Verdict: feedback.VerdictEdit, Original: original, Edited: edited}, known()},
			"Paragraphs this review does not list: " + segment + ", " + outcome +
				". Follow the correction through the listed paragraph that states the same finding\n",
		},
		{
			"a reject whose original cites a paragraph the review does not list carries no line",
			args{example{Verdict: feedback.VerdictReject, Original: original}, known()},
			"",
		},
		{
			"an edited review that does not decode carries no line",
			args{example{Verdict: feedback.VerdictEdit, Original: original, Edited: json.RawMessage(`"by hand"`)}, known()},
			"",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := tc.args.example.render(1, tc.args.known)

			assert.Equal(t, tc.want, tc.args.example.unlisted(tc.args.known))
			if tc.want != "" {
				assert.Contains(t, got.lead, tc.want)
			}
			assert.NotContains(t, got.body+got.tail, "does not list")
		})
	}
}
