package llm_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/llm"
)

func TestClaudeCLIComplete(t *testing.T) {
	t.Parallel()
	testdata, err := filepath.Abs("testdata")
	require.NoError(t, err)
	type want struct {
		res   llm.Response
		args  []string
		stdin string
	}
	success := llm.Response{
		Output:       json.RawMessage(`{"answer":42}`),
		CostUSD:      0.037367000000000004,
		InputTokens:  9,
		OutputTokens: 153,
		CacheRead:    30230,
		CacheCreate:  16785,
		Duration:     2839 * time.Millisecond,
	}
	tcs := []struct {
		name string
		args llm.Request
		want want
	}{
		{
			name: "every request field reaches claude and the success fixture becomes the response",
			args: llm.Request{
				System: "be terse", Prompt: "hello", Schema: json.RawMessage(`{"type":"object"}`), Model: "haiku",
			},
			want: want{
				res: success,
				args: []string{
					"-p", "--output-format", "json", "--json-schema", `{"type":"object"}`, "--max-turns", "3",
					"--max-budget-usd", "0.5", "--no-session-persistence", "--safe-mode", "--model", "haiku",
					"--append-system-prompt", "be terse",
				},
				stdin: "hello",
			},
		},
		{
			name: "a request without model and system runs the default model without a system prompt",
			args: llm.Request{Prompt: "x", Schema: json.RawMessage(`{}`)},
			want: want{
				res: success,
				args: []string{
					"-p", "--output-format", "json", "--json-schema", "{}", "--max-turns", "3", "--max-budget-usd", "0.5",
					"--no-session-persistence", "--safe-mode", "--model", "sonnet",
				},
				stdin: "x",
			},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			bin := filepath.Join(dir, "claude")
			script := "#!/bin/sh\n" +
				"printf '%s\\n' \"$@\" > \"" + dir + "/args\"\n" +
				"/bin/cat > \"" + dir + "/stdin\"\n" +
				"/bin/cat \"" + testdata + "/success.json\"\n"
			require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
			c := llm.NewClaudeCLI(bin, "sonnet", filepath.Join(dir, "work"), 0)

			got, err := c.Complete(ctx, tc.args)
			require.NoError(t, err)
			args, err := os.ReadFile(filepath.Join(dir, "args"))
			require.NoError(t, err)
			stdin, err := os.ReadFile(filepath.Join(dir, "stdin"))
			require.NoError(t, err)
			assert.Equal(t, tc.want.res, got)
			assert.Equal(t, tc.want.args, strings.Split(strings.TrimSuffix(string(args), "\n"), "\n"))
			assert.Equal(t, tc.want.stdin, string(stdin))
		})
	}
}

