package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// A Tuesday of the cycle that starts on Monday 2026-10-12
var curateNow = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

// A starter of a hook test that starts nothing
func noStart([]string) error {
	return nil
}

// The starter of a test that looks at other starts with the daily curate pass left out
// Every stop of a fresh record directory in a mode that infers starts it
func besideCurate(start starter) starter {
	return func(args []string) error {
		if slices.Equal(args, []string{"knowledge", "curate", "--daily"}) {
			return nil
		}
		return start(args)
	}
}

// A run of the place the label names that a person rejected an hour before the clock
func curateRun(t *testing.T, getenv func(string) string, label string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out.txt")
	require.NoError(t, os.WriteFile(out, []byte("an answer"), 0o600))
	earlier := func() time.Time { return curateNow.Add(-2 * time.Hour) }
	var id, stderr bytes.Buffer
	require.Equal(t, 0, runRun([]string{"record", "--producer", sessionProducer, "--label", label, "--output", out}, getenv, earlier, &id, &stderr), stderr.String())
	run := strings.TrimSpace(id.String())
	require.Equal(t, 0, runFeedback([]string{"add", "--trace", run, "--verdict", "reject", "--reason", "wrong", "--reviewer", "ann"},
		getenv, earlier, trace.ProofNone, &bytes.Buffer{}, &stderr), stderr.String())
	return run
}

