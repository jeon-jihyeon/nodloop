package knowledge_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/call"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

var cueAt = time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

// A candidate judgment of repo acme with the version, base, content and cue given
func cueCandidate(version, base int, content string, cue *knowledge.Cue) knowledge.Knowledge {
	return knowledge.Knowledge{
		ID: "k1", Version: version, Base: base, Kind: knowledge.KindJudgment, Content: content,
		Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}}},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: cueAt, Cue: cue,
	}
}

func cueLedger(t *testing.T, records ...knowledge.Knowledge) *knowledge.Ledger {
	t.Helper()
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	l := knowledge.NewLedger(store, vetofile.NewApprovedFile(t.TempDir(), "records"), func() time.Time { return cueAt },
		func(prefix string) string { return prefix + "new" })
	require.NoError(t, testkit.Err(l.Import(context.Background(), records)))
	return l
}

func TestCueFits(t *testing.T) {
	shapes := call.Shapes{{Tool: "Bash", Command: "gh pr create"}, {Tool: "Edit", File: "*_test.go"}}
	tcs := []struct {
		name string
		args knowledge.Cue
		want bool
	}{
		{"a prefix of the command", knowledge.Cue{Tool: "Bash", Command: "gh pr"}, true},
		{"the whole command", knowledge.Cue{Tool: "Bash", Command: "gh pr create"}, true},
		{"another command", knowledge.Cue{Tool: "Bash", Command: "git commit"}, false},
		{"a tool alone", knowledge.Cue{Tool: "Edit"}, true},
		{"a plain shape fits a test file", knowledge.Cue{Tool: "Edit", File: "*.go"}, true},
		{"a test shape", knowledge.Cue{Tool: "Edit", File: "*_test.go"}, true},
		{"another file", knowledge.Cue{Tool: "Edit", File: "*.md"}, false},
		{"a file on a call with none", knowledge.Cue{Tool: "Bash", File: "*.go"}, false},
		{"another tool", knowledge.Cue{Tool: "Write"}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Fits(shapes))
		})
	}
}

func TestSetCued(t *testing.T) {
	commit := cueCandidate(1, 0, "keep a commit title short", &knowledge.Cue{Tool: "Bash", Command: "git commit"})
	plain := cueCandidate(1, 0, "answer in Korean", nil)
	plain.ID = "k2"
	test := cueCandidate(1, 0, "name a test after its behavior", &knowledge.Cue{Tool: "Edit", File: "*_test.go"})
	test.ID = "k3"
	s := knowledge.Set{commit, plain, test}
	assert.Equal(t, knowledge.Set{commit, test}, s.Cued())
	assert.Equal(t, knowledge.Set{plain}, s.Uncued())
	assert.Equal(t, knowledge.Set{test}, s.Fitting(call.Shapes{{Tool: "Edit", File: "*_test.go"}}))
	assert.Equal(t, knowledge.Set{}, s.Fitting(call.Shapes{{Tool: "Bash", Command: "git status"}}))
}

func TestCueText(t *testing.T) {
	k := cueCandidate(
		1,
		0,
		"keep a commit title short",
		&knowledge.Cue{Tool: "Bash", Command: "git commit", File: "*.md"},
	)
	assert.True(t, strings.HasSuffix(k.Text(), "Cue: before each Bash call running git commit on a *.md file\n"))
	assert.Equal(t, k.Text(), k.PromptLine(), "a prompt that still carries it shows when it applies")
	assert.Equal(t, "before each Edit call", knowledge.Cue{Tool: "Edit"}.String())
}

func TestLedgerApproveCue(t *testing.T) {
	commit := &knowledge.Cue{Tool: "Bash", Command: "git commit"}
	approved := func(k knowledge.Knowledge) knowledge.Knowledge {
		k.Status, k.Approver, k.ApprovedAt = knowledge.StatusApproved, "ann", cueAt
		return k
	}
	type args struct {
		v1 *knowledge.Cue
		v2 *knowledge.Cue
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a version that keeps the cue", args{commit, &knowledge.Cue{Tool: "Bash", Command: "git commit"}}, nil},
		{"a version that drops the cue", args{commit, nil}, knowledge.ErrCueChanged},
		{
			"a version that changes the cue",
			args{commit, &knowledge.Cue{Tool: "Bash", Command: "git push"}},
			knowledge.ErrCueChanged,
		},
		{"a version that adds a cue to an uncued item", args{nil, commit}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := cueLedger(t, cueCandidate(1, 0, "keep a commit title short", tc.args.v1),
				approved(cueCandidate(1, 0, "keep a commit title short", tc.args.v1)),
				cueCandidate(2, 1, "keep a commit title under fifty characters", tc.args.v2))
			assert.ErrorIs(t, testkit.Err(l.Approve(context.Background(), "k1", 2, "ann")), tc.want)
		})
	}
}

