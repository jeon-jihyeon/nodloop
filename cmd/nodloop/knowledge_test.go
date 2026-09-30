package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
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
		folder       = "folder\t[0-9]+ of 70000 chars\t1 of 10 items in [a-z_ ]+\tno other item\n"
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
		// Content of `events.csv` in the data dir
		// Empty means one conversion_count row
		events string
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
			want{0, "^k-agg\tv1\tcandidate\nscope\tmetrics conversion_count\nfolder\t[0-9]+ of 70000 chars\t[0-9]+ of 10 items in [a-z_ ]+\tno other item\n$", `^$`},
		},
		{
			"propose without an id generates one",
			args{
				setup: nil,
				args:  []string{"propose", "--kind", "meaning", "--content", "time bases differ", "--evidence-paragraph", "p#1"},
			},
			want{0, "^k-[0-9a-f]+\tv1\tcandidate\nscope\tany event\nfolder\t[0-9]+ of 70000 chars\t[0-9]+ of 10 items in [a-z_ ]+\tno other item\n$", `^$`},
		},
		{
			"propose lists overlaps",
			args{setup: [][]string{proposeAgg}, args: proposeAgg2},
			want{
				0, "^k-agg2\tv1\tcandidate\nscope\tmetrics conversion_count\noverlaps\tk-agg\tv1\tcandidate\nfolder\t[0-9]+ of 70000 chars\t[0-9]+ of 10 items in [a-z_ ]+\tno other item\n$", `^$`,
			},
		},
		{
			"propose of a known id adds the next version",
			args{setup: [][]string{proposeAgg}, args: proposeAggV2},
			want{0, "^k-agg\tv2\tcandidate\nscope\tmetrics conversion_count\nfolder\t[0-9]+ of 70000 chars\t[0-9]+ of 10 items in [a-z_ ]+\tno other item\n$", `^$`},
		},
		{
			"propose takes the trace as feedback evidence",
			args{args: append(checkTracking, "--trace", "d1", "--scope-context", "no_known_change", "--scope-context", "unknown", "--exception", "unknown")},
			want{0, "^k-t\tv1\tcandidate\nscope\tchange contexts no_known_change or unknown\nfolder\t[0-9]+ of 70000 chars\t[0-9]+ of 10 items in [a-z_ ]+\tno other item\n$", `^$`},
		},
		{
			"propose with a misspelled exception fails before anything is recorded",
			args{args: append(slices.Clone(proposeAgg), "--exception", "planned_change")},
			want{1, `^$`, `^nodloop knowledge: knowledge: scope names no event: exception "planned_change" is not one of`},
		},
		{
			"propose with exceptions over its only change context fails",
			args{args: append(slices.Clone(proposeAgg), "--scope-context", "no_known_change", "--exception", "no_known_change")},
			want{1, `^$`, `^nodloop knowledge: knowledge: scope names no event: the exceptions \[no_known_change\] cover`},
		},
		{
			"propose with a metric no event carries fails",
			args{args: []string{
				"propose", "--id", "k-m", "--kind", "meaning", "--content", "c", "--scope-metric", "conversions",
				"--evidence-paragraph", "p#1",
			}},
			want{1, `^$`, `^nodloop knowledge: knowledge: scope names a value no event of the data set carries: metric conversions\n`},
		},
		{
			"propose without a scope metric never reads the events",
			args{args: []string{"propose", "--id", "k-e", "--kind", "meaning", "--content", "c", "--evidence-paragraph", "p#1"}, events: "nope\n"},
			want{0, "^k-e\tv1\tcandidate\nscope\tany event\n" + folder + "$", `^$`},
		},
		{
			"propose with a scope metric reads the events",
			args{args: proposeAgg, events: "nope\n"},
			want{1, `^$`, `^nodloop knowledge: .+`},
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
			"import of a file again skips the records already recorded",
			args{setup: [][]string{{"import", "--file", valid}}, args: []string{"import", "--file", valid}},
			want{0, "^imported 0\tskipped 1 already recorded\n$", `^$`},
		},
		{
			"import of a file again after a retire keeps the item retired",
			args{
				setup: [][]string{
					{"import", "--file", valid}, {"retire", "k-imp", "--version", "1", "--approver", "reviewer"},
					{"import", "--file", valid},
				},
				args: []string{"list"},
			},
			want{0, `^$`, `^$`},
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
				"^k-sed\tv1\tcandidate\nscope\tany event\nveto\tBash\tcommand matches sed\\\\s\\+-i unless \"\"\n" +
					"folder\t[0-9]+ of 70000 chars\t[0-9]+ of 10 items in [a-z_ ]+\tno other item\n$",
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
				`^nodloop knowledge: knowledge: folder may outgrow the review: 70125 of 70000 chars 2 of 10 items in no_known_change with k-agg v1 70 chars\n$`,
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
				0, "^k-r6\tv1\tapproved\treviewer\nfolder\t[0-9]+ of 70000 chars\t6 of 10 items in [a-z_ ]+\t.*\n" +
					"compaction due\ta review of one change context carries more than 5 approved items an event can replay\\. " +
					"Run nodloop knowledge compact k-r6\n$",
				`^$`,
			},
		},
		{
			"approve of a crowded folder whose events have no expected status says the compaction is blocked",
			args{
				setup: [][]string{{"import", "--file", "testdata/crowded-rejected.jsonl"}},
				args:  approveNamed("k-j6"),
			},
			want{
				0, "^k-j6\tv1\tapproved\treviewer\nfolder\t[0-9]+ of 70000 chars\t6 of 10 items in [a-z_ ]+\t.*\n" +
					"compaction blocked\ta review of one change context carries more than 5 approved items but no evidence event has " +
					"a label or an edit or approve verdict: e-r-6, e-r-1, e-r-2, e-r-3, e-r-4, e-r-5\n$",
				`^$`,
			},
		},
		{
			"an item that cites only paragraphs never makes a compaction due since it cannot anchor one",
			args{
				setup: [][]string{{"import", "--file", "testdata/crowded.jsonl"}, proposeNamed("k-p")},
				args:  approveNamed("k-p"),
			},
			want{0, "^k-p\tv1\tapproved\treviewer\nfolder\t[0-9]+ of 70000 chars\t6 of 10 items in [a-z_ ]+\t.*\n$", `^$`},
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
			// The reviews the crowded fixtures cite
			// t-N is approved so its event has an expected status and r-N is rejected so its event has none
			verdicts, err := feedbackfile.New(records)
			require.NoError(t, err)
			for i := 1; i <= 6; i++ {
				for prefix, verdict := range map[string]feedback.Verdict{"t": feedback.VerdictApprove, "r": feedback.VerdictReject} {
					id := fmt.Sprintf("%s-%d", prefix, i)
					require.NoError(t, traces.Append(ctx, trace.Trace{
						ID: id, Name: trace.NameDiagnose, Subject: "e-" + id, Time: at, Output: json.RawMessage(`{"status":"hold"}`),
					}))
					fb, err := feedback.New(id, verdict, "reason", nil, "", at)
					require.NoError(t, err)
					require.NoError(t, verdicts.Append(ctx, fb))
				}
			}
			// The files always go to a temp dir and a row without a home leaves HOME empty
			dir := t.TempDir()
			for rel, content := range tc.args.files {
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, rel), []byte(strings.ReplaceAll(content, "{home}", dir)), 0o600))
			}
			home := strings.NewReplacer("{home}", dir).Replace(tc.args.home)
			data := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(data, "policy.yaml"), []byte(tc.args.policy), 0o600))
			// Propose checks scope metrics against the events
			events := cmp.Or(tc.args.events, "event_id,timestamp,metric,value\ne1,2026-09-22T00:00:00Z,conversion_count,1\n")
			require.NoError(t, os.WriteFile(filepath.Join(data, "events.csv"), []byte(events), 0o600))
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

