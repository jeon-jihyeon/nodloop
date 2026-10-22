package curate_test

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// One real judge call of the three claude variants spends plan usage so it runs only when enabled by env
// 1. the model is NODLOOP_LLM_MODEL or Sonnet 5.5 since Haiku 4.5 has no effort parameter and claude runs it without one
// 2. NODLOOP_CLAUDE_BIN names another claude binary
func TestLiveJudge(t *testing.T) {
	if os.Getenv("NODLOOP_LLM_LIVE") == "" {
		t.Skip("set NODLOOP_LLM_LIVE=1 to run a real judge call")
	}
	ctx := context.Background()
	now := time.Now().UTC()
	decisions := curate.Shots{
		{Ref: ref("k-a0"), At: now.Add(-48 * time.Hour), Content: "Name the failing test in the first line of a fix summary", Kept: true},
		{Ref: ref("k-r0"), At: now.Add(-24 * time.Hour), Content: "Skip the tests when a change only touches comments"},
	}
	meter := llm.NewMeter(llm.NewClaudeCLI(os.Getenv("NODLOOP_CLAUDE_BIN"), "", "", 0))
	settings := curate.DefaultJudge
	settings.Model = cmp.Or(os.Getenv("NODLOOP_LLM_MODEL"), "claude-sonnet-5-5")
	cfg := curate.JudgeConfig{}.Next(settings, decisions, "", now)
	members, err := cfg.Members(meter, decisions)
	require.NoError(t, err)
	traces, err := tracefile.New(t.TempDir())
	require.NoError(t, err)
	seal := func(text string) string {
		sum := sha256.Sum256([]byte(text))
		return hex.EncodeToString(sum[:])
	}
	judge := curate.NewJudge(classify.NewPanel(classify.PointJudge, members, traces, seal, time.Now), cfg.Answered)

	got, err := judge.Judge(ctx, "run-live", curate.Case{
		Change: "add: run golangci-lint before go test when a Go file changed",
		Anchor: []string{"Run the linter before the tests in every change", "Never push to the main branch directly"},
		Words:  []string{"lint the Go files before you run go test"},
	})

	require.NoError(t, err)
	require.NoError(t, got.Err)
	recorded, err := traces.Get(ctx, got.Trace)
	require.NoError(t, err)
	var out struct {
		Members []struct {
			Name    string           `json:"name"`
			Model   string           `json:"model"`
			Effort  string           `json:"effort"`
			Answers classify.Answers `json:"answers"`
		} `json:"members"`
	}
	require.NoError(t, json.Unmarshal(recorded.Output, &out))
	require.Len(t, out.Members, 3)
	for _, m := range out.Members {
		assert.True(t, strings.HasPrefix(m.Model, "claude-"), "model of %s = %q", m.Name, m.Model)
		assert.Equal(t, curate.JudgeEffort, m.Effort, m.Name)
		assert.Len(t, m.Answers, 2, m.Name)
	}
	assert.True(t, got.Outcome.Valid())
	spent := meter.Take()
	t.Logf("outcome %s share %.2f bin %s", got.Outcome, got.Share, got.Bin)
	t.Logf("cost $%.4f input %d output %d thinking %d cache_read %d cache_create %d duration %s",
		spent.CostUSD, spent.InputTokens, spent.OutputTokens, spent.ThinkingTokens, spent.CacheRead, spent.CacheCreate, spent.Duration)
	t.Logf("trace input %s", recorded.Input)
	t.Logf("trace output %s", recorded.Output)
}