// A record directory where a person approved one lesson of repo=acme and the lessons given wait, each taught by a run of its own
func curateHome(t *testing.T, lessons [][3]string) (getenv func(string) string, records string) {
	t.Helper()
	home, records := t.TempDir(), t.TempDir()
	getenv = func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
	earlier := func() time.Time { return curateNow.Add(-time.Hour) }
	for _, l := range slices.Concat([][3]string{{"k-person", "Run the tests before a commit", "repo=acme"}}, lessons) {
		var stderr bytes.Buffer
		require.Equal(t, 0, runKnowledge([]string{
			"propose", "--id", l[0], "--kind", "meaning", "--content", l[1], "--trace", curateRun(t, getenv, l[2]), "--producer", sessionProducer,
			"--label", l[2],
		}, getenv, nil, earlier, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
	}
	var stderr bytes.Buffer
	require.Equal(t, 0, runKnowledge([]string{"approve", "k-person", "--version", "1", "--approver", "ann"}, getenv, nil, earlier, atTerminal,
		&bytes.Buffer{}, &stderr), stderr.String())
	return getenv, records
}

// A client whose judge finds every change conform in the first bin and whose drafter adds the lesson the critic passes
func curateClient(t *testing.T) *llmmock.MockClient {
	t.Helper()
	client := llmmock.NewMockClient(gomock.NewController(t))
	client.EXPECT().Complete(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(func(_ context.Context, req llm.Request) (llm.Response, error) {
		answers := map[string]string{
			extract.Rules:       `{"relation":"add","kind":"meaning","content":"Name the failing test first","keys":["repo"]}`,
			extract.CriticRules: `{"states":true,"holds":true,"fits":true,"why":"ok"}`,
		}
		answer, ok := answers[req.System]
		if !ok && strings.Contains(string(req.Schema), `"decided"`) {
			answer = `{"decided":{"case_yes":"y","case_no":"n","probability":0.9},"against":{"case_yes":"y","case_no":"n","probability":0.02}}`
		}
		return llm.Response{Output: json.RawMessage(answer), Model: "claude-sonnet-5-5"}, nil
	})
	return client
}

// The newest record of every version as "id vN status approver reason" in sorted order
func curateStatuses(t *testing.T, records string) []string {
	t.Helper()
	store, err := knowledgefile.New(records)
	require.NoError(t, err)
	list, err := store.List(context.Background())
	require.NoError(t, err)
	out := []string{}
	for _, k := range knowledge.RestoreAll(list).Versions() {
		out = append(out, strings.TrimSpace(strings.Join([]string{k.ID, "v1", string(k.Status), k.Approver, string(k.Reason)}, " ")))
	}
	slices.Sort(out)
	return out
}

// knowledge curate drops what tier 0 settles, leaves a tier 1 add waiting in shadow, carries it out as nodloop once the shadow ended
// and lists what only the person decides as a question in curate.json
func TestRunKnowledgeCurate(t *testing.T) {
	getenv, records := curateHome(t, [][3]string{
		{"k-dup", "run the tests before a commit.", "repo=acme"},
		{"k-add", "Name the failing test first", "repo=acme"},
		{"k-wide", "Use the staging cluster", "repo=svc"},
	})
	client := curateClient(t)
	now := func() time.Time { return curateNow }
	var first, second, stderr bytes.Buffer

	firstCode := runKnowledge([]string{"curate"}, getenv, client, now, trace.ProofNone, &first, &stderr)
	firstMark, err := curate.ReadMark(records)
	require.NoError(t, err)
	firstStatuses := curateStatuses(t, records)
	store, err := tracefile.New(records)
	require.NoError(t, err)
	decides, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameDecide)})
	require.NoError(t, err)
	// The tier 0 drop draws its audit on a decide id of random bytes so whether it is due as an audit is read from its draw
	drawn := []string{}
	for _, d := range trace.RestoreAll(decides) {
		if d.Subject == string(curate.ChangeDrop) && d.Draw != nil && d.Draw.Drawn {
			drawn = append(drawn, d.ID)
		}
	}
	knobs, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameKnob)})
	require.NoError(t, err)
	require.Len(t, knobs, 1)
	ev, ok := curate.RestoreKnob(trace.Restore(knobs[0]))
	require.True(t, ok)
	ev.Cause, ev.Knobs.Activated = curate.KnobNarrow, true
	activated, err := ev.Trace("0.10.0", curateNow)
	require.NoError(t, err)
	require.NoError(t, store.Append(context.Background(), activated.Record()))
	secondCode := runKnowledge([]string{"curate"}, getenv, client, now, trace.ProofNone, &second, &stderr)

	assert.Equal(t, 0, firstCode, stderr.String())
	assert.Equal(t, 0, secondCode, stderr.String())
	assert.ElementsMatch(t, []string{
		"drop\tdrop\tk-dup\tv1\ttier 0\tsame_as_item", "shadow\tadd\tk-add\tv1\ttier 1\tconform", "ask\tadd\tk-wide\tv1\ttier 2\tscope_wider",
	}, strings.Split(strings.TrimSpace(first.String()), "\n"))
	assert.Equal(t, "apply\tadd\tk-add\tv1\ttier 1\tconform\n", second.String())
	assert.Equal(t, []string{"k-add v1 candidate", "k-dup v1 retired nodloop duplicate", "k-person v1 approved ann", "k-wide v1 candidate"}, firstStatuses)
	assert.Equal(t, []string{"k-add v1 approved nodloop", "k-dup v1 retired nodloop duplicate", "k-person v1 approved ann", "k-wide v1 candidate"},
		curateStatuses(t, records))
	assert.Equal(t, "2026-10-12", firstMark.CycleID)
	assert.True(t, firstMark.At.IsZero(), "a pass without --daily leaves the daily pass due")
	kinds, audits := map[string]curate.QuestionKind{}, []string{}
	for _, d := range firstMark.Due {
		if d.Kind == curate.QuestionAudit {
			audits = append(audits, d.Decide)
			continue
		}
		kinds[d.Ref.ID] = d.Kind
	}
	assert.Equal(t, map[string]curate.QuestionKind{"k-add": curate.QuestionShadow, "k-wide": curate.QuestionDirection}, kinds)
	assert.Equal(t, drawn, audits, "the drop is due as an audit exactly when its draw drew it")
	rules, err := os.ReadFile(filepath.Join(records, "approved.md"))
	require.NoError(t, err)
	assert.Contains(t, string(rules), "Name the failing test first")
}

// knowledge curate --daily extracts a correction no extraction read and decides its candidate in the same pass, once a day
func TestRunKnowledgeCurateDaily(t *testing.T) {
	getenv, records := curateHome(t, nil)
	gap := curateRun(t, getenv, "repo=acme")
	client := curateClient(t)
	now := func() time.Time { return curateNow }
	var first, again, stderr bytes.Buffer

	firstCode := runKnowledge([]string{"curate", "--daily"}, getenv, client, now, trace.ProofNone, &first, &stderr)
	mark, err := curate.ReadMark(records)
	require.NoError(t, err)
	againCode := runKnowledge([]string{"curate", "--daily"}, getenv, client, func() time.Time { return curateNow.Add(time.Hour) }, trace.ProofNone, &again, &stderr)

	assert.Equal(t, 0, firstCode, stderr.String())
	assert.Equal(t, 0, againCode, stderr.String())
	assert.Contains(t, first.String(), "gap\t"+gap+"\n")
	assert.Contains(t, first.String(), "candidate\tk-")
	assert.Regexp(t, `shadow\tadd\tk-[0-9a-f]+\tv1\ttier 1\tconform\n$`, first.String())
	assert.Empty(t, again.String(), "the mark of the first pass is less than a day old")
	assert.Equal(t, curateNow, mark.At)
	assert.Len(t, mark.Due, 1)
}