// A batch review is corrected and proposed as knowledge from its trace
// tq-005 moved click_count and conversion_count and no metric moved on tq-001
// The model answers the review and the content draft and each row checks the candidate the ledger holds
func TestRunKnowledgeFrom(t *testing.T) {
	review := `{"status":"ready_for_review","observations":[],"causes":[{"summary":"low quality traffic","paragraph_ids":` +
		`["metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"]}],"checks":[` +
		`{"step":"s","purpose":"p","paragraph_ids":["metric-anomaly-investigation#Metric anomaly investigation/Confirm the signal#1"]}],` +
		`"open_questions":[]}`
	edited := filepath.Join(t.TempDir(), "edited.json")
	require.NoError(t, os.WriteFile(edited, []byte(`{"status":"no_action","observations":[],"causes":[],"checks":[],"open_questions":[]}`), 0o600))
	ctx := context.Background()
	moved := []string{"click_count", "conversion_count"}
	type args struct {
		event   string
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
	// The id line and the scope line and the folder line of a stored candidate
	candidate := func(scope string) string {
		return "^k-[0-9a-f]+\tv1\tcandidate\nscope\t" + scope + "\nfolder\t[0-9]+ of 70000 chars\t1 of 10 items in [a-z_ ]+\tno other item\n"
	}
	spike := "change contexts no_known_change\\. metrics click_count or conversion_count"
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
			args{"tq-005", []string{"--verdict", "reject"}, []string{"--kind", "meaning", "--content", "clicks without conversions"}},
			func(*llmmock.MockClient) {},
			want{0, candidate(spike) + "$", `^$`, []knowledge.Knowledge{filled(moved, "clicks without conversions", false)}},
		},
		{
			"content left out is drafted by the model and marked",
			args{"tq-005", []string{"--verdict", "edit", "--edited", edited}, []string{"--kind", "meaning", "--model", "haiku"}},
			func(client *llmmock.MockClient) {
				client.EXPECT().Complete(gomock.Any(), drafting).Return(drafted, nil)
			},
			want{
				0, candidate(spike) + "drafted\t0.0012 usd\tClicks that never convert are no incident\\.\n$", `^$`,
				[]knowledge.Knowledge{filled(moved, "Clicks that never convert are no incident.", true)},
			},
		},
		{
			"a scope metric given replaces the filled metrics",
			args{
				"tq-005", []string{"--verdict", "reject"},
				[]string{"--kind", "meaning", "--content", "c", "--scope-metric", "conversion_count"},
			},
			func(*llmmock.MockClient) {},
			want{
				0, candidate("change contexts no_known_change\\. metrics conversion_count") + "$", `^$`,
				[]knowledge.Knowledge{filled([]string{"conversion_count"}, "c", false)},
			},
		},
		{
			"a review where no metric moved and no scope given fails before anything is recorded",
			args{"tq-001", []string{"--verdict", "reject"}, []string{"--kind", "meaning", "--content", "c"}},
			func(*llmmock.MockClient) {},
			want{
				1, `^$`, `^nodloop knowledge: ` + diagnose.ErrQuietScope.Error() + ` no_known_change\. Name the change contexts of`,
				[]knowledge.Knowledge{},
			},
		},
		{
			"a review where no metric moved and no scope given fails before the content draft",
			args{"tq-001", []string{"--verdict", "edit", "--edited", edited}, []string{"--kind", "meaning", "--model", "haiku"}},
			func(*llmmock.MockClient) {},
			want{1, `^$`, `^nodloop knowledge: ` + diagnose.ErrQuietScope.Error(), []knowledge.Knowledge{}},
		},
		{
			"a change context given on a review where no metric moved accepts every event of that context",
			args{
				"tq-001", []string{"--verdict", "reject"},
				[]string{"--kind", "meaning", "--content", "c", "--scope-context", "no_known_change"},
			},
			func(*llmmock.MockClient) {},
			want{0, candidate("change contexts no_known_change") + "$", `^$`, []knowledge.Knowledge{filled(nil, "c", false)}},
		},
		{
			"a metric given on a review where no metric moved is refused before anything is recorded",
			args{
				"tq-001", []string{"--verdict", "reject"},
				[]string{"--kind", "meaning", "--content", "c", "--scope-metric", "conversion_count"},
			},
			func(*llmmock.MockClient) {},
			want{
				1, `^$`, `^nodloop knowledge: ` + diagnose.ErrQuietMetricScope.Error() + `: conversion_count`,
				[]knowledge.Knowledge{},
			},
		},
		{
			"a metric given with a change context on a review where no metric moved is refused before the content draft",
			args{
				"tq-001", []string{"--verdict", "edit", "--edited", edited},
				[]string{"--kind", "meaning", "--model", "haiku", "--scope-context", "no_known_change", "--scope-metric", "conversion_count"},
			},
			func(*llmmock.MockClient) {},
			want{1, `^$`, `^nodloop knowledge: ` + diagnose.ErrQuietMetricScope.Error(), []knowledge.Knowledge{}},
		},
		{
			"an approved review is no correction",
			args{"tq-005", []string{"--verdict", "approve"}, []string{"--kind", "meaning", "--content", "c"}},
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
			require.Equal(t, 0, runDiagnose([]string{"--event", tc.args.event, "--knowledge", "none"}, getenv, client, now, io.Discard, &stderr), stderr.String())
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
