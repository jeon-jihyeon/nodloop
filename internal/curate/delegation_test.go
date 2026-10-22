package curate_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func preferenceTriage() *curate.Triage {
	return &curate.Triage{Triage: classify.Triage{Policy: classify.TriagePolicy, Classify: "c1", Nature: classify.NaturePreference,
		Reversibility: classify.ReversibilityH, Source: classify.SourceMissing, Protected: classify.ProtectionNone,
		Lasting: true, Faithful: true}}
}

func newTriageBench(t *testing.T, k knowledge.Knowledge) bench {
	t.Helper()
	b := bench{stores: testkit.Open(t), client: llmmock.NewMockClient(gomock.NewController(t))}
	b.client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(
		llm.Response{Output: answer(0.04, 0.02), Model: "claude-sonnet-5-5"}, nil).AnyTimes()
	b.correct(t, "r-"+k.ID, k.Run.Labels, nil)
	require.NoError(t, testkit.Err(b.stores.Ledger.Import(context.Background(), []knowledge.Knowledge{personLesson(), k})))
	return b
}

// The HMAC a decide and the knobs name a grant by
func grantHash(t *testing.T, id string, scope knowledge.RunScope) string {
	t.Helper()
	raw, err := json.Marshal(scope)
	require.NoError(t, err)
	return hexSeal(classify.TriagePolicy + ":" + id + ":" + string(raw))
}

func (b bench) triageCurator() *curate.Curator {
	return curate.New(b.stores.Ledger, b.stores.Traces, b.stores.Feedback, b.client, nil, hexSeal, b.stores.Clock.Now)
}

func (b bench) triage(t *testing.T, k knowledge.Knowledge, metadata *curate.Triage) {
	t.Helper()
	classifier := ""
	if metadata != nil {
		classifier = metadata.Classify
	}
	out, err := json.Marshal(map[string]any{
		"conclusion": "proposed", "candidate": knowledge.Ref{ID: k.ID, Version: k.Version}, "triage": metadata,
		"attempts": []any{map[string]any{"classify": classifier,
			"draft":    map[string]any{"content": k.Content, "kind": k.Kind, "relation": "add"},
			"critique": map[string]bool{"states": true, "holds": true, "fits": true}}},
	})
	require.NoError(t, err)
	x := trace.Trace{ID: k.Extract, Name: trace.NameExtract, Subject: "model", Ref: "r-" + k.ID,
		Time: written.Add(2 * time.Minute), Output: out}
	rows, err := b.stores.Feedback.List(context.Background(), record.FeedbackFilter{TraceID: "r-" + k.ID})
	require.NoError(t, err)
	current := feedback.RestoreAll(rows).Reconciled()
	require.NotEmpty(t, current)
	x.Input, err = json.Marshal(map[string]string{"verdict_id": current[0].RecordID(), "reviewer": current[0].Reviewer})
	require.NoError(t, err)
	require.NoError(t, b.stores.Traces.Append(context.Background(), x.Record()))
}

