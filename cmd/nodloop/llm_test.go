package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	"github.com/jeon-jihyeon/nodloop/internal/userconfig"
)

func TestRunLLMProbe(t *testing.T) {
	type args struct {
		args []string
		// config.json before the call
		// None when empty
		saved string
		// The model the request carries
		model string
		res   llm.Response
		err   error
		// HOME is unset
		homeless bool
	}
	type want struct {
		code   int
		stdout string
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"success prints the cost and both durations",
			args{
				[]string{"probe", "--model", "sonnet"}, "", "sonnet",
				llm.Response{
					Output: json.RawMessage(`{"answer":42}`), CostUSD: 0.03, InputTokens: 5, Duration: 800 * time.Millisecond,
				},
				nil, false,
			},
			want{
				0, "output {\"answer\":42}\ncost $0.0300\ntokens in 5 out 0 cache_read 0 cache_create 0\n" +
					"duration 800ms (wall 1s)\n", "",
			},
		},
		{
			"no model flag asks haiku of claude",
			args{[]string{"probe"}, "", "haiku", llm.Response{Output: json.RawMessage(`{"answer":42}`)}, nil, false},
			want{
				0, "output {\"answer\":42}\ncost $0.0000\ntokens in 0 out 0 cache_read 0 cache_create 0\n" +
					"duration 0s (wall 1s)\n", "",
			},
		},
		{
			"no model flag on a chat backend asks its configured model",
			args{
				[]string{"probe"}, `{"llm":{"backend":"chat","base_url":"http://localhost:11434/v1","model":"qwen3"}}`, "",
				llm.Response{Output: json.RawMessage(`{"answer":42}`)}, nil, false,
			},
			want{
				0, "output {\"answer\":42}\ncost $0.0000\ntokens in 0 out 0 cache_read 0 cache_create 0\n" +
					"duration 0s (wall 1s)\n", "",
			},
		},
		{
			"wrong answer fails",
			args{[]string{"probe"}, "", "haiku", llm.Response{Output: json.RawMessage(`{"answer":7}`)}, nil, false},
			want{1, "", "nodloop llm probe: wrong answer: 7 in {\"answer\":7}\n"},
		},
		{
			"undecodable output names the decode error",
			args{[]string{"probe"}, "", "haiku", llm.Response{Output: json.RawMessage(`{"answer":"x"}`)}, nil, false},
			want{1, "", "nodloop llm probe: unexpected output: {\"answer\":\"x\"}: " +
				"json: cannot unmarshal string into Go struct field .answer of type int\n"},
		},
		{
			"call failure fails",
			args{[]string{"probe"}, "", "haiku", llm.Response{}, assert.AnError, false},
			want{1, "", "nodloop llm probe: " + assert.AnError.Error() + "\n"},
		},
		{
			"no home asks haiku of claude",
			args{[]string{"probe"}, "", "haiku", llm.Response{Output: json.RawMessage(`{"answer":42}`)}, nil, true},
			want{
				0, "output {\"answer\":42}\ncost $0.0000\ntokens in 0 out 0 cache_read 0 cache_create 0\n" +
					"duration 0s (wall 1s)\n", "",
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := savedHome(t, tc.args.saved)
			client := llmmock.NewMockClient(gomock.NewController(t))
			req := llm.Request{
				Prompt: "Return the number 42 as the field answer.", Schema: json.RawMessage(probeSchema), Model: tc.args.model,
			}
			client.EXPECT().Complete(gomock.Any(), req).Return(tc.args.res, tc.args.err)
			var stdout, stderr bytes.Buffer

			code := runLLM(tc.args.args, llmEnv(home, tc.args.homeless), client, testkit.Open(t).Clock.Now, trace.ProofNone, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code)
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Equal(t, tc.want.stderr, stderr.String())
		})
	}
}

