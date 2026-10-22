package curate_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// An extraction the critic passed for the candidate k-pref with its triage
const triageOutput = `{"conclusion":"proposed","candidate":{"id":"k-pref","version":1},` +
	`"triage":{"policy":"1","classify":"c1","nature":"preference","mixed":false,"reversibility":"H",` +
	`"source":"missing","protected":"none","lasting":true,"faithful":true,"conflict":false},` +
	`"attempts":[{"classify":"c1","refusal":"","error":"","draft":{"content":"Keep answers short",` +
	`"kind":"meaning","relation":"add"},"critique":{"states":true,"holds":true,"fits":true}}]}`

func TestCuratorPassTriageProvenance(t *testing.T) {
	type args struct {
		name    trace.Name
		subject string
		failed  string
		search  string
		replace string
	}
	tcs := []struct {
		name string
		args args
	}{
		{"a conversation supplied draft", args{trace.NameExtract, "conversation", "", "absent", ""}},
		{"another trace kind", args{trace.NameRun, "model", "", "absent", ""}},
		{"a failed extraction", args{trace.NameExtract, "model", "failed", "absent", ""}},
		{"an output that cannot decode", args{trace.NameExtract, "model", "", "{", "["}},
		{"another candidate", args{trace.NameExtract, "model", "", `"id":"k-pref"`, `"id":"k-other"`}},
		{"another candidate version", args{trace.NameExtract, "model", "", `"version":1`, `"version":2`}},
		{"a conflict extraction", args{trace.NameExtract, "model", "", `"conclusion":"proposed"`, `"conclusion":"conflict"`}},
		{"no triage", args{trace.NameExtract, "model", "", `"triage":{`, `"other":{`}},
		{"an unknown policy", args{trace.NameExtract, "model", "", `"policy":"1"`, `"policy":"other"`}},
		{"another lesson text", args{trace.NameExtract, "model", "", `"content":"Keep answers short"`, `"content":"Other"`}},
		{"another lesson kind", args{trace.NameExtract, "model", "", `"kind":"meaning"`, `"kind":"judgment"`}},
		{"an update draft", args{trace.NameExtract, "model", "", `"relation":"add"`, `"relation":"update"`}},
		{"a mismatched classifier", args{trace.NameExtract, "model", "", `"classify":"c1"`, `"classify":"c2"`}},
		{"no critique", args{trace.NameExtract, "model", "", `"critique":{`, `"other":{`}},
		{"a failed states check", args{trace.NameExtract, "model", "", `"states":true`, `"states":false`}},
		{"a failed holds check", args{trace.NameExtract, "model", "", `"holds":true`, `"holds":false`}},
		{"a failed fits check", args{trace.NameExtract, "model", "", `"fits":true`, `"fits":false`}},
		{"a refused attempt", args{trace.NameExtract, "model", "", `"refusal":""`, `"refusal":"critic"`}},
		{"a failed attempt", args{trace.NameExtract, "model", "", `"error":""`, `"error":"failed"`}},
		{"no attempts", args{trace.NameExtract, "model", "", `"attempts":[`, `"other":[`}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			k := lesson("k-pref", 1, "Keep answers short", acme("svc"), knowledge.StatusCandidate, "")
			k.Extract = "e1"
			b := newTriageBench(t, k)
			f, err := feedback.NewCaptured("r-k-pref", feedback.VerdictReject, feedback.ReasonForm,
				"Always keep answers short", written.Add(time.Minute))
			require.NoError(t, err)
			require.NoError(t, b.stores.Feedback.Append(ctx, f.Record()))
			raw := strings.Replace(triageOutput, tc.args.search, tc.args.replace, 1)
			x := trace.Trace{ID: "e1", Name: tc.args.name, Subject: tc.args.subject, Error: tc.args.failed,
				Ref: "r-k-pref", Time: written.Add(2 * time.Minute), Output: json.RawMessage(raw)}
			x.Input, err = json.Marshal(map[string]string{"verdict_id": f.RecordID(), "reviewer": f.Reviewer})
			require.NoError(t, err)
			// A malformed historical line is read through the store rather than accepted as a new trace
			if !json.Valid(x.Output) {
				x.Output = json.RawMessage(`"invalid"`)
			}
			require.NoError(t, b.stores.Traces.Append(ctx, x.Record()))
			s := b.setting(t)
			s.Delegations = []curate.Delegation{{ID: "g1", Scope: *acme("svc")}}

			got, err := b.triageCurator().Pass(ctx, s)

			require.NoError(t, err)
			require.Len(t, got.Decided, 1)
			assert.Equal(t, curate.ActionHold, got.Decided[0].Decision.Action)
			assert.Empty(t, got.Decided[0].Decision.Delegation)
			assert.Empty(t, got.Due)
		})
	}
}

