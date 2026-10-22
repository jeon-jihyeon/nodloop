package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// The Monday the first cycle of the curation records starts on
var curationMonday = time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)

// The HMAC a question shows its text as
var shownText = strings.Repeat("ab", 32)

// A record directory of curate events written through the constructors of curate as a pass and the hooks write them
type curationRecords struct {
	t       *testing.T
	records string
	traces  *tracefile.Store
	// The policy version of the knob event of each cycle by its id
	policies map[string]string
	// Spaces the records a second apart so each id keeps its time
	at time.Time
}

func newCurationRecords(t *testing.T) *curationRecords {
	t.Helper()
	records := t.TempDir()
	traces, err := tracefile.New(records)
	require.NoError(t, err)
	return &curationRecords{t: t, records: records, traces: traces, policies: map[string]string{}, at: curationMonday.Add(10 * time.Hour)}
}

func (r *curationRecords) append(tr trace.Trace, err error) trace.Trace {
	r.t.Helper()
	require.NoError(r.t, err)
	require.NoError(r.t, r.traces.Append(context.Background(), tr.Record()))
	return tr
}

// The time of the next record in the cycle of the week
func (r *curationRecords) next(week int) time.Time {
	r.at = r.at.Add(time.Second)
	return r.at.AddDate(0, 0, 7*week)
}

// The knob event of the start of the cycle with the start knobs, tier 1 active and the judge at the effort since the judge start
func (r *curationRecords) knob(week int, effort string, judgeStart time.Time) {
	judge := curate.JudgeConfig{Model: "claude-sonnet-5-5", Effort: effort, Prompt: curate.PromptVersion, Start: judgeStart, Sets: make([][]knowledge.Ref, 3)}
	r.knobEvent(week, judge, func(k *curate.Knobs) { k.Activated = true })
}

// The knob event of the start of the cycle with the start knobs the change gives under the default judge a pass keeps
func (r *curationRecords) knobs(week int, change func(*curate.Knobs)) {
	r.knobEvent(week, curate.JudgeConfig{}.Next(curate.DefaultJudge, nil, "", curationMonday), change)
}

func (r *curationRecords) knobEvent(week int, judge curate.JudgeConfig, change func(*curate.Knobs)) {
	start := curationMonday.AddDate(0, 0, 7*week)
	knobs := curate.StartKnobs()
	change(&knobs)
	event := curate.KnobEvent{CycleID: start.Format(trace.CycleDate), Start: start, Seed: "5eed", Knobs: knobs, Cause: curate.KnobStart, Judge: judge}
	r.policies[event.CycleID] = event.Policy("0.10.0")
	r.append(event.Trace("0.10.0", start.Add(9*time.Hour)))
}

// The add of the lesson every decide but a drop judges
var addLesson = curate.Change{Kind: curate.ChangeAdd, ID: "k-1", Version: 1, Anchor: 1}

// A decide of the change in the cycle with the decision drawn for audit at p
func (r *curationRecords) decide(week int, c curate.Change, d curate.Decision, p float64) (trace.Trace, curate.Event) {
	at := r.next(week)
	cycle := curationMonday.AddDate(0, 0, 14*(week/2)).Format(trace.CycleDate)
	// The shape of a trace id with a suffix of the time so the audit draw on it reads the same in every run
	id := fmt.Sprintf("%012x%08x", at.UnixMilli(), at.Unix())
	event := curate.Event{ID: id, Ref: "run-1", Source: trace.SourcePush, Draw: trace.NewDraw(cycle, r.policies[cycle], "5eed", id, "audit", p)}
	return r.append(event.Decide(c, d, nil, at)), event
}

