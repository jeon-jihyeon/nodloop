package llm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/llm"
)

const answerSchema = `{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`

// A clock that steps a second on every read so the wall time of a call is known
func steppingClock() func() time.Time {
	var mu sync.Mutex
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Second)
		return now
	}
}

// A chat completion whose first choice says the content with the usage given
func completion(content string, prompt, completionTokens, cached, reasoning int) string {
	b, _ := json.Marshal(content)
	return fmt.Sprintf(`{"id":"c1","object":"chat.completion","model":"qwen3:7b","choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":%d,"completion_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d},"completion_tokens_details":{"reasoning_tokens":%d}}}`,
		b, prompt, completionTokens, cached, reasoning)
}

// What the endpoint received
type received struct {
	Model          string `json:"model"`
	Messages       []map[string]string
	ResponseFormat struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
			Strict *bool           `json:"strict"`
		} `json:"json_schema"`
	} `json:"response_format"`
	Path          string `json:"-"`
	Authorization string `json:"-"`
}

// One received request by the fields a call must send
type sent struct {
	path       string
	model      string
	auth       string
	format     string
	schemaName string
	// The schema in compact JSON
	schema string
	// Nil since strict mode refuses a schema with an optional property
	strict *bool
	roles  []string
	// The content of the first user message
	prompt string
}

func compactJSON(raw []byte) string {
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return string(raw)
	}
	return out.String()
}

func (r received) sent() sent {
	s := sent{
		path: r.Path, model: r.Model, auth: r.Authorization, format: r.ResponseFormat.Type,
		schemaName: r.ResponseFormat.JSONSchema.Name, schema: compactJSON(r.ResponseFormat.JSONSchema.Schema),
		strict: r.ResponseFormat.JSONSchema.Strict,
	}
	for _, m := range r.Messages {
		s.roles = append(s.roles, m["role"])
		if m["role"] == "user" && s.prompt == "" {
			s.prompt = m["content"]
		}
	}
	return s
}

// The requests a call of req sends with the roles of each in order
func sends(model, auth string, req llm.Request, roles ...[]string) []sent {
	var out []sent
	for _, r := range roles {
		out = append(out, sent{
			path: "/v1/chat/completions", model: model, auth: auth, format: "json_schema", schemaName: "answer",
			schema: compactJSON(req.Schema), roles: r, prompt: req.Prompt,
		})
	}
	return out
}