func TestRunLLM(t *testing.T) {
	type args struct {
		args []string
		// config.json before the call
		// None when empty
		saved string
		// HOME is unset
		homeless bool
	}
	type want struct {
		code   int
		stdout string
		// Text stderr contains
		stderr string
		// config.json after the call
		// Empty when there is none
		saved string
	}
	chat := "{\n  \"llm\": {\n    \"backend\": \"chat\",\n    \"base_url\": \"http://localhost:11434/v1\",\n    \"model\": \"qwen3\",\n" +
		"    \"key_env\": \"LLM_KEY\"\n  }\n}\n"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"set chat saves the endpoint without the key",
			args{[]string{"set", "chat", "--base-url", "http://localhost:11434/v1", "--model", "qwen3", "--key-env", "LLM_KEY"}, "", false},
			want{0, "llm\tchat\thttp://localhost:11434/v1\tmodel qwen3\tkey $LLM_KEY\n", "", chat}},
		{"set claude replaces a chat endpoint", args{[]string{"set", "claude", "--model", "haiku"}, chat, false},
			want{0, "llm\tclaude\tmodel haiku\n", "", "{\n  \"llm\": {\n    \"backend\": \"claude\",\n    \"model\": \"haiku\"\n  }\n}\n"}},
		{"set claude alone saves the backend", args{[]string{"set", "claude"}, "", false},
			want{0, "llm\tclaude\n", "", "{\n  \"llm\": {\n    \"backend\": \"claude\"\n  }\n}\n"}},
		{"unset removes the key and keeps the others", args{[]string{"unset"}, `{"approver":"ann","llm":{"backend":"chat"}}`, false},
			want{0, "llm\tclaude (default)\n", "", "{\n  \"approver\": \"ann\"\n}\n"}},
		{"set without a backend is refused", args{[]string{"set"}, "", false}, want{1, "", "a backend is required", ""}},
		{"set chat without a model is refused", args{[]string{"set", "chat", "--base-url", "http://localhost:11434/v1"}, "", false},
			want{1, "", "a chat backend needs a model", ""}},
		{"set chat without an absolute URL is refused", args{[]string{"set", "chat", "--base-url", "localhost:11434", "--model", "qwen3"}, "", false},
			want{1, "", "base_url is not an absolute http or https URL", ""}},
		{"set of another backend is refused", args{[]string{"set", "responses"}, "", false}, want{1, "", "unknown backend", ""}},
		{"set without a home is refused", args{[]string{"set", "claude"}, "", true}, want{1, "", "home directory unknown", ""}},
		{"unset without a home is refused", args{[]string{"unset"}, "", true}, want{1, "", "home directory unknown", ""}},
		{"a broken config is not overwritten", args{[]string{"unset"}, "{broken", false}, want{1, "", "is not valid JSON", "{broken"}},
		{"a broken config keeps set from saving", args{[]string{"set", "claude"}, "{broken", false}, want{1, "", "is not valid JSON", "{broken"}},
		{"a broken config fails the probe", args{[]string{"probe"}, "{broken", false}, want{1, "", "is not valid JSON", "{broken"}},
		{"another action is refused", args{[]string{"list"}, "", false}, want{1, "", "unknown action", ""}},
		{"no action is refused", args{nil, "", false}, want{1, "", "an action is required", ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := savedHome(t, tc.args.saved)
			var stdout, stderr bytes.Buffer

			code := runLLM(tc.args.args, llmEnv(home, tc.args.homeless), nil, testkit.Open(t).Clock.Now, atTerminal, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code)
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			saved, _ := os.ReadFile(home.config().ConfigPath())
			assert.Equal(t, tc.want.saved, string(saved))
		})
	}
}

func TestLLMClient(t *testing.T) {
	type args struct {
		env map[string]string
		cfg llm.Config
	}
	type want struct {
		// The claude client built
		// Nil for chat or an error
		claude *llm.ClaudeCLI
		// A chat client was built
		chat bool
		err  error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an absent backend leaves the llm defaults", args{nil, llm.Config{}}, want{claude: llm.NewClaudeCLI("", "", "", 0)}},
		{"env names the binary and the default model", args{map[string]string{envClaudeBin: "/opt/claude", envLLMModel: "opus"}, llm.Config{}},
			want{claude: llm.NewClaudeCLI("/opt/claude", "opus", "", 0)}},
		{"the claude model of the config is the default", args{nil, llm.Config{Backend: llm.BackendClaude, Model: "haiku"}},
			want{claude: llm.NewClaudeCLI("", "haiku", "", 0)}},
		{"NODLOOP_LLM_MODEL wins over the claude model of the config",
			args{map[string]string{envLLMModel: "opus"}, llm.Config{Backend: llm.BackendClaude, Model: "haiku"}},
			want{claude: llm.NewClaudeCLI("", "opus", "", 0)}},
		{"chat builds a chat client", args{nil, llm.Config{Backend: llm.BackendChat, BaseURL: "http://localhost:11434/v1", Model: "qwen3"}},
			want{chat: true}},
		{"chat without a base URL fails", args{nil, llm.Config{Backend: llm.BackendChat}}, want{err: llm.ErrBaseURLInvalid}},
		{"another backend fails", args{nil, llm.Config{Backend: "responses"}}, want{err: llm.ErrBackendUnknown}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := llmClient(envOf(tc.args.env), tc.args.cfg)

			assert.ErrorIs(t, err, tc.want.err)
			claude, _ := got.(*llm.ClaudeCLI)
			assert.Equal(t, tc.want.claude, claude)
			_, chat := got.(*llm.ChatCompletions)
			assert.Equal(t, tc.want.chat, chat)
		})
	}
}

