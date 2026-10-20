package loop_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// One simulated report with the arms drawn per session and no real effect
// 1. a session of about 20 turns draws its arm once through the holdout of the hooks
// 2. each session corrects at its own rate so the turns of a session are alike
// 3. half the turns are judged
func simulatedEffect(rng *rand.Rand, rep, sessions int, share knowledge.Holdout) loop.EffectRow {
	gitC := []knowledge.Ref{{ID: "git-c", Version: 1}}
	var traces trace.Traces
	var verdicts feedback.Records
	for s := range sessions {
		key := fmt.Sprintf("rep%d-session%d", rep, s)
		applied, withheld := gitC, []knowledge.Ref(nil)
		if share.WithholdsRun("bot", key) {
			applied, withheld = nil, gitC
		}
		in, err := knowledge.NewRunInput(applied, withheld, "")
		if err != nil {
			panic(err)
		}
		raw, err := json.Marshal(share.Drawn(in, "bot", key))
		if err != nil {
			panic(err)
		}
		corrects := rng.Float64() * 0.6
		for turn := 0; turn == 0 || rng.Float64() >= 1.0/20; turn++ {
			id := fmt.Sprintf("%s-%d", key, turn)
			traces = append(traces, trace.Trace{ID: id, Name: trace.NameRun, Producer: "bot", Input: raw})
			if rng.Float64() < 0.5 {
				continue
			}
			verdict := feedback.VerdictApprove
			if rng.Float64() < corrects {
				verdict = feedback.VerdictReject
			}
			verdicts = append(verdicts, feedback.Feedback{TraceID: id, Verdict: verdict, Reviewer: "ann"})
		}
	}
	return loop.NewRuns(traces, verdicts).Effect(nil, nil, nil, time.Time{})[0]
}

// With 100 sessions of about 20 turns under a share of 0.1 and no real imbalance or effect
// the sample ratio check fails at about its nominal rate and the difference interval covers zero
// Counting turns as independent draws gives 171 of these 300 reports p below 0.001
// and leaves 77 percent of the 95 percent intervals over zero
func TestRunsEffectClusteredBySession(t *testing.T) {
	if testing.Short() {
		t.Skip("a simulation of 300 reports")
	}
	rng := rand.New(rand.NewPCG(1, 2))
	reps := 300
	var alarms, alarms05, judgedAlarms, differences, covered int
	for rep := range reps {
		row := simulatedEffect(rng, rep, 100, 0.1)
		require.NotNil(t, row.SRMP)
		alarms += boolInt(*row.SRMP < loop.SRMThreshold)
		alarms05 += boolInt(*row.SRMP < 0.05)
		judgedAlarms += boolInt(slices.Contains(row.Warnings, loop.WarnJudgedRate))
		if row.Difference == nil {
			continue
		}
		differences++
		covered += boolInt(*row.DifferenceLow <= 0 && *row.DifferenceHigh >= 0)
	}
	t.Logf("srm p below 0.001 %d, below 0.05 %d, judged rate warnings %d of %d reports, intervals over zero %d of %d",
		alarms, alarms05, judgedAlarms, reps, covered, differences)

	assert.LessOrEqual(t, float64(alarms)/float64(reps), 0.01, "srm p below 0.001")
	assert.InDelta(t, 0.05, float64(alarms05)/float64(reps), 0.03, "srm p below 0.05")
	assert.LessOrEqual(t, float64(judgedAlarms)/float64(reps), 0.1, "judged rate warnings at 0.05")
	assert.GreaterOrEqual(t, float64(covered)/float64(differences), 0.85, "intervals over zero")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