func TestCuratorPassDelegation(t *testing.T) {
	type args struct {
		triage *curate.Triage
		reason string
		grant  string
		limit  string
		files  bool
		known  bool
		paused bool
	}
	type want struct {
		action curate.Action
		source classify.Source
		status knowledge.Status
	}
	changed := func(change func(*curate.Triage)) *curate.Triage {
		t := preferenceTriage()
		change(t)
		return t
	}
	good := args{triage: preferenceTriage(), reason: "Always keep answers short", grant: "g1", limit: "svc"}
	with := func(change func(*args)) args { copy := good; change(&copy); return copy }
	applied := want{curate.ActionApply, classify.SourceComplete, knowledge.StatusApproved}
	held := want{curate.ActionHold, classify.SourceComplete, knowledge.StatusCandidate}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a new preference inside an explicit grant", good, applied},
		{"the built in critic applies without a separate classify record", with(func(a *args) {
			a.triage = changed(func(v *curate.Triage) { v.Classify = "" })
		}), applied},
		{"a grant never needs the old judge shadow to end", with(func(a *args) {
			a.known, a.limit = true, "acme"
		}), applied},
		{"an existing judge checks loaded direction without another call", with(func(a *args) {
			a.known, a.files, a.limit = true, true, "acme"
		}), applied},
		{"a wider place with files gets no new judge call", with(func(a *args) { a.files = true }), held},
		{"no grant", with(func(a *args) { a.grant = "" }), held},
		{"a different grant scope", with(func(a *args) { a.limit = "other" }), held},
		{"a paused grant", with(func(a *args) { a.paused = true }), held},
		{"no classification", with(func(a *args) { a.triage = nil }),
			want{curate.ActionHold, classify.SourceMissing, knowledge.StatusCandidate}},
		{"a truncated correction", with(func(a *args) { a.reason = strings.Repeat("x", 500) }),
			want{curate.ActionHold, classify.SourceIncomplete, knowledge.StatusCandidate}},
		{"a redacted correction", with(func(a *args) { a.reason = "Always use [redacted]" }),
			want{curate.ActionHold, classify.SourceIncomplete, knowledge.StatusCandidate}},
		{"no captured correction", with(func(a *args) { a.reason = "" }),
			want{curate.ActionHold, classify.SourceMissing, knowledge.StatusCandidate}},
		{"a safety concern", with(func(a *args) {
			a.triage = changed(func(v *curate.Triage) { v.Nature = classify.NatureSafety })
		}), held},
		{"an organization rule", with(func(a *args) {
			a.triage = changed(func(v *curate.Triage) { v.Nature = classify.NatureOrganization })
		}), held},
		{"mixed content", with(func(a *args) { a.triage = changed(func(v *curate.Triage) { v.Mixed = true }) }), held},
		{"code style", with(func(a *args) {
			a.triage = changed(func(v *curate.Triage) { v.Reversibility = classify.ReversibilityM })
		}), held},
		{"an external effect", with(func(a *args) {
			a.triage = changed(func(v *curate.Triage) { v.Reversibility = classify.ReversibilityL })
		}), held},
		{"a one time correction", with(func(a *args) {
			a.triage = changed(func(v *curate.Triage) { v.Lasting = false })
		}), held},
		{"a stronger rule", with(func(a *args) { a.triage = changed(func(v *curate.Triage) { v.Faithful = false }) }), held},
		{"a direction conflict", with(func(a *args) {
			a.triage = changed(func(v *curate.Triage) { v.Conflict = true })
		}), held},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			repo := "svc"
			if tc.args.known {
				repo = "acme"
			}
			k := lesson("k-pref", 1, "Keep answers short", acme(repo), knowledge.StatusCandidate, "")
			k.Extract = "e1"
			b := newTriageBench(t, k)
			if tc.args.reason != "" {
				f, err := feedback.NewCaptured("r-k-pref", feedback.VerdictReject, feedback.ReasonForm,
					tc.args.reason, written.Add(time.Minute))
				require.NoError(t, err)
				require.NoError(t, b.stores.Feedback.Append(ctx, f.Record()))
			} else {
				f, err := feedback.New("r-k-pref", feedback.VerdictReject, feedback.ReasonForm,
					"The model's summary", nil, feedback.ReviewerSession, written.Add(time.Minute))
				require.NoError(t, err)
				require.NoError(t, b.stores.Feedback.Append(ctx, f.Record()))
			}
			b.triage(t, k, tc.args.triage)
			s := b.setting(t)
			s.Delegations = []curate.Delegation{{ID: tc.args.grant, Scope: *acme(tc.args.limit)}}
			if tc.args.files {
				files := []curate.AnchorFile{{Path: "rules.md", HMAC: hexSeal("Keep answers short")}}
				s.Anchor = curate.NewAnchor(b.all(t), files, nil)
				s.Read = func(string) (string, error) { return "Keep answers short", nil }
			}
			if tc.args.paused {
				b.knobs(t, func(k *curate.Knobs) {
					k.DelegationPaused = []string{grantHash(t, tc.args.grant, *acme(tc.args.limit))}
				})
			}

			got, err := b.triageCurator().Pass(ctx, s)

			require.NoError(t, err)
			require.Len(t, got.Decided, 1)
			all := b.all(t)
			latest, err := all.Version(k.ID, 1)
			require.NoError(t, err)
			d := got.Decided[0].Decision
			require.NotNil(t, d.Triage)
			assert.Equal(t, tc.want, want{d.Action, d.Triage.Source, latest.Status})
			_, anchored := all.PersonApproved().Find(k.ID)
			assert.False(t, anchored)
			assert.Empty(t, d.Bin)
		})
	}
}

