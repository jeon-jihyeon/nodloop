package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// One correction followed from its run to a later run that applied its lesson by the link fields of the published index alone
// 1. the run, the verdict, the extraction with its candidate, the approval and the later run come from the CLI
// 2. the decide, the expose and the answer stand for what curate and the question path write and come from their constructors
func TestRecordLinksJoin(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	home, records := t.TempDir(), t.TempDir()
	getenv := envOf(map[string]string{"HOME": home, envRecordDir: records})
	now := func() time.Time { return at }
	out := filepath.Join(home, "out.txt")
	require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
	run := extractRun(t, getenv, now, out, "ran cd before git")
	lesson := joinedLesson(t, getenv, now, run)
	later := joinedLaterRun(t, getenv, now, out, joinedAnswer(t, records, at, run, lesson))
	ix, err := schema.Published.Index()
	require.NoError(t, err)
	all := readTransported(t, ix, records)
	hops := []hop{
		{kind: "verdict", from: []string{"record_id"}, to: []string{"trace_id"}},
		{kind: "extract", from: []string{"record_id"}, to: []string{"input.verdict_id"}},
		{kind: "knowledge", from: []string{"record_id"}, to: []string{"extract"}},
		{kind: "decide", from: []string{"id", "version"}, to: []string{"input.id", "input.version"}},
		{kind: "expose", from: []string{"record_id"}, to: []string{"ref"}},
		{kind: "answer", from: []string{"record_id"}, to: []string{"ref"}},
		{kind: "knowledge", from: []string{"record_id"}, to: []string{"decision"}},
		{kind: "run", from: []string{"id", "version"}, to: []string{"input.applied.id", "input.applied.version"}},
	}

	got := all.walk("run", run, hops)

	assert.Empty(t, unlinked(ix, "run", hops), "every hop joins by record_id or a link field of the index")
	assert.Equal(t, []int{1, 1, 2, 1, 1, 1, 1, 1}, got.counts, "the candidate and the approval of it both name the extraction")
	assert.Equal(t, []string{later}, got.last)
}