// A change tier 1 carried out in b1 asked as an audit at p and answered with the choice by the person
func (r *curationRecords) audit(week int, p float64, choice curate.Choice) {
	share, confidence := 1.0, 0.97
	decide, event := r.decide(week, addLesson, curate.Decision{
		Tier: 1, Judge: "c-1", Outcome: curate.OutcomeConform, Vote: &share, Bin: curate.Bin1, Confidence: &confidence, Action: curate.ActionApply,
	}, p)
	asked := curate.Event{Session: "s1", Ref: decide.ID, Source: trace.SourcePush, Draw: *decide.Draw}
	change := &knowledge.Ref{ID: "k-1", Version: 1}
	expose := r.append(asked.Expose(curate.Exposure{
		Kind: curate.QuestionAudit, Key: "a7k2", Change: change, Surface: curate.SurfaceContext, Rules: curate.QuestionRules,
		Choices: []curate.Choice{curate.ChoiceSame, curate.ChoiceUndo}, Text: shownText, Lines: []string{shownText}, Round: 1, Run: "run-1", Runes: 180,
	}, r.next(week)))
	answered := curate.Event{Session: "s1", Ref: expose.ID, Source: trace.SourcePush, Draw: event.Draw}
	r.append(answered.Answer(curate.Answer{
		Choice: choice, Decide: decide.ID, Change: change, Hidden: true, Overturn: choice == curate.ChoiceUndo, Proof: trace.ProofReply, Person: "ann",
	}, curate.AnswerOutput{}, r.next(week)))
}

// A judgment the person asked for through the manual path that drops a change curate carried out
func (r *curationRecords) manual(week int) {
	share := 1.0
	decide, _ := r.decide(week, addLesson, curate.Decision{
		Tier: 1, Judge: "c-1", Outcome: curate.OutcomeConform, Vote: &share, Bin: curate.Bin1, Action: curate.ActionApply,
	}, 0.15)
	cycle := decide.Draw.CycleID
	asked := curate.Event{Session: "s2", Ref: decide.ID, Source: trace.SourceManual, Draw: trace.Draw{CycleID: cycle, PolicyVersion: r.policies[cycle]}}
	expose := r.append(asked.Expose(curate.Exposure{
		Kind: curate.QuestionDirection, Key: "m3n4", Change: &knowledge.Ref{ID: "k-1", Version: 1}, Surface: curate.SurfaceContext,
		Rules: curate.QuestionRules, Choices: []curate.Choice{curate.ChoiceKeep, curate.ChoiceDrop}, Text: shownText, Lines: []string{shownText}, Round: 1, Runes: 240,
	}, r.next(week)))
	answered := asked
	answered.Ref = expose.ID
	r.append(answered.Answer(curate.Answer{
		Choice: curate.ChoiceDrop, Decide: decide.ID, Overturn: true, Proof: trace.ProofTerminal, Person: "ann",
	}, curate.AnswerOutput{Records: []trace.Written{{ID: "k-1", Version: 1, Status: "retired"}}}, r.next(week)))
}

// A change the judge members split on prepared as a question for the person
func (r *curationRecords) split(week int) {
	share := 1.0 / 3
	r.decide(week, addLesson, curate.Decision{
		Tier: 2, RuleCode: curate.RuleJudgeSplit, Judge: "c-2", Outcome: curate.OutcomeSplit, Vote: &share, Action: curate.ActionAsk,
	}, 0.15)
}

// A tier 1 decision in shadow the stop picked at p_select in session s3 and the person kept
func (r *curationRecords) shadow(week int, pSelect float64) {
	share, confidence := 1.0, 0.97
	decide, _ := r.decide(week, addLesson, curate.Decision{
		Tier: 1, Judge: "c-3", Outcome: curate.OutcomeConform, Vote: &share, Bin: curate.Bin1, Confidence: &confidence, Action: curate.ActionShadow,
	}, 0.15)
	cycle := decide.Draw.CycleID
	asked := curate.Event{
		Session: "s3", Ref: decide.ID, Source: trace.SourcePush, Draw: trace.NewDraw(cycle, r.policies[cycle], "5eed", "s3", "shadow", pSelect),
	}
	expose := r.append(asked.Expose(curate.Exposure{
		Kind: curate.QuestionShadow, Key: "s5t6", Change: &knowledge.Ref{ID: "k-1", Version: 1}, Surface: curate.SurfaceContext,
		Rules: curate.QuestionRules, Choices: []curate.Choice{curate.ChoiceKeep, curate.ChoiceDrop}, Text: shownText, Lines: []string{shownText}, Round: 1, Runes: 210,
	}, r.next(week)))
	answered := asked
	answered.Ref = expose.ID
	r.append(answered.Answer(curate.Answer{Choice: curate.ChoiceKeep, Decide: decide.ID, Hidden: true, Proof: trace.ProofReply, Person: "ann"},
		curate.AnswerOutput{}, r.next(week)))
}