func TestCuratorPassDelegationReconsidered(t *testing.T) {
	type args struct {
		initial string
		next    string
		paused  bool
		files   bool
		orphan  bool
	}
	tcs := []struct {
		name string
		args args
		want int
	}{
		{"an explicit grant reopens an eligible hold", args{next: "g1"}, 1},
		{"a new grant can replace a paused grant", args{initial: "g1", next: "g2", paused: true}, 1},
		{"another pass does not repeat the same held grant", args{initial: "g1", next: "g1", files: true}, 0},
		{"a crash before the ledger write gets fresh validation", args{initial: "g1", next: "g1", orphan: true}, 1},
		{"a removed grant never recovers an orphan approval", args{initial: "g1", orphan: true}, 1},
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
			b.triage(t, k, preferenceTriage())
			s := b.setting(t)
			s.Delegations = []curate.Delegation{{ID: tc.args.initial, Scope: *acme("svc")}}
			if tc.args.files {
				s.Anchor = curate.NewAnchor(b.all(t), []curate.AnchorFile{{Path: "rules.md", HMAC: hmac}}, nil)
			}
			if tc.args.paused {
				b.knobs(t, func(k *curate.Knobs) { k.DelegationPaused = []string{grantHash(t, tc.args.initial, *acme("svc"))} })
			}
			if tc.args.orphan {
				typed := preferenceTriage()
				typed.Source = classify.SourceComplete
				d := curate.Decision{Tier: 1, RuleCode: curate.RuleDelegated, Action: curate.ActionApply, Triage: typed,
					Delegation: grantHash(t, tc.args.initial, *acme("svc"))}
				x, err := decided("d-orphan", 0).Decide(
					curate.Change{Kind: curate.ChangeAdd, ID: k.ID, Version: 1, Extract: k.Extract, Anchor: 1}, d, nil, written)
				require.NoError(t, err)
				require.NoError(t, b.stores.Traces.Append(ctx, x.Record()))
			} else {
				_, err := b.triageCurator().Pass(ctx, s)
				require.NoError(t, err)
			}
			s.Delegations = []curate.Delegation{{ID: tc.args.next, Scope: *acme("svc")}}

			got, err := b.triageCurator().Pass(ctx, s)

			require.NoError(t, err)
			assert.Len(t, got.Decided, tc.want)
			latest, err := b.all(t).Version(k.ID, 1)
			require.NoError(t, err)
			assert.Equal(t, tc.args.next != "" && !tc.args.files, latest.Status == knowledge.StatusApproved)
		})
	}
}

// An update is never delegable so a grant over its scope never reopens it at every pass
func TestCuratorPassDelegationUpdate(t *testing.T) {
	tcs := []struct {
		name string
		args []curate.Delegation
		want int
	}{
		{"no grant", nil, 0},
		{"a grant over the scope of the update", []curate.Delegation{{ID: "g1", Scope: *acme("svc")}}, 0},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			k := lesson("k-pref", 2, "Keep answers short", acme("svc"), knowledge.StatusCandidate, "")
			k.Extract, k.Base = "e1", 1
			b := newTriageBench(t, k)
			first := lesson("k-pref", 1, "Keep answers brief", acme("svc"), knowledge.StatusApproved, knowledge.ApproverNodloop)
			first.Time, first.ApprovedAt = written.Add(-time.Hour), written.Add(-time.Hour)
			require.NoError(t, testkit.Err(b.stores.Ledger.Import(ctx, []knowledge.Knowledge{first})))
			f, err := feedback.NewCaptured("r-k-pref", feedback.VerdictReject, feedback.ReasonForm,
				"Always keep answers short", written.Add(time.Minute))
			require.NoError(t, err)
			require.NoError(t, b.stores.Feedback.Append(ctx, f.Record()))
			b.triage(t, k, preferenceTriage())
			s := b.setting(t)
			s.Delegations = tc.args
			decided, err := b.triageCurator().Pass(ctx, s)
			require.NoError(t, err)
			require.Len(t, decided.Decided, 1)
			require.Empty(t, decided.Decided[0].Decision.Delegation)

			got, err := b.triageCurator().Pass(ctx, s)

			require.NoError(t, err)
			assert.Len(t, got.Decided, tc.want)
		})
	}
}