func TestCuratorPassTriageSource(t *testing.T) {
	type args struct {
		extract string
		input   string
		ref     string
		// A second cited run whose verdict a person turned to approve
		approved bool
		// A lesson the person approved in the place after the extraction
		newer bool
	}
	type want struct {
		source    classify.Source
		protected classify.Protection
		action    curate.Action
	}
	good := args{extract: "e1", input: `{"verdict_id":"%s","reviewer":"nodloop"}`, ref: "r-k-pref"}
	with := func(change func(*args)) args { copy := good; change(&copy); return copy }
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a direct complete correction", good, want{classify.SourceComplete, classify.ProtectionNone, curate.ActionApply}},
		{"an extraction the store lacks", with(func(a *args) { a.extract = "e-gone" }),
			want{classify.SourceMissing, classify.ProtectionUnknown, curate.ActionHold}},
		{"an extraction that names no verdict", with(func(a *args) { a.input = `{}` }),
			want{classify.SourceMissing, classify.ProtectionNone, curate.ActionHold}},
		{"an extraction of another run", with(func(a *args) { a.ref = "r-other" }),
			want{classify.SourceMissing, classify.ProtectionNone, curate.ActionHold}},
		{"a cited run no longer corrected", with(func(a *args) { a.approved = true }),
			want{classify.SourceMissing, classify.ProtectionNone, curate.ActionHold}},
		{"a person lesson newer than the extraction", with(func(a *args) { a.newer = true }),
			want{classify.SourceComplete, classify.ProtectionUnknown, curate.ActionHold}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			k := lesson("k-pref", 1, "Keep answers short", acme("svc"), knowledge.StatusCandidate, "")
			k.Extract = tc.args.extract
			if tc.args.approved {
				k.Evidence.FeedbackTraceIDs = append(k.Evidence.FeedbackTraceIDs, "r-k-two")
			}
			b := newTriageBench(t, k)
			b.correct(t, "r-k-two", acme("svc").Labels, []feedback.Verdict{feedback.VerdictReject, feedback.VerdictApprove})
			if tc.args.newer {
				later := lesson("k-later", 1, "Use plain words", acme("svc"), knowledge.StatusApproved, "ann")
				later.Time, later.ApprovedAt = written.Add(3*time.Minute), written.Add(3*time.Minute)
				require.NoError(t, testkit.Err(b.stores.Ledger.Import(ctx, []knowledge.Knowledge{later})))
			}
			f, err := feedback.NewCaptured("r-k-pref", feedback.VerdictReject, feedback.ReasonForm,
				"Always keep answers short", written.Add(time.Minute))
			require.NoError(t, err)
			require.NoError(t, b.stores.Feedback.Append(ctx, f.Record()))
			x := trace.Trace{ID: "e1", Name: trace.NameExtract, Subject: "model", Ref: tc.args.ref,
				Time: written.Add(2 * time.Minute), Output: json.RawMessage(triageOutput)}
			x.Input = json.RawMessage(strings.Replace(tc.args.input, "%s", f.RecordID(), 1))
			require.NoError(t, b.stores.Traces.Append(ctx, x.Record()))
			s := b.setting(t)
			s.Delegations = []curate.Delegation{{ID: "g1", Scope: *acme("svc")}}

			got, err := b.triageCurator().Pass(ctx, s)

			require.NoError(t, err)
			require.Len(t, got.Decided, 1)
			d := got.Decided[0].Decision
			require.NotNil(t, d.Triage)
			assert.Equal(t, tc.want, want{d.Triage.Source, d.Triage.Protected, d.Action})
		})
	}
}

func TestCuratorPassSourceBinding(t *testing.T) {
	tcs := []struct {
		name string
		args string
	}{
		{"a summary used to draft the candidate differs from the direct correction", "summary"},
		{"a newer captured correction cannot authorize an older extraction", "capture"},
		{"a newer person proof cannot authorize an older extraction", "person"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			k := lesson("k-pref", 1, "Use tables in answers", acme("svc"), knowledge.StatusCandidate, "")
			k.Extract = "e1"
			b := newTriageBench(t, k)
			first, err := feedback.NewCaptured("r-k-pref", feedback.VerdictReject, feedback.ReasonForm,
				"Always use short paragraphs", written.Add(time.Minute))
			require.NoError(t, err)
			require.NoError(t, b.stores.Feedback.Append(ctx, first.Record()))
			if tc.args == "summary" {
				summary, err := feedback.New("r-k-pref", feedback.VerdictReject, feedback.ReasonForm,
					"Always use tables", nil, feedback.ReviewerSession, written.Add(90*time.Second))
				require.NoError(t, err)
				require.NoError(t, b.stores.Feedback.Append(ctx, summary.Record()))
			}
			b.triage(t, k, preferenceTriage())
			if tc.args == "capture" {
				later, err := feedback.NewCaptured("r-k-pref", feedback.VerdictReject, feedback.ReasonForm,
					"Always use tables", written.Add(3*time.Minute))
				require.NoError(t, err)
				require.NoError(t, b.stores.Feedback.Append(ctx, later.Record()))
			}
			if tc.args == "person" {
				later, err := feedback.New("r-k-pref", feedback.VerdictReject, feedback.ReasonForm,
					"Always use tables", nil, "ann", written.Add(3*time.Minute))
				require.NoError(t, err)
				require.NoError(t, b.stores.Feedback.Append(ctx, later.WithProof(trace.ProofTerminal).Record()))
			}
			s := b.setting(t)
			s.Delegations = []curate.Delegation{{ID: "g1", Scope: *acme("svc")}}

			got, err := b.triageCurator().Pass(ctx, s)

			require.NoError(t, err)
			require.Len(t, got.Decided, 1)
			assert.Equal(t, curate.ActionHold, got.Decided[0].Decision.Action)
			require.NotNil(t, got.Decided[0].Decision.Triage)
			assert.Equal(t, classify.SourceMissing, got.Decided[0].Decision.Triage.Source)
			assert.Empty(t, got.Decided[0].Decision.Delegation)
		})
	}
}