// The chat client sends the model of the config unless the request names one and the key its env holds
func TestLLMClientChat(t *testing.T) {
	type args struct {
		env   map[string]string
		model string
	}
	type want struct {
		model string
		auth  string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the model of the config without a key", args{nil, ""}, want{"qwen3", ""}},
		{"--model wins over the config", args{nil, "llama3"}, want{"llama3", ""}},
		{"the key of the env named", args{map[string]string{"LLM_KEY": "k1"}, ""}, want{"qwen3", "Bearer k1"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model, auth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				model, auth = body.Model, r.Header.Get("Authorization")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"answer\":42}"}}]}`))
			}))
			t.Cleanup(srv.Close)
			client, err := llmClient(envOf(tc.args.env), llm.Config{Backend: llm.BackendChat, BaseURL: srv.URL, Model: "qwen3", KeyEnv: "LLM_KEY"})
			require.NoError(t, err)

			res, err := client.Complete(context.Background(), llm.Request{Prompt: "p", Schema: json.RawMessage(probeSchema), Model: tc.args.model})

			assert.NoError(t, err)
			assert.JSONEq(t, `{"answer":42}`, string(res.Output))
			assert.Equal(t, tc.want, want{model, auth})
		})
	}
}

// A config that builds no client fails the model call and nothing before it
func TestConfiguredLLM(t *testing.T) {
	type args struct {
		// config.json under HOME and none when empty
		saved string
		// HOME is unset
		homeless bool
	}
	type want struct {
		// The claude client built and nil for any other
		claude *llm.ClaudeCLI
		// A chat client was built
		chat bool
		// The error of a model call on a client the config did not build
		err error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an unknown backend fails the call", args{`{"llm":{"backend":"responses"}}`, false}, want{err: llm.ErrBackendUnknown}},
		{"chat without a base URL fails the call", args{`{"llm":{"backend":"chat"}}`, false}, want{err: llm.ErrBaseURLInvalid}},
		{"a broken config fails the call", args{"{broken", false}, want{err: userconfig.ErrInvalid}},
		{"no home builds claude", args{"", true}, want{claude: llm.NewClaudeCLI("", "", "", 0)}},
		{"a claude config builds claude with its model", args{`{"llm":{"backend":"claude","model":"haiku"}}`, false}, want{claude: llm.NewClaudeCLI("", "haiku", "", 0)}},
		{"a chat config builds the chat client", args{`{"llm":{"backend":"chat","base_url":"http://localhost:11434/v1","model":"qwen3"}}`, false}, want{chat: true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := savedHome(t, tc.args.saved)

			got := configuredLLM(llmEnv(home, tc.args.homeless))

			claude, _ := got.(*llm.ClaudeCLI)
			assert.Equal(t, tc.want.claude, claude)
			_, chat := got.(*llm.ChatCompletions)
			assert.Equal(t, tc.want.chat, chat)
			// Only a failed client is called since the others would reach a model
			failed, _ := got.(failedLLM)
			assert.ErrorIs(t, testkit.Err(failed.Complete(context.Background(), llm.Request{})), tc.want.err)
		})
	}
}

// The env of a home or of none when homeless
func llmEnv(home homeDir, homeless bool) func(string) string {
	if homeless {
		return envOf(nil)
	}
	return envOf(map[string]string{"HOME": string(home)})
}

// A home with config.json holding saved or none when saved is empty
func savedHome(t *testing.T, saved string) homeDir {
	t.Helper()
	home := homeDir(t.TempDir())
	require.NoError(t, os.MkdirAll(home.dir(), 0o755))
	if saved != "" {
		require.NoError(t, os.WriteFile(home.config().ConfigPath(), []byte(saved), 0o600))
	}
	return home
}

func envOf(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}