// A change the judge found conform in a bin tier 1 does not carry out yet asked as a direction question the person answered with the choice
func (r *curationRecords) direction(week int, bin curate.Bin, choice curate.Choice) {
	share, confidence := 1.0, 0.9
	decide, _ := r.decide(week, addLesson, curate.Decision{
		Tier: 2, RuleCode: curate.RuleKindClosed, Judge: "c-4", Outcome: curate.OutcomeConform, Vote: &share, Bin: bin, Confidence: &confidence,
		Action: curate.ActionAsk,
	}, 0)
	cycle := decide.Draw.CycleID
	asked := curate.Event{
		Session: "s4", Ref: decide.ID, Source: trace.SourcePush, Draw: trace.NewDraw(cycle, r.policies[cycle], "5eed", "s4", "direction", 1),
	}
	expose := r.append(asked.Expose(curate.Exposure{
		Kind: curate.QuestionDirection, Key: "d7e8", Change: &knowledge.Ref{ID: "k-1", Version: 1}, Surface: curate.SurfaceContext,
		Rules: curate.QuestionRules, Choices: []curate.Choice{curate.ChoiceKeep, curate.ChoiceDrop}, Text: shownText, Lines: []string{shownText}, Round: 1, Runes: 220,
	}, r.next(week)))
	answered := asked
	answered.Ref = expose.ID
	r.append(answered.Answer(curate.Answer{Choice: choice, Decide: decide.ID, Proof: trace.ProofReply, Person: "ann"}, curate.AnswerOutput{}, r.next(week)))
}

// A candidate tier 0 dropped as a duplicate and drew for audit at p 1
func (r *curationRecords) drop(week int) {
	r.decide(week, curate.Change{Kind: curate.ChangeDrop, ID: "k-2", Version: 1, Anchor: 1}, curate.Decision{
		Tier: 0, RuleCode: curate.RuleSameAsItem, Action: curate.ActionDrop, Reason: knowledge.ReasonDuplicate,
		Failure: curate.FailureHard, Reopen: curate.ReopenPersonOnly,
	}, 1)
}

// report curation as one JSON value
func (r *curationRecords) report(t *testing.T, now time.Time, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	getenv := func(k string) string { return map[string]string{envRecordDir: r.records}[k] }
	code := runReport(append([]string{"curation"}, args...), getenv, func() time.Time { return now }, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// The acceptance of the statistics over records the curate constructors wrote
// 1. 43 answers with no differ meet the anytime valid bound, 42 do not, and 66 with one differ meet it
// 2. a manual answer is left out of every estimate and the bound and counted as an overturn
// 3. a shift of the judge with the same configuration keeps the bound and starts the posterior of its bin again
func TestCuration(t *testing.T) {
	type args struct {
		cycles int
		fill   func(r *curationRecords)
	}
	type want struct {
		same, differ, posterior int
		e                       float64
		met                     bool
		answered                int
		manual, overturned      int
		shift, changed          bool
	}
	audits := func(n, week int, choice curate.Choice) func(r *curationRecords) {
		return func(r *curationRecords) {
			for range n {
				r.audit(week, 0.15, choice)
			}
		}
	}
	all := func(fills ...func(r *curationRecords)) func(r *curationRecords) {
		return func(r *curationRecords) {
			for _, fill := range fills {
				fill(r)
			}
		}
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"43 answers with no differ meet the bound", args{1, audits(43, 0, curate.ChoiceSame)}, want{43, 0, 43, 20.887535, true, 43, 0, 0, false, false}},
		{"42 answers with no differ do not", args{1, audits(42, 0, curate.ChoiceSame)}, want{42, 0, 42, 19.215032, false, 42, 0, 0, false, false}},
		{
			"66 answers with one differ meet the bound",
			args{1, all(audits(65, 0, curate.ChoiceSame), audits(1, 0, curate.ChoiceUndo))},
			want{65, 1, 66, 21.157022, true, 66, 0, 0, false, false},
		},
		{
			"a manual answer is left out of every estimate and the bound and counted as an overturn",
			args{1, all(audits(42, 0, curate.ChoiceSame), func(r *curationRecords) { r.manual(0) })},
			want{42, 0, 42, 19.215032, false, 42, 1, 1, false, false},
		},
		{
			"a shift with the same configuration keeps the bound and starts the posterior of its bin again",
			args{2, all(audits(40, 0, curate.ChoiceSame), audits(3, 2, curate.ChoiceSame), func(r *curationRecords) {
				for range 10 {
					r.split(2)
				}
			})},
			want{43, 0, 3, 20.887535, true, 3, 0, 0, true, false},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newCurationRecords(t)
			for c := range tc.args.cycles {
				r.knob(2*c, curate.JudgeEffort, curationMonday)
			}
			tc.args.fill(r)

			code, stdout, stderr := r.report(t, curationMonday.AddDate(0, 0, 14*tc.args.cycles-1), "--json")

			require.Equal(t, 0, code, stderr)
			var got loop.CurationReport
			require.NoError(t, json.Unmarshal([]byte(stdout), &got))
			i := slices.IndexFunc(got.Kinds, func(k loop.KindRow) bool { return k.Kind == "add" && k.Bin == "b1" })
			require.GreaterOrEqual(t, i, 0)
			row := got.Kinds[i]
			require.NotEmpty(t, got.Estimates)
			estimate := got.Estimates[len(got.Estimates)-1]
			shifts := append([]loop.ShiftRow{{}}, got.Shift...)
			shift := shifts[len(shifts)-1]
			assert.Equal(t, tc.want, want{
				row.Same, row.Differ, row.PosteriorAnswers, math.Round(*row.E*1e6) / 1e6, row.BoundMet, estimate.Answered,
				got.Overturns.Manual, got.Overturns.ManualOverturned, shift.Shift, shift.Changed,
			})
		})
	}
}