// The candidate knowledge extract proposed from the corrected run through a model that drafts an add the critic passes
func joinedLesson(t *testing.T, getenv func(string) string, now func() time.Time, run string) knowledge.Knowledge {
	t.Helper()
	answers := []string{
		`{"relation":"add","kind":"judgment","content":"Run git with -C <dir> instead of changing into the directory","keys":["repo"]}`,
		`{"states":true,"holds":true,"fits":true,"why":"ok"}`,
	}
	client := llmmock.NewMockClient(gomock.NewController(t))
	client.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(len(answers)).DoAndReturn(func(context.Context, llm.Request) (llm.Response, error) {
		next := answers[0]
		answers = answers[1:]
		return llm.Response{Output: json.RawMessage(next)}, nil
	})
	var stderr bytes.Buffer
	require.Equal(t, 0, runKnowledge([]string{"extract", "--from", run}, getenv, client, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
	return newestKnowledge(t, getenv(envRecordDir))
}

// The newest knowledge record of the record directory
func newestKnowledge(t *testing.T, records string) knowledge.Knowledge {
	t.Helper()
	items, err := knowledgefile.New(records)
	require.NoError(t, err)
	listed, err := items.List(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, listed)
	return knowledge.Restore(listed[0])
}

// The id of the answer of the person to the question curate asked about the candidate
// A decide sent the candidate to the person, an expose asked about it and an answer of keep wrote its approval
func joinedAnswer(t *testing.T, records string, at time.Time, run string, k knowledge.Knowledge) string {
	t.Helper()
	traces, err := tracefile.New(records)
	require.NoError(t, err)
	const cycle, policy = "2026-10-05", "0.10.0+1a2b3c4d"
	change := &knowledge.Ref{ID: k.ID, Version: k.Version}
	decideID := trace.NewID(at)
	decide, err := curate.Event{ID: decideID, Ref: run, Source: trace.SourcePush, Draw: trace.NewDraw(cycle, policy, "seed", decideID, "audit", 1)}.
		Decide(curate.Change{Kind: curate.ChangeAdd, ID: k.ID, Version: k.Version, Extract: k.Extract, Anchor: 1},
			curate.Decision{Tier: 2, RuleCode: curate.RuleKindClosed, Action: curate.ActionAsk}, nil, at)
	require.NoError(t, err)
	asked := curate.Event{Session: "s1", Ref: decide.ID, Source: trace.SourcePush, Draw: trace.NewDraw(cycle, policy, "seed", "s1", "direction", 1)}
	expose, err := asked.Expose(curate.Exposure{
		Kind: curate.QuestionDirection, Key: "ab12", Change: change, Surface: curate.SurfaceContext, Rules: curate.QuestionRules,
		Choices: []curate.Choice{curate.ChoiceKeep, curate.ChoiceDrop}, Text: strings.Repeat("0", 64), Lines: []string{strings.Repeat("0", 64)}, Round: 1, Run: run,
	}, at)
	require.NoError(t, err)
	answered := asked
	answered.ID, answered.Ref = trace.NewID(at), expose.ID
	answer, err := answered.Answer(curate.Answer{Choice: curate.ChoiceKeep, Decide: decide.ID, Change: change, Proof: trace.ProofReply, Person: "ann"},
		curate.AnswerOutput{Records: []trace.Written{{ID: k.ID, Version: k.Version, Status: string(knowledge.StatusApproved)}}}, at)
	require.NoError(t, err)
	for _, tr := range []trace.Trace{decide, expose, answer} {
		require.NoError(t, traces.Append(context.Background(), tr.Record()))
	}
	return answer.ID
}

// The id of the later run of the place that applied the version the person approved through the answer
// The approval naming the answer in decision is imported as the answer writes it, since the join reads records and no pass
func joinedLaterRun(t *testing.T, getenv func(string) string, now func() time.Time, out, answer string) string {
	t.Helper()
	approved := newestKnowledge(t, getenv(envRecordDir))
	approved.Status, approved.Approver, approved.ApprovedAt, approved.Time, approved.Decision = knowledge.StatusApproved, "ann", now(), now(), answer
	line, err := json.Marshal(approved)
	require.NoError(t, err)
	file := filepath.Join(t.TempDir(), "approved.jsonl")
	require.NoError(t, os.WriteFile(file, append(line, '\n'), 0o600))
	var stderr bytes.Buffer
	require.Equal(t, 0, runKnowledge([]string{"import", "--file", file}, getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
	var id bytes.Buffer
	applied := fmt.Sprintf("%s:%d", approved.ID, approved.Version)
	args := []string{"record", "--producer", "session", "--label", "repo=nodloop", "--applied", applied, "--output", out}
	require.Equal(t, 0, runRun(args, getenv, now, &id, &stderr), stderr.String())
	return strings.TrimSpace(id.String())
}

// One step of a join: the records of the kind holding at the to paths what the from paths of a record before hold
// The to paths share one parent object, so an item ref inside a list matches its id and version in one element
type hop struct {
	kind     string
	from, to []string
}

// Whether next holds at the to paths what the from paths of prev hold
func (h hop) joins(prev, next map[string]any) bool {
	parent, _ := splitPath(h.to[0])
	return slices.ContainsFunc(valuesAt(next, parent), func(v any) bool {
		object, ok := v.(map[string]any)
		if !ok {
			return false
		}
		for i, to := range h.to {
			_, field := splitPath(to)
			held := valuesAt(prev, h.from[i])
			if len(held) != 1 || fmt.Sprint(object[field]) != fmt.Sprint(held[0]) {
				return false
			}
		}
		return true
	})
}

// The paths of the hops from the kind on that are neither record_id nor at or below a link field of their kind
func unlinked(ix schema.Index, kind string, hops []hop) []string {
	var out []string
	for _, h := range hops {
		for _, p := range h.from {
			if !linked(ix, kind, p) {
				out = append(out, kind+" "+p)
			}
		}
		for _, p := range h.to {
			if !linked(ix, h.kind, p) {
				out = append(out, h.kind+" "+p)
			}
		}
		kind = h.kind
	}
	return out
}

func linked(ix schema.Index, kind, path string) bool {
	k, err := ix.Kind(kind)
	return path == "record_id" || err == nil && slices.ContainsFunc(k.Links, func(l string) bool { return path == l || strings.HasPrefix(path, l+".") })
}

// Records of one record directory by kind as every transport sends them
type transported map[string][]map[string]any

// What a walk reached: how many records each hop reached and the record ids the last one did
type walked struct {
	counts []int
	last   []string
}

// The records each hop reaches from the record of the kind and id, each hop starting from every record the one before reached
func (all transported) walk(kind, id string, hops []hop) walked {
	reached := slices.DeleteFunc(slices.Clone(all[kind]), func(r map[string]any) bool { return r["record_id"] != id })
	var w walked
	for _, h := range hops {
		before := reached
		reached = slices.DeleteFunc(slices.Clone(all[h.kind]), func(next map[string]any) bool {
			return !slices.ContainsFunc(before, func(prev map[string]any) bool { return h.joins(prev, next) })
		})
		w.counts = append(w.counts, len(reached))
	}
	for _, r := range reached {
		w.last = append(w.last, fmt.Sprint(r["record_id"]))
	}
	return w
}

// The parent path and the last field of a dotted path, an empty parent naming the record itself
func splitPath(path string) (string, string) {
	i := strings.LastIndex(path, ".")
	if i < 0 {
		return "", path
	}
	return path[:i], path[i+1:]
}

// The values at a dotted path through objects and arrays, each element of an array standing alone
func valuesAt(v any, path string) []any {
	if list, ok := v.([]any); ok {
		var out []any
		for _, e := range list {
			out = append(out, valuesAt(e, path)...)
		}
		return out
	}
	if path == "" {
		return []any{v}
	}
	head, rest, _ := strings.Cut(path, ".")
	if object, ok := v.(map[string]any); ok {
		if inner, ok := object[head]; ok {
			return valuesAt(inner, rest)
		}
	}
	return nil
}

// Every record of the trace, verdict and knowledge files of the directory as a transport sends it, each valid under its schema
func readTransported(t *testing.T, ix schema.Index, dir string) transported {
	t.Helper()
	out := transported{}
	for file, kindOf := range map[string]func(line []byte) string{
		"traces.jsonl":    traceName,
		"feedback.jsonl":  func([]byte) string { return "verdict" },
		"knowledge.jsonl": func([]byte) string { return "knowledge" },
	} {
		f, err := os.Open(filepath.Join(dir, file))
		require.NoError(t, err)
		lines := bufio.NewScanner(f)
		for lines.Scan() {
			kind, err := ix.Kind(kindOf(lines.Bytes()))
			require.NoError(t, err)
			rec, err := kind.Record(json.RawMessage(slices.Clone(lines.Bytes())))
			require.NoError(t, err)
			require.NoError(t, kind.Validate(rec))
			var fields map[string]any
			require.NoError(t, json.Unmarshal(rec.JSON, &fields))
			out[rec.Kind] = append(out[rec.Kind], fields)
		}
		require.NoError(t, f.Close())
	}
	return out
}