// The text of the error and empty for none
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestChatCompletionsComplete(t *testing.T) {
	t.Parallel()
	request := llm.Request{System: "be terse", Prompt: "hello", Schema: json.RawMessage(answerSchema), Model: "qwen3"}
	bare := llm.Request{Prompt: "hello", Schema: json.RawMessage(answerSchema)}
	first := []string{"system", "user"}
	resent := []string{"system", "user", "assistant", "user"}
	type reply struct {
		status int
		body   string
	}
	type args struct {
		key     string
		req     llm.Request
		replies []reply
	}
	type want struct {
		err error
		// Text the error carries
		errText string
		res     llm.Response
		// Each request the endpoint received
		sent []sent
	}
	ok := reply{http.StatusOK, completion(`{"answer": 42}`, 30, 5, 10, 2)}
	wrong := reply{http.StatusOK, completion(`{"answer":"x"}`, 40, 6, 0, 0)}
	answered := llm.Response{
		Output: json.RawMessage(`{"answer":42}`), InputTokens: 20, OutputTokens: 5, ThinkingTokens: 2, CacheRead: 10, Duration: time.Second, Model: "qwen3:7b",
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a valid answer becomes the output with its usage and the wall time", args{"", request, []reply{ok}},
			want{res: answered, sent: sends("qwen3", "", request, first)}},
		{"a key is sent as a Bearer token", args{"k1", request, []reply{ok}},
			want{res: answered, sent: sends("qwen3", "Bearer k1", request, first)}},
		{"a request without system and model sends the user message to the default model", args{"", bare, []reply{ok}},
			want{res: answered, sent: sends("default", "", bare, []string{"user"})}},
		{"an answer that fails the schema is sent once more and the usage of both is summed", args{"", request, []reply{wrong, ok}},
			want{res: llm.Response{
				Output: json.RawMessage(`{"answer":42}`), InputTokens: 60, OutputTokens: 11, ThinkingTokens: 2, CacheRead: 10, Duration: time.Second, Model: "qwen3:7b",
			}, sent: sends("qwen3", "", request, first, resent)}},
		{"an answer that is no JSON is sent once more", args{"", request, []reply{{http.StatusOK, completion("forty two", 1, 1, 0, 0)}, ok}},
			want{res: llm.Response{
				Output: json.RawMessage(`{"answer":42}`), InputTokens: 21, OutputTokens: 6, ThinkingTokens: 2, CacheRead: 10, Duration: time.Second, Model: "qwen3:7b",
			}, sent: sends("qwen3", "", request, first, resent)}},
		{"a second failure returns the validation error", args{"", request, []reply{wrong, wrong}},
			want{err: llm.ErrSchemaMismatch, errText: "answer", sent: sends("qwen3", "", request, first, resent)}},
		{"a failed resend returns its error", args{"", request, []reply{wrong, {http.StatusBadGateway, "down"}}},
			want{err: llm.ErrStatus, errText: "error status: 502", sent: sends("qwen3", "", request, first, resent)}},
		{"a non 2xx status names the status alone and never the body", args{"", request, []reply{{http.StatusUnauthorized, "echoed secret prompt"}}},
			want{err: llm.ErrStatus, errText: "error status: 401", sent: sends("qwen3", "", request, first)}},
		{"a body past a megabyte is a wrong endpoint", args{"", request, []reply{{http.StatusOK, strings.Repeat(" ", 1<<20+1)}}},
			want{err: llm.ErrResponseInvalid, errText: "body over", sent: sends("qwen3", "", request, first)}},
		{"a body that is no chat completion fails", args{"", request, []reply{{http.StatusOK, "<html>"}}},
			want{err: llm.ErrResponseInvalid, sent: sends("qwen3", "", request, first)}},
		{"a completion without a choice fails", args{"", request, []reply{{http.StatusOK, `{"choices":[]}`}}},
			want{err: llm.ErrResponseInvalid, errText: "no choice", sent: sends("qwen3", "", request, first)}},
		{"a request without schema sends nothing", args{"", llm.Request{Prompt: "hello"}, nil}, want{err: llm.ErrSchemaRequired}},
		{"a schema that is no JSON sends nothing", args{"", llm.Request{Prompt: "hello", Schema: json.RawMessage(`{`)}, nil},
			want{err: llm.ErrSchemaInvalid}},
		{"a schema that does not resolve sends nothing", args{"", llm.Request{Prompt: "hello", Schema: json.RawMessage(`{"$ref":"#/nowhere"}`)}, nil},
			want{err: llm.ErrSchemaInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var got []sent
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				rec := received{Path: r.URL.Path, Authorization: r.Header.Get("Authorization")}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&rec))
				got = append(got, rec.sent())
				rep := tc.args.replies[len(got)-1]
				w.WriteHeader(rep.status)
				_, _ = w.Write([]byte(rep.body))
			}))
			t.Cleanup(srv.Close)

			res, err := llm.NewChatCompletions(srv.URL+"/v1/", "default", tc.args.key, 0, steppingClock()).Complete(context.Background(), tc.args.req)

			mu.Lock()
			defer mu.Unlock()
			assert.ErrorIs(t, err, tc.want.err)
			assert.Contains(t, errText(err), tc.want.errText)
			assert.NotContains(t, errText(err), "secret", "an error quotes no body an endpoint answered")
			assert.Equal(t, tc.want.res, res)
			assert.Equal(t, tc.want.sent, got)
		})
	}
}