// The text of report curation opens with the window and prints one line per row a person reads
func TestRunReportCuration(t *testing.T) {
	ctx := context.Background()
	r := newCurationRecords(t)
	r.knob(0, curate.JudgeEffort, curationMonday)
	r.knob(2, curate.JudgeEffort, curationMonday)
	r.audit(0, 0.15, curate.ChoiceSame)
	r.audit(0, 0.3, curate.ChoiceUndo)
	r.manual(0)
	r.shadow(0, 0.5)
	r.drop(0)
	r.audit(2, 0.15, curate.ChoiceSame)
	r.split(2)
	for range 3 {
		r.audit(2, 0.15, curate.ChoiceUndo)
	}
	items, err := knowledgefile.New(r.records)
	require.NoError(t, err)
	require.NoError(t, items.Append(ctx, knowledge.Knowledge{
		ID: "k-1", Version: 1, Kind: knowledge.KindJudgment, Content: "c", Status: knowledge.StatusApproved, Approver: knowledge.ApproverNodloop,
		Run: &knowledge.RunScope{Producer: sessionProducer}, Time: curationMonday,
	}.Record()))
	held := knowledge.Ref{ID: "k-1", Version: 1}
	run, err := trace.NewRun(sessionProducer, "", nil, mustInput(t, knowledge.RunInput{
		Applied: []knowledge.Ref{held}, Eligible: []knowledge.Eligible{{Ref: held, PInclude: 0.9}}, Unit: "u1",
	}), []byte("answer"), curationMonday.AddDate(0, 0, 15))
	r.append(run, err)

	code, stdout, stderr := r.report(t, curationMonday.AddDate(0, 0, 27))

	require.Equal(t, 0, code, stderr)
	policy := r.policies["2026-09-21"]
	assert.Equal(t, strings.Join([]string{
		"window\tall",
		"cycle\t2026-09-21\t2026-09-21T00:00:00Z to 2026-10-05T00:00:00Z\tclosed\tpolicy " + policy + "\tjudge claude-sonnet-5-5 high prompt " +
			curate.PromptVersion + "\tactive",
		"cycle\t2026-10-05\t2026-10-05T00:00:00Z to 2026-10-19T00:00:00Z\topen\tpolicy " + policy + "\tjudge claude-sonnet-5-5 high prompt " +
			curate.PromptVersion + "\tactive",
		"touchpoints\tcycle 2026-09-21\tquestions 4 in 3 sessions\taudit push 2, direction manual 1, shadow push 1\tnotices 0\tproposals 0\tanswers 4" +
			"\tno answer 0\tinterventions -\trunes median 195 max 240",
		"touchpoints\tcycle 2026-10-05\tquestions 4 in 1 sessions\taudit push 4\tnotices 0\tproposals 0\tanswers 4\tno answer 0\tinterventions -\trunes median 180 max 180",
		"touchpoints\tweek 2026-09-21\tquestions 4 in 3 sessions\taudit push 2, direction manual 1, shadow push 1\tnotices 0\tproposals 0\tanswers 4" +
			"\tno answer 0\tinterventions -\trunes median 195 max 240",
		"touchpoints\tweek 2026-10-05\tquestions 4 in 1 sessions\taudit push 4\tnotices 0\tproposals 0\tanswers 4\tno answer 0\tinterventions -\trunes median 180 max 180",
		"kind\tadd\tbin -\tdecided 1\tcarried 0\tasked 1\tdrawn 0 at -\texposed 0\tsame 0\tdiffer 0\tno answer 0" +
			"\tposterior 0.100 above 0.387 since 2026-09-21\tbound 1.000 e 1.000\topen no\tbound met no",
		"kind\tadd\tbin b1\tdecided 8\tcarried 7\tasked 0\tdrawn 0 at -\texposed 7\tsame 3\tdiffer 4\tno answer 0" +
			"\tposterior 0.294 above 0.983 since 2026-09-21\tbound 0.937 e 0.211\topen yes\tbound met no",
		"kind\tdrop\tbin -\tdecided 1\tcarried 1\tasked 0\tdrawn 1 at 1.00\texposed 0\tsame 0\tdiffer 0\tno answer 0" +
			"\tposterior 0.100 above 0.387 since 2026-09-21\tbound 1.000 e 1.000\topen yes\tbound met no",
		"calibration\tb1\tanswered 7\tsame 3\trate 0.429 [0.158, 0.750]",
		"calibration\tbrier 0.538 over 7",
		"shadow\tcycles 0",
		"shadow\tadd\tdecided 1\tasked 1 at 0.500 to 0.500\tanswered 1\tsame 1\tshare 1.000\tbound 0.974\tbound met no",
		"estimate\tcycle 2026-09-21\ttier 1\tanswered 3\tdiffer 1\tno answer 0\trate 0.278 [0.038, 0.791]\tkish 2.418\tworst 0.278",
		"estimate\tcycle 2026-10-05\ttier 1\tanswered 4\tdiffer 3\tno answer 0\trate 0.750 [0.301, 0.954]\tkish 4.000\tworst 0.750",
		"overturns\tdirection 0 of 0\tmanual 1 of 1\tundo 0\tpause 0",
		"repeat\tnodloop\titems 1\trepeat 0 of 0\tnamed repeat 0 of 0\tinferred repeat 0 of 0",
		"repeat\tperson\titems 0\trepeat 0 of 0\tnamed repeat 0 of 0\tinferred repeat 0 of 0",
		"cusum\tanswers 6\ts 1.099\talarms at 5\talarm yes\tnarrow add b1",
		"shift\tcycle 2026-10-05\tagainst 2026-09-21\tjudged 5 and 4\tp 0.343\tchanged no\tshift no\ttouched -",
		"item\tk-1\tv1\tunits 1\tincluded 1 judged 0 rate -\theld 0 judged 0 rate -\tdifference -",
	}, "\n")+"\n", stdout)
}