func TestClaudeCLICompleteFailure(t *testing.T) {
	t.Parallel()
	type args struct {
		req     llm.Request
		script  string
		workDir string
	}
	type want struct {
		err     error
		message string
	}
	schema := json.RawMessage(`{}`)
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "a request without schema is refused before claude runs",
			args: args{req: llm.Request{Prompt: "x"}, script: "exit 0"},
			want: want{err: llm.ErrSchemaRequired, message: "llm: schema is required"},
		},
		{
			name: "a work dir that cannot be created is refused",
			args: args{req: llm.Request{Prompt: "x", Schema: schema}, script: "exit 0", workDir: "claude/work"},
			want: want{err: llm.ErrWorkDir, message: "llm: work dir: mkdir "},
		},
		{
			name: "exit 1 is a claude failure carrying stderr",
			args: args{
				req:    llm.Request{Prompt: "x", Schema: schema},
				script: "echo 'Not logged in. Run claude login' >&2; exit 1",
			},
			want: want{
				err:     llm.ErrClaudeFailed,
				message: "llm: claude failed: exit status 1: Not logged in. Run claude login",
			},
		},
		{
			name: "stderr longer than the limit is cut to its head",
			args: args{req: llm.Request{Prompt: "x", Schema: schema}, script: "printf '%0250d' 0 >&2; exit 1"},
			want: want{
				err:     llm.ErrClaudeFailed,
				message: "llm: claude failed: exit status 1: " + strings.Repeat("0", 200) + "...",
			},
		},
		{
			name: "multibyte stderr is cut by characters so no character is split",
			args: args{req: llm.Request{Prompt: "x", Schema: schema}, script: "printf '€%.0s' $(seq 250) >&2; exit 1"},
			want: want{
				err:     llm.ErrClaudeFailed,
				message: "llm: claude failed: exit status 1: " + strings.Repeat("€", 200) + "...",
			},
		},
		{
			name: "plain text on stdout is not a result",
			args: args{req: llm.Request{Prompt: "x", Schema: schema}, script: "echo plain text"},
			want: want{
				err:     llm.ErrNotResult,
				message: "llm: claude output is not result json: invalid character 'p' looking for beginning of value: ",
			},
		},
		{
			name: "json of another type is not a result",
			args: args{req: llm.Request{Prompt: "x", Schema: schema}, script: `echo '{"type":"assistant"}'`},
			want: want{err: llm.ErrNotResult, message: `llm: claude output is not result json: type "assistant": `},
		},
		{
			name: "a success without structured output is refused",
			args: args{
				req:    llm.Request{Prompt: "x", Schema: schema},
				script: `echo '{"type":"result","subtype":"success","structured_output":null}'`,
			},
			want: want{err: llm.ErrNoOutput, message: "llm: claude result has no structured_output: "},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			bin := filepath.Join(dir, "claude")
			script := "#!/bin/sh\n/bin/cat > /dev/null\n" + tc.args.script + "\n"
			require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
			c := llm.NewClaudeCLI(bin, "sonnet", filepath.Join(dir, tc.args.workDir), 0)

			got, err := c.Complete(ctx, tc.args.req)
			assert.Equal(t, llm.Response{}, got)
			assert.ErrorIs(t, err, tc.want.err)
			assert.ErrorContains(t, err, tc.want.message)
		})
	}
}

// claude prints a result JSON and may still exit 1 when a budget or turn limit ends the run
func TestClaudeCLICompleteResultError(t *testing.T) {
	t.Parallel()
	testdata, err := filepath.Abs("testdata")
	require.NoError(t, err)
	tcs := []struct {
		name string
		args string
		want *llm.ResultError
	}{
		{
			name: "the max turns fixture with exit 1 becomes a result error",
			args: `/bin/cat "$testdata/error_max_turns.json"; exit 1`,
			want: &llm.ResultError{
				Subtype: "error_max_turns", Errors: []string{"Reached maximum number of turns (3)"}, CostUSD: 0.0261659,
			},
		},
		{
			name: "the max budget fixture with exit 0 becomes a result error",
			args: `/bin/cat "$testdata/error_max_budget.json"`,
			want: &llm.ResultError{
				Subtype: "error_max_budget_usd", Errors: []string{"Reached maximum budget ($0.05)"}, CostUSD: 0.094031,
			},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			bin := filepath.Join(dir, "claude")
			script := "#!/bin/sh\ntestdata=\"" + testdata + "\"\n/bin/cat > /dev/null\n" + tc.args + "\n"
			require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
			c := llm.NewClaudeCLI(bin, "sonnet", dir, 0)

			got, err := c.Complete(ctx, llm.Request{Prompt: "x", Schema: json.RawMessage(`{}`)})
			assert.Equal(t, llm.Response{}, got)
			assert.Equal(t, tc.want, err)
		})
	}
}