// The resend shows the refused answer and why it was refused
func TestChatCompletionsCompleteResend(t *testing.T) {
	t.Parallel()
	type want struct {
		// The refused answer the resend repeats as the assistant
		answer string
		// Text the refusal of the resend carries
		refusal string
	}
	tcs := []struct {
		name string
		// The content of the first answer
		args string
		want want
	}{
		{"an answer that fails the schema is refused with the validation error", `{"answer":"x"}`, want{`{"answer":"x"}`, "answer"}},
		{"an answer that is no JSON is refused as such", "forty two", want{"forty two", "not JSON"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var got []received
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				var rec received
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&rec))
				got = append(got, rec)
				answers := []string{tc.args, `{"answer":42}`}
				_, _ = w.Write([]byte(completion(answers[len(got)-1], 1, 1, 0, 0)))
			}))
			t.Cleanup(srv.Close)

			_, err := llm.NewChatCompletions(srv.URL, "m", "", 0, steppingClock()).
				Complete(context.Background(), llm.Request{Prompt: "hello", Schema: json.RawMessage(answerSchema)})

			mu.Lock()
			defer mu.Unlock()
			require.NoError(t, err)
			require.Len(t, got, 2)
			require.Len(t, got[1].Messages, 3)
			assert.Equal(t, map[string]string{"role": "assistant", "content": tc.want.answer}, got[1].Messages[1])
			assert.Equal(t, "user", got[1].Messages[2]["role"])
			assert.Contains(t, got[1].Messages[2]["content"], "Your answer was refused: ")
			assert.Contains(t, got[1].Messages[2]["content"], tc.want.refusal)
		})
	}
}

// An answer refused twice fails naming where the schema refused it and never the value it refused
func TestChatCompletionsCompleteRefusedTwice(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		// The content of both answers
		args string
		want string
	}{
		{"a value of the wrong type", `{"answer":"ann said never print it"}`, "llm: answer does not match the schema: /properties/answer: type"},
		{"a value under a rule that quotes it", `{"answer":-1}`, "llm: answer does not match the schema: /properties/answer: minimum"},
		{"a key the schema does not allow", `{"answer":1,"ann said never print it":1}`, "llm: answer does not match the schema: root: unexpected additional properties"},
		{"an answer that is no JSON", "ann said never print it", "llm: answer does not match the schema: not JSON"},
	}
	strict := `{"type":"object","additionalProperties":false,"properties":{"answer":{"type":"integer","minimum":0}},"required":["answer"]}`
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(completion(tc.args, 1, 1, 0, 0)))
			}))
			t.Cleanup(srv.Close)

			_, err := llm.NewChatCompletions(srv.URL, "m", "", 0, steppingClock()).
				Complete(context.Background(), llm.Request{Prompt: "hello", Schema: json.RawMessage(strict)})

			require.ErrorIs(t, err, llm.ErrSchemaMismatch)
			assert.Equal(t, tc.want, err.Error())
		})
	}
}

// A redirect fails with its status so neither the prompt nor the key reaches the endpoint it names
func TestChatCompletionsCompleteRedirect(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		// The redirect status the endpoint answers
		args int
		// Text the error carries
		want string
	}{
		{"a temporary redirect is not followed", http.StatusTemporaryRedirect, "307"},
		{"a permanent redirect is not followed", http.StatusPermanentRedirect, "308"},
		{"a found redirect is not followed", http.StatusFound, "302"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var reached atomic.Bool
			elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached.Store(true)
				_, _ = w.Write([]byte(completion(`{"answer":42}`, 1, 1, 0, 0)))
			}))
			t.Cleanup(elsewhere.Close)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, elsewhere.URL+"/chat/completions", tc.args)
			}))
			t.Cleanup(srv.Close)

			_, err := llm.NewChatCompletions(srv.URL, "m", "k1", 0, steppingClock()).
				Complete(context.Background(), llm.Request{Prompt: "hello", Schema: json.RawMessage(answerSchema)})

			assert.ErrorIs(t, err, llm.ErrStatus)
			assert.Contains(t, errText(err), tc.want)
			assert.False(t, reached.Load())
		})
	}
}

// The context error the call failed with and nil for another error
func contextErr(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		return context.Canceled
	}
	return nil
}