func mustInput(t *testing.T, in knowledge.RunInput) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(in)
	require.NoError(t, err)
	return b
}

// A report curation that cannot parse its flags, open its records or read them fails with nothing on stdout
func TestRunReportCurationFails(t *testing.T) {
	type args struct {
		args []string
		// The environment of the command with the temp directory of the test
		env func(t *testing.T) map[string]string
	}
	type want struct {
		code   int
		stderr string
	}
	records := func(t *testing.T) map[string]string { return map[string]string{envRecordDir: t.TempDir()} }
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an unknown flag fails", args{[]string{"--bogus"}, records}, want{1, "flag provided but not defined"}},
		{"an argument after the flags fails", args{[]string{"extra"}, records}, want{1, `unknown action "extra"`}},
		{"no record directory fails", args{nil, func(*testing.T) map[string]string { return nil }}, want{1, "set --record-dir"}},
		{
			"a config that does not decode fails",
			args{nil, func(t *testing.T) map[string]string {
				home := t.TempDir()
				require.NoError(t, os.MkdirAll(filepath.Join(home, ".nodloop"), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(home, ".nodloop", "config.json"), []byte("not json"), 0o600))
				return map[string]string{"HOME": home}
			}},
			want{1, "config"},
		},
		{
			"a corrupt record fails",
			args{[]string{"--json"}, func(t *testing.T) map[string]string {
				dir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "traces.jsonl"), []byte("not json\n"), 0o600))
				return map[string]string{envRecordDir: dir}
			}},
			want{1, "corrupt lines"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := tc.args.env(t)
			getenv := func(k string) string { return env[k] }
			var stdout, stderr bytes.Buffer

			code := runReport(append([]string{"curation"}, tc.args.args...), getenv, func() time.Time { return curationMonday }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code)
			assert.Empty(t, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
		})
	}
}