func TestCuratorPassDelegationRevoked(t *testing.T) {
	type args struct {
		choice  trace.Choice
		reverse bool
		restore bool
		agent   bool
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"a terminal undo stops its grant", args{choice: trace.ChoiceUndo}, true},
		{"a later restore keeps the grant stopped", args{choice: trace.ChoiceUndo, restore: true}, true},
		{"reversing an undo keeps the grant stopped", args{choice: trace.ChoiceUndo, reverse: true}, true},
		{"reversing a keep leaves the grant active", args{choice: trace.ChoiceKeep, reverse: true}, false},
		{"an agent proposal stops no grant", args{choice: trace.ChoiceUndo, agent: true}, false},
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
			b.triage(t, k, preferenceTriage())
			s := b.setting(t)
			s.Delegations = []curate.Delegation{{ID: "g1", Scope: *acme("svc")}}
			first, err := b.triageCurator().Pass(ctx, s)
			require.NoError(t, err)
			require.Len(t, first.Decided, 1)
			require.Equal(t, curate.ActionApply, first.Decided[0].Decision.Action)
			iv := curate.NewIntervener(b.stores.Ledger, b.stores.Traces, curate.InterveneSetting{}, b.stores.Clock.Now)
			by := curate.Submitter{Proof: trace.ProofTerminal, Person: "ann", Source: trace.SourceCommand}
			if tc.args.agent {
				by.Proof = trace.ProofNone
			}
			made, err := iv.Intervene(ctx, curate.Request{Action: trace.ActionJudge, Target: "k-pref:1", Choice: tc.args.choice}, by)
			require.NoError(t, err)
			if tc.args.reverse {
				_, err = iv.Intervene(ctx, curate.Request{Action: trace.ActionReverse, Target: made.Trace.ID}, by)
				require.NoError(t, err)
			}
			if tc.args.restore {
				_, err = iv.Intervene(ctx, curate.Request{Action: trace.ActionJudge, Target: "k-pref:1", Choice: trace.ChoiceKeep}, by)
				require.NoError(t, err)
			}

			got, err := b.triageCurator().Pass(ctx, s)

			require.NoError(t, err)
			grant := grantHash(t, "g1", *acme("svc"))
			assert.Equal(t, tc.want, slices.Contains(got.Event.Knobs.DelegationPaused, grant))
			stops := slices.ContainsFunc(got.Event.Changes, func(c curate.KnobChange) bool {
				return c.Rule == curate.KnobDelegation && c.Grant == grant
			})
			assert.Equal(t, tc.want, stops)
			again, err := b.triageCurator().Pass(ctx, s)
			require.NoError(t, err)
			assert.Equal(t, got.Event, again.Event, "a stopped grant is recorded once")
		})
	}
}

func TestCuratorPassDelegationReadFailures(t *testing.T) {
	type args struct {
		gets     testkit.Reads
		appends  testkit.Reads
		feedback testkit.Reads
		outside  bool
	}
	tcs := []struct {
		name string
		args args
	}{
		{"the extraction cannot be read", args{gets: testkit.Reads{Err: assert.AnError}}},
		{"the original run cannot be read", args{gets: testkit.Reads{Allowed: 2, Err: assert.AnError}, outside: true}},
		{"the direct correction cannot be read", args{feedback: testkit.Reads{Allowed: 1, Err: assert.AnError}}},
		{"the decision cannot be recorded", args{appends: testkit.Reads{Allowed: 1, Err: assert.AnError}}},
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
			b.triage(t, k, preferenceTriage())
			s := b.setting(t)
			s.Delegations = []curate.Delegation{{ID: "g1", Scope: *acme("svc")}}
			if tc.args.outside {
				s.Window = written.Add(time.Hour)
			}
			traces := &testkit.FlakyTraces{Store: b.stores.Traces, Gets: tc.args.gets, Appends: tc.args.appends}
			verdicts := &testkit.FlakyFeedback{Store: b.stores.Feedback, Reads: tc.args.feedback}
			c := curate.New(b.stores.Ledger, traces, verdicts, b.client, nil, seal, b.stores.Clock.Now)

			got, err := c.Pass(ctx, s)

			assert.ErrorIs(t, err, assert.AnError)
			assert.Empty(t, got.Decided)
			latest, readErr := b.all(t).Version(k.ID, 1)
			require.NoError(t, readErr)
			assert.Equal(t, knowledge.StatusCandidate, latest.Status)
		})
	}
}