// A timeout or a cancel returns the context error and a closed endpoint or a cut body or a bad URL the error of its own
func TestChatCompletionsCompleteInterrupted(t *testing.T) {
	t.Parallel()
	// Cleanups run last first so the handler returns before Close waits for it
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(slow.Close)
	// The status goes out and the body never ends
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":`))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(stalled.Close)
	t.Cleanup(func() { close(release) })
	// The body ends before the length the header promised
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte(`{"choices":`))
	}))
	t.Cleanup(cut.Close)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	req := llm.Request{Prompt: "hello", Schema: json.RawMessage(answerSchema)}
	type args struct {
		url     string
		ctx     context.Context
		timeout time.Duration
	}
	tcs := []struct {
		name string
		args args
		// The context error and nil when the call fails for another reason
		want error
	}{
		{"a timeout returns the deadline", args{slow.URL, context.Background(), 50 * time.Millisecond}, context.DeadlineExceeded},
		{"a cancel returns the cancel", args{slow.URL, cancelled, time.Minute}, context.Canceled},
		{"a timeout while the body is read returns the deadline", args{stalled.URL, context.Background(), 50 * time.Millisecond},
			context.DeadlineExceeded},
		{"a closed endpoint returns the transport error", args{closed.URL, context.Background(), time.Minute}, nil},
		{"a body cut short returns the read error", args{cut.URL, context.Background(), time.Minute}, nil},
		{"a base URL no request can be built from returns the parse error", args{"http://bad host", context.Background(), time.Minute}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Wall time since the deadline is the real one of the context and no fake clock drives it
			start := time.Now()

			_, err := llm.NewChatCompletions(tc.args.url, "m", "", tc.args.timeout, steppingClock()).Complete(tc.args.ctx, req)

			require.Error(t, err)
			assert.Equal(t, tc.want, contextErr(err))
			assert.Less(t, time.Since(start), 5*time.Second)
		})
	}
}

func TestConfigCheck(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		args llm.Config
		want error
	}{
		{"an absent backend reads as claude", llm.Config{}, nil},
		{"claude needs no base URL", llm.Config{Backend: llm.BackendClaude, Model: "haiku"}, nil},
		{"chat with an http base URL", llm.Config{Backend: llm.BackendChat, BaseURL: "http://localhost:11434/v1", Model: "qwen3"}, nil},
		{"chat with an https base URL", llm.Config{Backend: llm.BackendChat, BaseURL: "https://example.com/v1", Model: "qwen3"}, nil},
		{"chat without a model", llm.Config{Backend: llm.BackendChat, BaseURL: "http://localhost:11434/v1"}, llm.ErrModelRequired},
		{"chat without a base URL", llm.Config{Backend: llm.BackendChat}, llm.ErrBaseURLInvalid},
		{"chat with a relative base URL", llm.Config{Backend: llm.BackendChat, BaseURL: "localhost/v1"}, llm.ErrBaseURLInvalid},
		{"chat with another scheme", llm.Config{Backend: llm.BackendChat, BaseURL: "ftp://example.com"}, llm.ErrBaseURLInvalid},
		{"chat with a base URL that does not parse", llm.Config{Backend: llm.BackendChat, BaseURL: "http://[::1"}, llm.ErrBaseURLInvalid},
		{"a malformed URL with a token", llm.Config{Backend: llm.BackendChat, BaseURL: "http://bad host/v1?key=k1"},
			llm.ErrBaseURLInvalid},
		{"an unsupported scheme with credentials", llm.Config{Backend: llm.BackendChat, BaseURL: "ftp://ann:k1@example.com"},
			llm.ErrBaseURLInvalid},
		{"chat with a password in the base URL", llm.Config{Backend: llm.BackendChat, BaseURL: "https://ann:k1@example.com/v1", Model: "qwen3"},
			llm.ErrBaseURLCredentials},
		{"chat with a user in the base URL", llm.Config{Backend: llm.BackendChat, BaseURL: "https://ann@example.com/v1", Model: "qwen3"},
			llm.ErrBaseURLCredentials},
		{"chat with a token in the query of the base URL", llm.Config{Backend: llm.BackendChat, BaseURL: "https://example.com/v1?key=k1", Model: "qwen3"},
			llm.ErrBaseURLInvalid},
		{"chat with a fragment in the base URL", llm.Config{Backend: llm.BackendChat, BaseURL: "https://example.com/v1#k1", Model: "qwen3"},
			llm.ErrBaseURLInvalid},
		{"another backend", llm.Config{Backend: "responses"}, llm.ErrBackendUnknown},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.args.Check()
			assert.ErrorIs(t, err, tc.want)
			if err != nil {
				assert.NotContains(t, err.Error(), "k1")
			}
		})
	}
}