// A writer whose every write fails as a closed pipe does
type closedPipe struct{}

func (closedPipe) Write([]byte) (int, error) {
	return 0, os.ErrClosed
}

// report curation that cannot print its JSON fails
func TestRunReportCurationPrintFails(t *testing.T) {
	records := t.TempDir()
	getenv := func(k string) string { return map[string]string{envRecordDir: records}[k] }
	var stderr bytes.Buffer

	code := runReport([]string{"curation", "--json"}, getenv, func() time.Time { return curationMonday }, closedPipe{}, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), os.ErrClosed.Error())
}

// The numbers the knobs of curate move on as the report hands them over
func TestCurationNumbers(t *testing.T) {
	above, open := 0.736, loop.KindBin{Kind: "add", Bin: "b1"}
	closed := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	cycles := []loop.CurationCycle{{ID: "2026-09-21", Start: closed, Closed: true}, {ID: "2026-10-05", Start: closed.AddDate(0, 0, 14)}}
	tcs := []struct {
		name string
		args loop.CurationReport
		want curate.Numbers
	}{
		{"an empty report moves nothing", loop.CurationReport{}, curate.Numbers{}},
		{
			"a gauge per kind and bin with the alarm, the shift of the newest closed cycle and the shadow",
			loop.CurationReport{
				Cycles: cycles,
				Kinds: []loop.KindRow{
					{Kind: "add", Bin: "b1", Same: 1, Differ: 1, Above: &above},
					{Kind: "withdraw", Same: 43, BoundMet: true},
				},
				CUSUM:  loop.CUSUMReport{Alarm: true, Narrow: []loop.KindBin{open}},
				Shift:  []loop.ShiftRow{{Cycle: "2026-09-21", Shift: true}, {Cycle: "2026-10-05"}},
				Shadow: loop.ShadowReport{Cycles: 2, Kinds: []loop.ShadowRow{{Kind: "add", BoundMet: true}}},
			},
			curate.Numbers{
				Gauges: []curate.Gauge{
					{Opening: curate.Opening{Kind: curate.ChangeAdd, Bin: curate.Bin1}, Answers: 2, Differ: 1, Above: 0.736},
					{Opening: curate.Opening{Kind: curate.ChangeWithdraw}, Answers: 43, Met: true},
				},
				Alarmed: []curate.Opening{{Kind: curate.ChangeAdd, Bin: curate.Bin1}}, Shift: true, ShadowCycles: 2, ShadowMet: true,
			},
		},
		{
			"an alarm the pass already acted on and a shift of the open cycle move nothing",
			loop.CurationReport{
				Cycles: cycles, CUSUM: loop.CUSUMReport{Narrow: []loop.KindBin{}}, Shift: []loop.ShiftRow{{Cycle: "2026-10-05", Shift: true}},
				Shadow: loop.ShadowReport{Kinds: []loop.ShadowRow{{Kind: "update", BoundMet: true}}},
			},
			curate.Numbers{},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, curationNumbers(tc.args))
		})
	}
}
