package diagnose_test

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// Panics on the append of a revise trace
// The revise runs under the record lock so the panic lands where a held lock would block every later review
type panickingTraces struct {
	*tracefile.Store
}

func (p panickingTraces) Append(ctx context.Context, tr trace.Trace) error {
	if tr.Name == trace.NameRevise {
		panic("store failed")
	}
	return p.Store.Append(ctx, tr)
}

// The pool rules eval and compaction replays share
// Every job runs under the replay tag so the log reads its session tags
// After every run a later review must still record so no panic leaves the record lock held
func TestRunAll(t *testing.T) {
	type args struct {
		events   []string
		parallel int
		// Model failure per event
		failures map[string]error
		// Panics on the model call of this event
		panics string
		// Answered with a review that is sent back so its revise trace panics
		sentBack string
	}
	type want struct {
		subjects []string
		errs     []string
		log      string
		err      error
	}
	noAction := llm.Response{
		Output:  json.RawMessage(`{"status":"no_action","observations":[],"causes":[],"checks":[],"open_questions":[]}`),
		CostUSD: 0.01,
	}
	uncited := llm.Response{
		Output: json.RawMessage(`{"status":"ready_for_review","observations":[],` +
			`"causes":[{"summary":"a cause","paragraph_ids":[]}],"checks":[],"open_questions":[]}`),
	}
	maxTurns := &llm.ResultError{Subtype: "error_max_turns"}
	eventRe := regexp.MustCompile(`# Event (tq-\d+)\n`)
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"results come back in job order with one log line each",
			args{events: []string{"tq-005", "tq-001"}, parallel: 2},
			want{
				subjects: []string{"tq-005", "tq-001"}, errs: []string{"", ""},
				log: "tq-005\treplay\tno_action\tforced=false\t$0.0100\t0ms\n" +
					"tq-001\treplay\tno_action\tforced=false\t$0.0100\t0ms\n",
			},
		},
		{
			"a failed review keeps its failed trace and the run goes on",
			args{events: []string{"tq-001", "tq-002"}, parallel: 1, failures: map[string]error{"tq-001": maxTurns}},
			want{
				subjects: []string{"tq-001", "tq-002"}, errs: []string{"claude result error_max_turns", ""},
				log: "tq-001\treplay\tfailed: claude result error_max_turns\n" +
					"tq-002\treplay\tno_action\tforced=false\t$0.0100\t0ms\n",
			},
		},
		{
			"a failure without a trace stops the run",
			args{events: []string{"tq-999", "tq-002"}, parallel: 1},
			want{err: diagnose.ErrNoFailedTrace},
		},
		{
			"a panic becomes the error of its job",
			args{events: []string{"tq-001"}, parallel: 1, panics: "tq-001"},
			want{err: diagnose.ErrReviewPanicked},
		},
		{
			"a panic under the record lock releases the lock",
			args{events: []string{"tq-001"}, parallel: 1, sentBack: "tq-001"},
			want{err: diagnose.ErrReviewPanicked},
		},
		{
			"a negative parallel is refused before any review",
			args{events: []string{"tq-001"}, parallel: -1},
			want{err: diagnose.ErrNegativeParallel},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, req llm.Request) (llm.Response, error) {
					event := eventRe.FindStringSubmatch(req.Prompt)[1]
					if event == tc.args.panics {
						panic("boom")
					}
					if event == tc.args.sentBack {
						return uncited, nil
					}
					return noAction, tc.args.failures[event]
				}).AnyTimes()
			at := s.Clock.Now()
			d := diagnose.New(
				s.Source, testkit.Policy(t), client, panickingTraces{s.Traces}, s.Feedback, s.Ledger,
				func() time.Time { return at },
			)
			var jobs []diagnose.Job
			for _, id := range tc.args.events {
				opts := diagnose.BatchOptions{Session: diagnose.Session{ID: "c-1", Tags: []string{"replay"}}}
				jobs = append(jobs, diagnose.Job{EventID: id, Options: opts})
			}
			var log bytes.Buffer

			results, err := d.RunAll(ctx, jobs, tc.args.parallel, &log)
			assert.ErrorIs(t, err, tc.want.err)
			var subjects, errs []string
			for _, res := range results {
				tr, err := s.Traces.Get(ctx, res.TraceID)
				require.NoError(t, err)
				assert.Equal(t, trace.NameDiagnose, tr.Name)
				subjects, errs = append(subjects, tr.Subject), append(errs, tr.Error)
			}
			later := make(chan error, 1)
			go func() {
				_, err := d.Run(ctx, "tq-003", diagnose.BatchOptions{})
				later <- err
			}()

			assert.Equal(t, tc.want.subjects, subjects)
			assert.Equal(t, tc.want.errs, errs)
			// Parallel reviews may finish in any order and a line is never split
			assert.ElementsMatch(t, strings.SplitAfter(tc.want.log, "\n"), strings.SplitAfter(log.String(), "\n"))
			select {
			case err := <-later:
				assert.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("a later review is blocked on the record lock")
			}
		})
	}
}