// The judge of a chat backend runs its model, an endpoint set for the judge point joins the claude variants
// and the variants read the files the person designates
func TestRunKnowledgeCurateJudge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"decided":{"noul":0.9},"against":{"noul":0.02}}}`))
	}))
	t.Cleanup(srv.Close)
	getenv, records := curateHome(t, [][3]string{{"k-add", "Name the failing test first", "repo=acme"}})
	now := func() time.Time { return curateNow }
	plan := filepath.Join(t.TempDir(), "plan.md")
	require.NoError(t, os.WriteFile(plan, []byte("Lint before tests"), 0o600))
	var stderr bytes.Buffer
	require.Equal(t, 0, runClassifier([]string{"set", "judge", "--url", srv.URL}, getenv, now, &bytes.Buffer{}, &stderr), stderr.String())
	require.Equal(t, 0, runLLM([]string{"set", "chat", "--base-url", "http://192.0.2.1/v1", "--model", "acme-model"}, getenv, nil, now, &bytes.Buffer{}, &stderr),
		stderr.String())
	require.Equal(t, 0, runAnchor([]string{"add", plan}, getenv, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
	var prompts []string
	client := llmmock.NewMockClient(gomock.NewController(t))
	client.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(3).DoAndReturn(func(_ context.Context, req llm.Request) (llm.Response, error) {
		prompts = append(prompts, req.Prompt+" model "+req.Model)
		return llm.Response{Output: json.RawMessage(`{"decided":{"probability":0.9},"against":{"probability":0.02}}`)}, nil
	})
	var stdout bytes.Buffer

	code := runKnowledge([]string{"curate"}, getenv, client, now, trace.ProofNone, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	assert.Equal(t, "shadow\tadd\tk-add\tv1\ttier 1\tconform\n", stdout.String())
	require.Len(t, prompts, 3)
	assert.Contains(t, prompts[0], "- file "+plan+"\nLint before tests\n")
	assert.True(t, strings.HasSuffix(prompts[0], " model acme-model"), prompts[0])
	store, err := tracefile.New(records)
	require.NoError(t, err)
	classified, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameClassify), Subject: "judge"})
	require.NoError(t, err)
	require.Len(t, classified, 1)
	var out struct {
		Members []struct {
			Name string `json:"name"`
		} `json:"members"`
	}
	require.NoError(t, json.Unmarshal(classified[0].Output, &out))
	assert.Len(t, out.Members, 4)
}

// knowledge curate fails on a mark or records it cannot read and leaves the mark as it was
// A daily pass still prints each correction it could not extract and goes on
func TestRunKnowledgeCurateFails(t *testing.T) {
	type args struct {
		flags []string
		// curate.json, traces.jsonl or prune.json is a directory
		markDir, tracesDir, pruneDir bool
		// The record directory lies below a regular file
		underFile bool
		// The drafter answers what does not decode
		badDraft bool
	}
	type want struct {
		code           int
		stdout, stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a mark that cannot be opened", args{flags: []string{"curate"}, markDir: true}, want{1, "", "curate: mark unreadable"}},
		{"records that cannot be read", args{flags: []string{"curate"}, tracesDir: true}, want{1, "", "trace file"}},
		{"a daily pass over records that cannot be read", args{flags: []string{"curate", "--daily"}, tracesDir: true}, want{1, "", "trace file"}},
		{"a correction the drafter cannot draft", args{flags: []string{"curate", "--daily"}, badDraft: true}, want{0, "\tnot extracted: ", ""}},
		{"a daily pass whose prune mark cannot be read", args{flags: []string{"curate", "--daily"}, pruneDir: true}, want{1, "", "marker"}},
		{"a record directory that cannot be made", args{flags: []string{"curate"}, underFile: true}, want{1, "", "not a directory"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, records := curateHome(t, nil)
			curateRun(t, getenv, "repo=acme")
			if tc.args.markDir {
				require.NoError(t, os.Mkdir(filepath.Join(records, curate.MarkFile), 0o755))
			}
			if tc.args.tracesDir {
				require.NoError(t, os.RemoveAll(filepath.Join(records, "traces.jsonl")))
				require.NoError(t, os.Mkdir(filepath.Join(records, "traces.jsonl"), 0o755))
			}
			if tc.args.pruneDir {
				require.NoError(t, os.Mkdir(filepath.Join(records, "prune.json"), 0o755))
			}
			if tc.args.underFile {
				file := filepath.Join(t.TempDir(), "file")
				require.NoError(t, os.WriteFile(file, nil, 0o600))
				inner := getenv
				getenv = func(k string) string {
					if k == envRecordDir {
						return filepath.Join(file, "records")
					}
					return inner(k)
				}
			}
			client := curateClient(t)
			if tc.args.badDraft {
				client = llmmock.NewMockClient(gomock.NewController(t))
				client.EXPECT().Complete(gomock.Any(), gomock.Any()).AnyTimes().Return(llm.Response{Output: json.RawMessage(`"no draft"`)}, nil)
			}
			var stdout, stderr bytes.Buffer

			code := runKnowledge(tc.args.flags, getenv, client, func() time.Time { return curateNow }, trace.ProofNone, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Contains(t, stdout.String(), tc.want.stdout)
			assert.Contains(t, stderr.String(), tc.want.stderr)
		})
	}
}

// A pass finds the mark held by another and ends at once, quietly when the stop started it
func TestRunKnowledgeCurateHeld(t *testing.T) {
	type want struct {
		code   int
		stderr string
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{"a pass a person runs says another one runs", []string{"curate"}, want{1, "another curate pass is running"}},
		{"a daily pass ends quietly", []string{"curate", "--daily"}, want{0, ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, records := curateHome(t, nil)
			held, err := curate.NewMarker(records).Hold()
			require.NoError(t, err)
			require.NotNil(t, held)
			t.Cleanup(func() { _ = held.Release() })
			var stdout, stderr bytes.Buffer

			code := runKnowledge(tc.args, getenv, curateClient(t), func() time.Time { return curateNow }, trace.ProofNone, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code)
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.Empty(t, stdout.String())
		})
	}
}

// The stop hook starts knowledge curate --daily in a mode that infers when its mark is due
func TestRunHookStopStartsTheDailyCurate(t *testing.T) {
	type args struct {
		session string
		// curate.json before the stop and none when empty
		mark string
		// curate.json is a directory that cannot be read
		unreadable bool
	}
	type want struct {
		started [][]string
		stderr  string
	}
	daily := [][]string{{"knowledge", "curate", "--daily"}}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no mark starts it", args{}, want{daily, ""}},
		{"a mark a day old starts it", args{mark: `{"at":"2026-10-12T08:00:00Z"}`}, want{daily, ""}},
		{"a fresh mark starts nothing", args{mark: `{"at":"2026-10-13T08:00:00Z"}`}, want{nil, ""}},
		{"manual starts nothing", args{session: "manual"}, want{nil, ""}},
		{"off starts nothing", args{session: "off"}, want{nil, ""}},
		{"a mark that cannot be read is printed and the hook passes", args{unreadable: true}, want{nil, "curate: mark unreadable"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string {
				return map[string]string{"HOME": home, envRecordDir: records, envSession: tc.args.session}[k]
			}
			if tc.args.mark != "" {
				require.NoError(t, os.WriteFile(filepath.Join(records, curate.MarkFile), []byte(tc.args.mark), 0o600))
			}
			if tc.args.unreadable {
				require.NoError(t, os.Mkdir(filepath.Join(records, curate.MarkFile), 0o755))
			}
			var started [][]string
			start := func(args []string) error {
				if args[0] == "knowledge" {
					started = append(started, args)
				}
				return nil
			}
			stdin := `{"session_id":"s1","cwd":"` + repo + `","last_assistant_message":"done"}`
			var stderr bytes.Buffer

			code := runHook([]string{"stop"}, getenv, start, func() time.Time { return curateNow }, strings.NewReader(stdin), &bytes.Buffer{}, &stderr)

			assert.Equal(t, 0, code)
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.Equal(t, tc.want.started, started)
		})
	}
}