// A cued judgment still counts toward its folder since a plugin without the tool hook adds it with the prompt
// and no compaction takes it since a merge would drop when it arrives
func TestLedgerApproveCueInTheFolder(t *testing.T) {
	// Each fits alone and two would pass the cap
	big := strings.Repeat("a long lesson ", knowledge.RunChars*2/3/14)
	full := cueCandidate(1, 0, big, nil)
	full.ID = "full"
	cued := cueCandidate(1, 0, big, &knowledge.Cue{Tool: "Bash", Command: "git commit"})
	l := cueLedger(t, full, cued)
	ctx := context.Background()
	require.NoError(t, testkit.Err(l.Approve(ctx, "full", 1, "ann")))
	assert.ErrorIs(
		t,
		testkit.Err(l.Approve(ctx, "k1", 1, "ann")),
		knowledge.ErrFolderFull,
		"a cued item fills the folder like any other",
	)
	all, err := l.All(ctx)
	require.NoError(t, err)
	assert.Contains(
		t,
		all.ForAt("session", trace.Labels{"repo": {"acme"}}, cueAt).Refs(),
		knowledge.Ref{ID: "full", Version: 1},
	)
}

func TestSetCompactableLeavesCuesOut(t *testing.T) {
	approved := func(id string, cue *knowledge.Cue) knowledge.Knowledge {
		k := cueCandidate(1, 0, "lesson "+id, cue)
		k.ID, k.Status, k.Approver, k.ApprovedAt = id, knowledge.StatusApproved, "ann", cueAt
		return k
	}
	commit := &knowledge.Cue{Tool: "Bash", Command: "git commit"}
	s := knowledge.Set{approved("a", nil), approved("b", nil), approved("c", commit)}
	got, err := s.Compactable("a")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, ids(got.Items))
	got, err = s.Compactable("c")
	require.NoError(t, err)
	assert.Equal(t, []string{"c"}, ids(got.Items), "a cued anchor has nothing to compact")
}

func ids(s knowledge.Set) []string {
	out := []string{}
	for _, k := range s {
		out = append(out, k.ID)
	}
	return out
}

