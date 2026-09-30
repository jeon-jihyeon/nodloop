package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func TestRunKnowledge(t *testing.T) {
	files := t.TempDir()
	valid := filepath.Join(files, "valid.jsonl")
	require.NoError(t, os.WriteFile(valid, []byte(`{"id":"k-imp","version":1,"kind":"judgment",`+
		`"content":"check tracking first","scope":{},"evidence":{"paragraph_ids":["p#1"]},"basis":"stated",`+
		`"status":"approved","approver":"demo","author":"demo","time":"2026-09-23T00:00:00Z"}`+"\n"), 0o600))
	invalid := filepath.Join(files, "invalid.jsonl")
	require.NoError(t, os.WriteFile(invalid, []byte(`{"id":"k-imp","version":1,"kind":"meaning","content":"c",`+
		`"basis":"stated",`+
		`"status":"approved","author":"demo"}`+"\n"), 0o600))
	broken := filepath.Join(files, "broken.jsonl")
	require.NoError(t, os.WriteFile(broken, []byte("nope\n"), 0o600))
	regular := filepath.Join(files, "regular")
	require.NoError(t, os.WriteFile(regular, nil, 0o600))
	agg := []string{"--kind", "meaning", "--scope-metric", "conversion_count", "--evidence-paragraph", "p#1"}
	var (
		proposeAgg    = append([]string{"propose", "--id", "k-agg", "--content", "time bases differ"}, agg...)
		proposeAgg2   = append([]string{"propose", "--id", "k-agg2", "--content", "a second item"}, agg...)
		proposeAggV2  = append([]string{"propose", "--id", "k-agg", "--content", "time bases differ by an hour"}, agg...)
		approveAgg    = []string{"approve", "k-agg", "--version", "1", "--approver", "reviewer"}
		approveAggV2  = []string{"approve", "k-agg", "--version", "2", "--approver", "reviewer"}
		secondVersion = [][]string{proposeAgg, approveAgg, proposeAggV2, approveAggV2}
		checkTracking = []string{"propose", "--id", "k-t", "--kind", "judgment", "--content", "check tracking"}
		proposeSed    = []string{
			"propose", "--id", "k-sed", "--kind", "judgment", "--content", "never edit files with sed -i",
			"--evidence-paragraph", "p#1", "--veto-tool", "Bash", "--veto-field", "command", "--veto-match", `sed\s+-i`,
			"--veto-example", `{"command":"sed -i s/a/b/ f"}`,
		}
		proposeSedNotJSON = []string{
			"propose", "--id", "k-sed", "--kind", "judgment", "--content", "never edit files with sed -i",
			"--evidence-paragraph", "p#1", "--veto-tool", "Bash", "--veto-field", "command", "--veto-match", `sed\s+-i`,
			"--veto-example", "sed -i",
		}
		proposeSedMissed = []string{
			"propose", "--id", "k-sed", "--kind", "judgment", "--content", "never edit files with sed -i",
			"--evidence-paragraph", "p#1", "--veto-tool", "Bash", "--veto-field", "command", "--veto-match", `sed\s+-i`,
			"--veto-example", `{"command":"ls"}`,
		}
		approveSed   = []string{"approve", "k-sed", "--version", "1", "--approver", "reviewer"}
		proposeLarge = append([]string{"propose", "--id", "k-large", "--content", strings.Repeat("x", knowledge.ReviewChars)}, agg...)
		proposeNamed = func(id string) []string {
			return append([]string{"propose", "--id", id, "--content", "item " + id}, agg...)
		}
		approveNamed = func(id string) []string { return []string{"approve", id, "--version", "1", "--approver", "reviewer"} }
		retireSed    = []string{"retire", "k-sed", "--version", "1", "--approver", "reviewer"}
		hooked       = `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"{home}/bin/nodloop guard"}]}]}}`
		vetoes       = "vetoes\t1 approved in .*/\\.claude/nodloop/vetoes\\.approved\\.[0-9a-f]+\\.yaml\t"
		unhooked     = "guard hook not installed\\. Run nodloop guard install to enforce them\n"
		folder       = "folder\t[0-9]+ of 70000 chars\t1 items\tno other item\n"
	)
	type args struct {
		// Commands that must succeed before the one under test
		// The record dir holds the diagnose trace d1 and the context trace c1
		setup [][]string
		args  []string
		// HOME for the run
		// `{home}` is a temp dir and empty means no home
		home string
		// Files under the temp home keyed by relative path such as `.claude/settings.json`
		files map[string]string
		// Content of `policy.yaml` in the data dir
		policy string
	}
	type want struct {
		code int
		// Regexp matched against stdout
		stdout string
		// Regexp matched against stderr
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"propose without evidence fails",
			args{args: []string{"propose", "--id", "k-new", "--kind", "meaning", "--content", "time bases differ"}},
			want{1, `^$`, `^nodloop knowledge: ` + knowledge.ErrEvidenceRequired.Error()},
		},
		{
			"propose adds a candidate",
			args{args: proposeAgg},
			want{0, "^k-agg\tv1\tcandidate\nfolder\t[0-9]+ of 70000 chars\t[0-9]+ items\tno other item\n$", `^$`},
		},
		{
			"propose without an id generates one",
			args{
				setup: nil,
				args:  []string{"propose", "--kind", "meaning", "--content", "time bases differ", "--evidence-paragraph", "p#1"},
			},
			want{0, "^k-[0-9a-f]+\tv1\tcandidate\nfolder\t[0-9]+ of 70000 chars\t[0-9]+ items\tno other item\n$", `^$`},
		},
		{
			"propose lists overlaps",
			args{setup: [][]string{proposeAgg}, args: proposeAgg2},
			want{
				0, "^k-agg2\tv1\tcandidate\noverlaps\tk-agg\tv1\tcandidate\nfolder\t[0-9]+ of 70000 chars\t[0-9]+ items\tno other item\n$", `^$`,
			},
		},
		{
			"propose of a known id adds the next version",
			args{setup: [][]string{proposeAgg}, args: proposeAggV2},
			want{0, "^k-agg\tv2\tcandidate\nfolder\t[0-9]+ of 70000 chars\t[0-9]+ items\tno other item\n$", `^$`},
		},
		{
			"propose takes the trace as feedback evidence",
			args{args: append(checkTracking, "--trace", "d1", "--scope-context", "launch", "--exception", "other")},
			want{0, "^k-t\tv1\tcandidate\nfolder\t[0-9]+ of 70000 chars\t[0-9]+ items\tno other item\n$", `^$`},
		},
		{
			"propose with an unknown trace fails",
			args{args: append(checkTracking, "--trace", "nope")},
			want{1, `^$`, `^nodloop knowledge: trace "nope": ` + trace.ErrNotFound.Error() + `\n$`},
		},
		{
			"propose with a context trace as outcome evidence fails",
			args{args: append(checkTracking, "--evidence-outcome", "c1")},
			want{1, `^$`, `^nodloop knowledge: .*not a diagnose trace: c1 is a context trace\n$`},
		},
		{
			"approve without an approver fails",
			args{setup: [][]string{proposeAgg}, args: []string{"approve", "k-agg", "--version", "1"}},
			want{1, `^$`, `^nodloop knowledge: approve: an id, --version and --approver is required\n\nusage:`},
		},
		{
			"approve names the approver",
			args{setup: [][]string{proposeAgg}, args: approveAgg},
			want{0, "^k-agg\tv1\tapproved\treviewer\n" + folder + "$", `^$`},
		},
		{
			"approve twice fails",
			args{setup: [][]string{proposeAgg, approveAgg}, args: approveAgg},
			want{1, `^$`, `^nodloop knowledge: .*cannot become`},
		},
		{
			"approve of the second version",
			args{setup: [][]string{proposeAgg, approveAgg, proposeAggV2}, args: approveAggV2},
			want{0, "^k-agg\tv2\tapproved\treviewer\n" + folder + "$", `^$`},
		},
		{
			"list shows only the current version",
			args{setup: secondVersion, args: []string{"list"}},
			want{0, "^k-agg\tv2\tapproved\tmeaning\ttime bases differ by an hour\tstale=false\n$", `^$`},
		},
		{
			"list filters by status and kind",
			args{
				setup: [][]string{proposeAgg, approveAgg, proposeAgg2},
				args:  []string{"list", "--status", "candidate", "--kind", "meaning"},
			},
			want{0, "^k-agg2\tv1\tcandidate\tmeaning\ta second item\tstale=false\n$", `^$`},
		},
		{
			"show keeps the superseded first version",
			args{setup: secondVersion, args: []string{"show", "k-agg"}},
			want{0, `"status": "superseded"`, `^$`},
		},
		{
			"show links the second version to the first",
			args{setup: secondVersion, args: []string{"show", "k-agg"}},
			want{0, `"supersedes": 1`, `^$`},
		},
		{
			"show of an unknown id fails",
			args{args: []string{"show", "nope"}},
			want{1, `^$`, `^nodloop knowledge: .*not found`},
		},
		{
			"show without an id fails",
			args{args: []string{"show"}},
			want{1, `^$`, `^nodloop knowledge: an id is required\n\nusage:`},
		},
		{
			"overlaps lists items of the same scope",
			args{setup: [][]string{proposeAgg, proposeAgg2}, args: []string{"overlaps", "k-agg"}},
			want{0, "^k-agg2\tv1\tcandidate\ta second item\n$", `^$`},
		},
		{
			"overlaps of an unknown id fails",
			args{args: []string{"overlaps", "nope"}},
			want{1, `^$`, `^nodloop knowledge: .*not found`},
		},
		{
			"overlaps without an id fails",
			args{args: []string{"overlaps"}},
			want{1, `^$`, `^nodloop knowledge: an id is required\n\nusage:`},
		},
		{
			"retire marks the item retired",
			args{
				setup: [][]string{proposeAgg2},
				args:  []string{"retire", "k-agg2", "--version", "1", "--approver", "reviewer"},
			},
			want{0, "^k-agg2\tv1\tretired\treviewer\n$", `^$`},
		},
		{
			"retire without a version fails",
			args{args: []string{"retire", "k-agg2", "--approver", "reviewer"}},
			want{1, `^$`, `^nodloop knowledge: retire: an id, --version and --approver is required\n\nusage:`},
		},
		{
			"retire of an unknown id fails",
			args{args: []string{"retire", "nope", "--version", "1", "--approver", "reviewer"}},
			want{1, `^$`, `^nodloop knowledge: .*not found`},
		},
		{
			"import appends the records of a file",
			args{args: []string{"import", "--file", valid}},
			want{0, "^imported 1\n$", `^$`},
		},
		{
			"import without a file fails",
			args{args: []string{"import"}},
			want{1, `^$`, `^nodloop knowledge: --file is required\n\nusage:`},
		},
		{
			"import of a missing file fails",
			args{args: []string{"import", "--file", filepath.Join(files, "missing.jsonl")}},
			want{1, `^$`, `^nodloop knowledge: stat .*missing.jsonl: no such file or directory\n$`},
		},
		{
			"import of a broken file fails",
			args{args: []string{"import", "--file", broken}},
			want{1, `^$`, `^nodloop knowledge: broken.jsonl line 1`},
		},
		{
			"import of an invalid record fails",
			args{args: []string{"import", "--file", invalid}},
			want{1, `^$`, `^nodloop knowledge: .*invalid.jsonl: record 1`},
		},
		{
			"record dir that is a file fails",
			args{args: []string{"list", "--record-dir", regular}},
			want{1, `^$`, `^nodloop knowledge: record dir: `},
		},
		{
			"unknown source fails",
			args{args: []string{"list", "--source", "postgres"}},
			want{1, `^$`, `^nodloop knowledge: unknown source: "postgres"\n$`},
		},
		{
			"propose with a veto adds a judgment that carries it",
			args{args: proposeSed},
			want{
				0,
				"^k-sed\tv1\tcandidate\nveto\tBash\tcommand matches sed\\\\s\\+-i unless \"\"\n" +
					"folder\t[0-9]+ of 70000 chars\t[0-9]+ items\tno other item\n$",
				`^$`,
			},
		},
		{
			"propose with a veto example that is not JSON fails",
			args{args: proposeSedNotJSON},
			want{1, `^$`, `^nodloop knowledge: veto example is not a JSON object: --veto-example: invalid character`},
		},
		{
			"propose with a veto that lets its example through fails",
			args{args: proposeSedMissed},
			want{1, `^$`, `^nodloop knowledge: ` + knowledge.ErrVetoExample.Error() + `\n$`},
		},
		{
			"approve of a veto says the guard hook is not installed",
			args{setup: [][]string{proposeSed}, args: approveSed, home: "{home}"},
			want{0, "^k-sed\tv1\tapproved\treviewer\n" + vetoes + unhooked + folder + "$", `^$`},
		},
		{
			"approve of a veto says the guard hook is installed",
			args{
				setup: [][]string{proposeSed}, args: approveSed, home: "{home}",
				files: map[string]string{".claude/settings.json": hooked, "bin/nodloop": ""},
			},
			want{0, "^k-sed\tv1\tapproved\treviewer\n" + vetoes + "guard hook installed\n" + folder + "$", `^$`},
		},
		{
			"approve of a veto says a hook whose executable is gone is broken",
			args{
				setup: [][]string{proposeSed}, args: approveSed, home: "{home}",
				files: map[string]string{".claude/settings.json": hooked},
			},
			want{
				0, "^k-sed\tv1\tapproved\treviewer\n" + vetoes +
					"guard hook broken: hook executable missing: .*/bin/nodloop\\. Run nodloop guard install to repair it\n" + folder + "$",
				`^$`,
			},
		},
		{
			"approve of a veto with a broken settings file says the hook state is unknown",
			args{
				setup: [][]string{proposeSed}, args: approveSed, home: "{home}",
				files: map[string]string{".claude/settings.json": "{"},
			},
			want{0, "^k-sed\tv1\tapproved\treviewer\n" + vetoes + "guard hook unknown: settings file: not valid JSON", `^$`},
		},
		{
			"retire of the only veto says none is left",
			args{setup: [][]string{proposeSed, approveSed}, args: retireSed, home: "{home}"},
			want{0, "^k-sed\tv1\tretired\treviewer\nvetoes\t0 approved in .*\t" + unhooked + "$", `^$`},
		},
		{
			"retire of a candidate with a veto says nothing about vetoes",
			args{setup: [][]string{proposeSed}, args: retireSed, home: "{home}"},
			want{0, "^k-sed\tv1\tretired\treviewer\n$", `^$`},
		},
		{
			"approve of a veto without a home says it was not exported",
			args{setup: [][]string{proposeSed}, args: approveSed},
			want{0, "^k-sed\tv1\tapproved\treviewer\nvetoes\tnot exported because the home directory is unknown\n" + folder + "$", `^$`},
		},
		{
			"approve of a veto whose export fails prints the approval and the error",
			args{
				setup: [][]string{proposeSed}, args: approveSed, home: "{home}",
				files: map[string]string{".claude/nodloop": ""},
			},
			want{
				1, "^k-sed\tv1\tapproved\treviewer\n$",
				`^nodloop knowledge: knowledge: vetoes were not exported: k-sed v1 is approved: .*failed to write veto file`,
			},
		},
		{
			"export writes the approved vetoes again",
			args{setup: [][]string{proposeSed, approveSed}, args: []string{"export"}, home: "{home}"},
			want{0, "^" + vetoes + unhooked + "$", `^$`},
		},
		{
			"approve of an item whose folder may outgrow the review is refused",
			args{
				setup: [][]string{proposeAgg, approveAgg, proposeLarge},
				args:  []string{"approve", "k-large", "--version", "1", "--approver", "reviewer"},
			},
			want{
				1, `^$`,
				`^nodloop knowledge: knowledge: folder may outgrow the review: 70125 of 70000 chars with k-agg v1 70 chars\n$`,
			},
		},
		{
			"a broken analyzer in the policy never blocks a knowledge command",
			args{setup: [][]string{proposeAgg2}, args: []string{"list"}, policy: "version: v\nanalyzers:\n  - rule: nope\n"},
			want{0, "^k-agg2\tv1\tcandidate\tmeaning\ta second item\tstale=false\n$", `^$`},
		},
		{
			"a broken policy never stops a knowledge command",
			args{setup: [][]string{proposeAgg2}, args: []string{"list"}, policy: "limits: ["},
			want{0, "^k-agg2\tv1\tcandidate\tmeaning\ta second item\tstale=false\n$", `^$`},
		},
		{
			"approve prints the folder and says when a compaction is due",
			args{
				setup: [][]string{{"import", "--file", "testdata/crowded.jsonl"}},
				args:  approveNamed("k-r6"),
			},
			want{
				0, "^k-r6\tv1\tapproved\treviewer\nfolder\t[0-9]+ of 70000 chars\t6 items\t.*\n" +
					"compaction due\tthe folder holds more than 5 approved items an event can replay\\. " +
					"Run nodloop knowledge compact k-r6\n$",
				`^$`,
			},
		},
		{
			"an item that cites only paragraphs never makes a compaction due since it cannot anchor one",
			args{
				setup: [][]string{{"import", "--file", "testdata/crowded.jsonl"}, proposeNamed("k-p")},
				args:  approveNamed("k-p"),
			},
			want{0, "^k-p\tv1\tapproved\treviewer\nfolder\t[0-9]+ of 70000 chars\t6 items\t.*\n$", `^$`},
		},
		{
			"unknown flag fails",
			args{args: []string{"list", "--nope"}},
			want{1, `^$`, `^flag provided but not defined: -nope\n`},
		},
		{
			"unknown action fails",
			args{args: []string{"bogus"}},
			want{1, `^$`, `^nodloop knowledge: unknown action "bogus"\n\nusage:`},
		},
	}
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records := t.TempDir()
			traces, err := tracefile.New(records)
			require.NoError(t, err)
			require.NoError(t, traces.Append(ctx, trace.Trace{ID: "d1", Name: trace.NameDiagnose, Time: at}))
			require.NoError(t, traces.Append(ctx, trace.Trace{ID: "c1", Name: trace.NameContext, Time: at}))
			// The files always go to a temp dir and a row without a home leaves HOME empty
			dir := t.TempDir()
			for rel, content := range tc.args.files {
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, rel), []byte(strings.ReplaceAll(content, "{home}", dir)), 0o600))
			}
			home := strings.NewReplacer("{home}", dir).Replace(tc.args.home)
			data := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(data, "policy.yaml"), []byte(tc.args.policy), 0o600))
			getenv := func(k string) string {
				return map[string]string{envFileDir: data, envRecordDir: records, "HOME": home}[k]
			}
			now := testkit.Open(t).Clock.Now
			var stderr bytes.Buffer
			for _, args := range tc.args.setup {
				code := runKnowledge(args, getenv, nil, now, io.Discard, &stderr)
				require.Equal(t, 0, code, "%s: stderr = %s", strings.Join(args, " "), stderr.String())
			}
			var stdout bytes.Buffer

			got := runKnowledge(tc.args.args, getenv, nil, now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Regexp(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}

// A batch review of tq-005 is corrected and proposed as knowledge from its trace
// The model answers the review and the content draft and each row checks the candidate the ledger holds
func TestRunKnowledgeFrom(t *testing.T) {
	review := `{"status":"ready_for_review","observations":[],"causes":[{"summary":"low quality traffic","paragraph_ids":` +
		`["metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"]}],"checks":[` +
		`{"step":"s","purpose":"p","paragraph_ids":["metric-anomaly-investigation#Metric anomaly investigation/Confirm the signal#1"]}],` +
		`"open_questions":[]}`
	edited := filepath.Join(t.TempDir(), "edited.json")
	require.NoError(t, os.WriteFile(edited, []byte(`{"status":"no_action","observations":[],"causes":[],"checks":[],"open_questions":[]}`), 0o600))
	ctx := context.Background()
	st := testkit.Open(t)
	ev, err := st.Source.Event(ctx, "tq-005")
	require.NoError(t, err)
	obs, err := testkit.Policy(t).Analyze(ev)
	require.NoError(t, err)
	type args struct {
		verdict []string
		propose []string
	}
	type want struct {
		code           int
		stdout, stderr string
		// The candidates as stored without their id and time and evidence
		stored []knowledge.Knowledge
	}
	filled := func(metrics []string, content string, drafted bool) knowledge.Knowledge {
		return knowledge.Knowledge{
			Version: 1, Kind: knowledge.KindMeaning, Content: content, Basis: knowledge.BasisStated,
			Scope: knowledge.Scope{Scope: evidence.Scope{
				ChangeContexts: []evidence.Context{evidence.ContextNoKnownChange}, Metrics: metrics,
			}},
			Status: knowledge.StatusCandidate, Author: "author", Drafted: drafted,
		}
	}
	candidate := "^k-[0-9a-f]+\tv1\tcandidate\nfolder\t[0-9]+ of 70000 chars\t1 items\tno other item\n"
	drafting := gomock.Cond(func(r llm.Request) bool { return r.System == diagnose.DraftRules && r.Model == "haiku" })
	drafted := llm.Response{Output: json.RawMessage(`{"content":"Clicks that never convert are no incident."}`), CostUSD: 0.0012}
	tcs := []struct {
		name string
		args args
		// Model calls beyond the review
		init func(client *llmmock.MockClient)
		want want
	}{
		{
			"content given fills scope and evidence without a model call",
			args{[]string{"--verdict", "reject"}, []string{"--kind", "meaning", "--content", "clicks without conversions"}},
			func(*llmmock.MockClient) {},
			want{0, candidate + "$", `^$`, []knowledge.Knowledge{filled(obs.Moved(), "clicks without conversions", false)}},
		},
		{
			"content left out is drafted by the model and marked",
			args{[]string{"--verdict", "edit", "--edited", edited}, []string{"--kind", "meaning", "--model", "haiku"}},
			func(client *llmmock.MockClient) {
				client.EXPECT().Complete(gomock.Any(), drafting).Return(drafted, nil)
			},
			want{
				0, candidate + "drafted\t0.0012 usd\tClicks that never convert are no incident\\.\n$", `^$`,
				[]knowledge.Knowledge{filled(obs.Moved(), "Clicks that never convert are no incident.", true)},
			},
		},
		{
			"a scope metric given replaces the filled metrics",
			args{
				[]string{"--verdict", "reject"},
				[]string{"--kind", "meaning", "--content", "c", "--scope-metric", "conversion_count"},
			},
			func(*llmmock.MockClient) {},
			want{0, candidate + "$", `^$`, []knowledge.Knowledge{filled([]string{"conversion_count"}, "c", false)}},
		},
		{
			"an approved review is no correction",
			args{[]string{"--verdict", "approve"}, []string{"--kind", "meaning", "--content", "c"}},
			func(*llmmock.MockClient) {},
			want{1, `^$`, `^nodloop knowledge: ` + diagnose.ErrNotCorrected.Error(), []knowledge.Knowledge{}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records, home := t.TempDir(), t.TempDir()
			getenv := func(k string) string {
				return map[string]string{envFileDir: testkit.DemoDir(t), envRecordDir: records, "HOME": home}[k]
			}
			client := llmmock.NewMockClient(gomock.NewController(t))
			reviewing := gomock.Cond(func(r llm.Request) bool { return r.System == diagnose.Rules })
			client.EXPECT().Complete(gomock.Any(), reviewing).Return(llm.Response{Output: json.RawMessage(review)}, nil).AnyTimes()
			tc.init(client)
			now := testkit.Open(t).Clock.Now
			var stderr bytes.Buffer
			require.Equal(t, 0, runDiagnose([]string{"--event", "tq-005", "--knowledge", "none"}, getenv, client, now, io.Discard, &stderr), stderr.String())
			traceID := strings.TrimSpace(strings.TrimPrefix(stderr.String(), "trace "))
			stderr.Reset()
			verdict := append([]string{"add", "--trace", traceID, "--reason", "clicks without conversions"}, tc.args.verdict...)
			require.Equal(t, 0, runFeedback(verdict, getenv, now, io.Discard, &stderr), stderr.String())
			var stdout bytes.Buffer

			got := runKnowledge(append([]string{"propose", "--from", traceID}, tc.args.propose...), getenv, client, now, &stdout, &stderr)

			store, err := knowledgefile.New(records)
			require.NoError(t, err)
			all, err := store.List(ctx)
			require.NoError(t, err)
			stored := make([]knowledge.Knowledge, 0, len(all))
			for _, k := range all {
				assert.Equal(t, knowledge.Evidence{FeedbackTraceIDs: []string{traceID}}, k.Evidence)
				k.ID, k.Time, k.Evidence = "", time.Time{}, knowledge.Evidence{}
				stored = append(stored, k)
			}
			assert.Equal(t, tc.want.code, got)
			assert.Regexp(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
			assert.Equal(t, tc.want.stored, stored)
		})
	}
}

// The loop a correction closes: a judgment with a veto is approved and the next matching tool call is blocked
func TestApprovedVetoBlocksTheCall(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		args string
		want int
	}{
		{"the example of the approved veto is blocked", `{"command":"sed -i s/a/b/ f"}`, 2},
		{"a call the veto does not match passes", `{"command":"sed s/a/b/ f"}`, 0},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records := t.TempDir(), t.TempDir()
			getenv := func(k string) string {
				return map[string]string{envFileDir: testkit.DemoDir(t), envRecordDir: records, "HOME": home}[k]
			}
			now := testkit.Open(t).Clock.Now
			var stderr bytes.Buffer
			for _, args := range [][]string{
				{
					"propose", "--id", "k-sed", "--kind", "judgment", "--content", "never edit files with sed -i",
					"--evidence-paragraph", "p#1", "--veto-tool", "Bash", "--veto-field", "command",
					"--veto-match", `sed\s+-i`, "--veto-example", `{"command":"sed -i x f"}`,
				},
				{"approve", "k-sed", "--version", "1", "--approver", "reviewer"},
			} {
				require.Equal(t, 0, runKnowledge(args, getenv, nil, now, io.Discard, &stderr), stderr.String())
			}
			input := `{"tool_name":"Bash","cwd":"` + t.TempDir() + `","tool_input":` + tc.args + `}`

			got := runGuard(nil, getenv, os.Executable, strings.NewReader(input), io.Discard, &stderr)

			assert.Equal(t, tc.want, got)
		})
	}
}