func TestCuratorPassTriageProtectsActiveJudge(t *testing.T) {
	tcs := []struct {
		name string
		args *curate.Triage
	}{
		{"code style with no protection label", &curate.Triage{Triage: classify.Triage{Policy: classify.TriagePolicy,
			Nature: classify.NaturePreference, Reversibility: classify.ReversibilityM, Source: classify.SourceMissing,
			Protected: classify.ProtectionNone, Lasting: true, Faithful: true}}},
		{"external effect with no protection label", &curate.Triage{Triage: classify.Triage{Policy: classify.TriagePolicy,
			Nature: classify.NaturePreference, Reversibility: classify.ReversibilityL, Source: classify.SourceMissing,
			Protected: classify.ProtectionNone, Lasting: true, Faithful: true}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			k := lesson("k-pref", 1, "Keep code comments short", acme("acme"), knowledge.StatusCandidate, "")
			k.Extract = "e1"
			b := newTriageBench(t, k)
			b.client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(
				llm.Response{Output: answer(0.99, 0.01), Model: "claude-sonnet-5-5"}, nil).AnyTimes()
			f, err := feedback.NewCaptured("r-k-pref", feedback.VerdictReject, feedback.ReasonForm,
				"Always keep code comments short", written.Add(time.Minute))
			require.NoError(t, err)
			require.NoError(t, b.stores.Feedback.Append(ctx, f.Record()))
			b.triage(t, k, tc.args)
			b.knobs(t, func(k *curate.Knobs) { k.Activated = true })
			s := b.setting(t)
			s.Delegations = []curate.Delegation{{ID: "g1", Scope: *acme("acme")}}

			got, err := b.triageCurator().Pass(ctx, s)

			require.NoError(t, err)
			require.Len(t, got.Decided, 1)
			assert.Equal(t, curate.ActionHold, got.Decided[0].Decision.Action)
			assert.Equal(t, curate.RuleTriageProtected, got.Decided[0].Decision.RuleCode)
			assert.Empty(t, got.Decided[0].Decision.Judge)
			latest, err := b.all(t).Version(k.ID, 1)
			require.NoError(t, err)
			assert.Equal(t, knowledge.StatusCandidate, latest.Status)
		})
	}
}

func TestCuratorPassDelegationScope(t *testing.T) {
	broad := acme("svc")
	broad.Labels["repo"] = []string{"svc", "other"}
	other := acme("svc")
	other.Producer = "other"
	excluded := acme("svc")
	excluded.Except = trace.Labels{"task": {"docs"}}
	specific := acme("svc", "task", "code")
	host := acme("svc", "host", "example")
	correlation := acme("svc", "correlation", "C1")
	tcs := []struct {
		name  string
		scope *knowledge.RunScope
		grant knowledge.RunScope
		want  curate.Action
	}{
		{"a candidate wider than its direct source", broad, *broad, curate.ActionHold},
		{"another source producer", other, *other, curate.ActionHold},
		{"an excluded task kept by the candidate", excluded, *excluded, curate.ActionApply},
		{"an exclusion missing from the candidate", acme("svc"), *excluded, curate.ActionHold},
		{"a label that cannot reach the excluded task", specific, *excluded, curate.ActionApply},
		{"a grant label the candidate leaves open", acme("svc"), *specific, curate.ActionHold},
		{"a host scope cannot become a delegation", host, *host, curate.ActionHold},
		{"a correlation scope cannot become a delegation", correlation, *correlation, curate.ActionHold},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			k := lesson("k-pref", 1, "Keep answers short", tc.scope, knowledge.StatusCandidate, "")
			k.Extract = "e1"
			b := newTriageBench(t, k)
			f, err := feedback.NewCaptured("r-k-pref", feedback.VerdictReject, feedback.ReasonForm,
				"Always keep answers short", written.Add(time.Minute))
			require.NoError(t, err)
			require.NoError(t, b.stores.Feedback.Append(ctx, f.Record()))
			b.triage(t, k, preferenceTriage())
			s := b.setting(t)
			s.Delegations = []curate.Delegation{{ID: "g1", Scope: tc.grant}}

			got, err := b.triageCurator().Pass(ctx, s)

			require.NoError(t, err)
			require.Len(t, got.Decided, 1)
			assert.Equal(t, tc.want, got.Decided[0].Decision.Action)
		})
	}
}