// The context ends a running claude and Complete returns well before the script would finish
func TestClaudeCLICompleteStops(t *testing.T) {
	t.Parallel()
	type args struct {
		timeout     time.Duration
		cancelAfter time.Duration
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{
			name: "cancelling the caller context stops claude",
			args: args{cancelAfter: 100 * time.Millisecond},
			want: context.Canceled,
		},
		{
			name: "the timeout stops claude",
			args: args{timeout: 100 * time.Millisecond, cancelAfter: time.Minute},
			want: context.DeadlineExceeded,
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			bin := filepath.Join(dir, "claude")
			require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nsleep 10\n"), 0o755))
			c := llm.NewClaudeCLI(bin, "sonnet", dir, tc.args.timeout)
			callCtx, cancel := context.WithCancel(ctx)
			t.Cleanup(cancel)
			timer := time.AfterFunc(tc.args.cancelAfter, cancel)
			t.Cleanup(func() { timer.Stop() })

			start := time.Now()
			got, err := c.Complete(callCtx, llm.Request{Prompt: "x", Schema: json.RawMessage(`{}`)})
			assert.Equal(t, llm.Response{}, got)
			assert.ErrorIs(t, err, tc.want)
			assert.Less(t, time.Since(start), 2*time.Second)
		})
	}
}

// Environment variables change the process so these cases run one after another
func TestClaudeCLICompleteDefaults(t *testing.T) {
	testdata, err := filepath.Abs("testdata")
	require.NoError(t, err)
	workDir, err := filepath.EvalSymlinks(os.TempDir())
	require.NoError(t, err)
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"" + dir + "/args\"\n" +
		"pwd -P > \"" + dir + "/pwd\"\n" +
		"/bin/cat > /dev/null\n" +
		"/bin/cat \"" + testdata + "/success.json\"\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	type args struct {
		bin   string
		model string
		path  string
	}
	type want struct {
		args    []string
		workDir string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "the arguments name the binary and the model",
			args: args{bin: bin, model: "opus", path: "/nonexistent"},
			want: want{
				args: []string{
					"-p", "--output-format", "json", "--json-schema", "{}", "--max-turns", "3", "--max-budget-usd", "0.5",
					"--no-session-persistence", "--safe-mode", "--model", "opus",
				},
				workDir: filepath.Join(workDir, "nodloop-llm"),
			},
		},
		{
			name: "empty arguments run claude from PATH with sonnet",
			args: args{path: dir},
			want: want{
				args: []string{
					"-p", "--output-format", "json", "--json-schema", "{}", "--max-turns", "3", "--max-budget-usd", "0.5",
					"--no-session-persistence", "--safe-mode", "--model", "sonnet",
				},
				workDir: filepath.Join(workDir, "nodloop-llm"),
			},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PATH", tc.args.path)
			c := llm.NewClaudeCLI(tc.args.bin, tc.args.model, "", 0)

			_, err := c.Complete(ctx, llm.Request{Prompt: "x", Schema: json.RawMessage(`{}`)})
			require.NoError(t, err)
			args, err := os.ReadFile(filepath.Join(dir, "args"))
			require.NoError(t, err)
			pwd, err := os.ReadFile(filepath.Join(dir, "pwd"))
			require.NoError(t, err)
			assert.Equal(t, tc.want.args, strings.Split(strings.TrimSuffix(string(args), "\n"), "\n"))
			assert.Equal(t, tc.want.workDir, strings.TrimSpace(string(pwd)))
		})
	}
}

// A real claude call spends plan usage so it runs only when enabled by env
func TestLiveComplete(t *testing.T) {
	if os.Getenv("NODLOOP_LLM_LIVE") == "" {
		t.Skip("set NODLOOP_LLM_LIVE=1 to run a real claude -p call")
	}
	c := llm.NewClaudeCLI("", "", "", 0)
	res, err := c.Complete(context.Background(), llm.Request{
		Prompt: "Return the number 42 as the field answer.",
		Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`),
		Model:  "haiku",
	})
	require.NoError(t, err)
	var out struct {
		Answer int `json:"answer"`
	}
	require.NoError(t, json.Unmarshal(res.Output, &out), "output = %s", res.Output)
	assert.Equal(t, 42, out.Answer, "output = %s", res.Output)
	assert.Positive(t, res.CostUSD)
	t.Logf(
		"cost $%.4f cache_read %d cache_create %d duration %s",
		res.CostUSD, res.CacheRead, res.CacheCreate, res.Duration,
	)
}