// The calls of an answered run are those of its own turn and those its continuation made after it
// They stay so whether or not retention removed the turn traces between them
func TestTurnCalls(t *testing.T) {
	at := func(ms int) time.Time { return cueAt.Add(time.Duration(ms) * time.Millisecond) }
	calls := func(ms, turn int, command string) trace.Trace {
		in := call.Input{At: at(ms), Calls: call.Shapes{{Tool: "Bash", Command: command}}}
		if turn >= 0 {
			in.Turn = at(turn)
		}
		raw, err := json.Marshal(in)
		require.NoError(t, err)
		return trace.Trace{Name: trace.NameCalls, SessionID: "s1", Time: at(ms), Input: raw}
	}
	run := func(ms, durationMS int, continued bool) trace.Trace {
		input := `{"applied":[]}`
		if continued {
			input = `{"applied":[],"continued":true}`
		}
		return trace.Trace{
			Name:       trace.NameRun,
			SessionID:  "s1",
			Time:       at(ms),
			DurationMS: int64(durationMS),
			Input:      json.RawMessage(input),
		}
	}
	turn := func(ms int) trace.Trace { return trace.Trace{Name: trace.NameTurn, SessionID: "s1", Time: at(ms)} }
	// The answer at 10000 ms of a turn that began at 3000 ms
	// It is measured from the turn in whole milliseconds
	answer := run(10000, 7000, false)
	earlier, continued, later := run(0, 0, false), run(12000, 2000, true), run(30000, 5000, false)
	interrupted := calls(2000, 1000, "git commit")
	own := calls(4000, 3000, "git push")
	continuation := calls(11000, 3000, "gh pr create")
	next := calls(26000, 25000, "git tag")
	push, pr := call.Shape{Tool: "Bash", Command: "git push"}, call.Shape{Tool: "Bash", Command: "gh pr create"}
	type args struct {
		run     trace.Trace
		session trace.Traces
	}
	tcs := []struct {
		name string
		args args
		want call.Shapes
	}{
		{"with the turn traces the calls of the turn join, the continuation after the answer included",
			args{
				answer,
				trace.Traces{
					earlier,
					turn(1000),
					interrupted,
					turn(3000),
					own,
					answer,
					continuation,
					continued,
					turn(25000),
					next,
					later,
				},
			},
			call.Shapes{push, pr}},
		{"once retention removed the turn traces the duration tells the turn",
			args{
				answer,
				trace.Traces{earlier, interrupted, own, answer, continuation, continued, next, later},
			}, call.Shapes{push, pr}},
		{"a turn read from a duration within the slack still joins",
			args{run(10000, 6999, false), trace.Traces{earlier, own, continuation}}, call.Shapes{push, pr}},
		{
			"a turn read past the slack is another turn",
			args{run(10000, 6990, false), trace.Traces{earlier, own, continuation}},
			nil,
		},
		{"calls that name no turn join by time from the turn of the run up to the next turn",
			args{
				answer,
				trace.Traces{
					earlier,
					calls(2000, -1, "git commit"),
					calls(4000, -1, "git push"),
					answer,
					calls(11000, -1, "gh pr create"),
					turn(25000),
					calls(26000, -1, "git tag"),
				},
			},
			call.Shapes{push, pr}},
		{"calls that name no turn stop where the next answer's turn began",
			args{
				answer,
				trace.Traces{
					earlier,
					calls(4000, -1, "git push"),
					answer,
					calls(11000, -1, "gh pr create"),
					calls(26000, -1, "git tag"),
					later,
				},
			},
			call.Shapes{push, pr}},
		{
			"a run with no duration and no turn trace joins calls by time",
			args{run(10000, 0, false), trace.Traces{earlier, own}},
			call.Shapes{push},
		},
		{"the slack never moves the bound of the answer before backward",
			args{
				run(10, 7, false),
				trace.Traces{run(2, 0, false), calls(1, -1, "git commit"), calls(4, -1, "git push")},
			}, call.Shapes{push}},
		{"a turn whose prompt left no trace takes its calls by time and the answer before never takes them",
			args{
				run(20000, 0, false),
				trace.Traces{turn(3000), own, answer, calls(16000, 15000, "git tag")},
			}, call.Shapes{{Tool: "Bash", Command: "git tag"}}},
		{"the answer before a turn that left no trace keeps only its own calls",
			args{
				answer,
				trace.Traces{turn(3000), own, answer, calls(16000, 15000, "git tag"), run(20000, 0, false)},
			}, call.Shapes{push}},
		{
			"a run of another session joins nothing",
			args{trace.Trace{Name: trace.NameRun, SessionID: "s2", Time: at(10000)}, trace.Traces{own}},
			nil,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, knowledge.TurnCalls(tc.args.run, tc.args.session))
		})
	}
}

// A folder full of a cued item and two others still offers the compaction of the two
func TestSetRoomPassesACuedAnchor(t *testing.T) {
	approved := func(id, content string, cue *knowledge.Cue, labels trace.Labels) knowledge.Knowledge {
		k := cueCandidate(1, 0, content, cue)
		k.ID, k.Status, k.Approver, k.ApprovedAt = id, knowledge.StatusApproved, "ann", cueAt
		k.Run.Labels = labels
		return k
	}
	third := strings.Repeat("a long lesson ", knowledge.RunChars/3/14)
	both := trace.Labels{"repo": {"acme"}, "dir": {"."}}
	s := knowledge.Set{
		approved("cued", third, &knowledge.Cue{Tool: "Bash", Command: "git commit"}, both),
		approved("a", third, nil, trace.Labels{"repo": {"acme"}}),
		approved("b", third, nil, trace.Labels{"repo": {"acme"}}),
		cueCandidate(1, 0, third, nil),
	}
	s[3].ID = "new"
	room, err := s.Room("new", 1)
	require.NoError(t, err)
	assert.NotEmpty(t, room.Anchor)
	assert.NotEqual(t, "cued", room.Anchor)
}
