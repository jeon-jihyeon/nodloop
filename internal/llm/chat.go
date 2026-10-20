package llm

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

const (
	// Bytes of an error body a message quotes
	bodyQuote = 200
	// A structured answer fits in well under a megabyte so a larger body is a wrong endpoint and is cut before it fills memory
	bodyLimit = 1 << 20
)

// An OpenAI compatible Chat Completions endpoint such as OpenAI or Gemini or Ollama or vLLM or a LiteLLM proxy
type ChatCompletions struct {
	// The URL `/chat/completions` is joined to
	baseURL string
	model   string
	// Sent as a Bearer token when not empty
	key     string
	timeout time.Duration
	now     func() time.Time
	client  *http.Client
}

// An empty timeout takes the default of ClaudeCLI
// A redirect is answered as the status it carries since following it would send the prompt to an endpoint the config does not name
func NewChatCompletions(baseURL, model, key string, timeout time.Duration, now func() time.Time) *ChatCompletions {
	return &ChatCompletions{
		baseURL: strings.TrimSuffix(baseURL, "/"), model: model, key: key, timeout: cmp.Or(timeout, defaultTimeout), now: now,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat responseFormat `json:"response_format"`
}

// strict is left out since strict mode refuses a schema with an optional property such as relates_to of the extract draft
// Every answer is validated here instead
type responseFormat struct {
	Type       string `json:"type"`
	JSONSchema struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
	} `json:"json_schema"`
}

// Fields of a chat completion that are used
type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

// An answer that is no JSON or fails the schema is sent back once with the validation error
// 1. an endpoint may ignore response_format so every answer is validated here
// 2. the usage of both calls is summed and CostUSD stays 0 since the API states no price
func (c *ChatCompletions) Complete(ctx context.Context, req Request) (Response, error) {
	if len(req.Schema) == 0 {
		return Response{}, ErrSchemaRequired
	}
	schema, err := newAnswerSchema(req.Schema)
	if err != nil {
		return Response{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	start := c.now()
	var messages []chatMessage
	if req.System != "" {
		messages = append(messages, chatMessage{Role: "system", Content: req.System})
	}
	messages = append(messages, chatMessage{Role: "user", Content: req.Prompt})
	res, content, err := c.call(ctx, req, messages)
	if err != nil {
		return Response{}, err
	}
	out, invalid := schema.output(content)
	if invalid != nil {
		messages = append(messages, chatMessage{Role: "assistant", Content: content}, chatMessage{
			Role: "user", Content: fmt.Sprintf("Your answer was refused: %s. Return only JSON that matches the schema.", invalid),
		})
		again, content, err := c.call(ctx, req, messages)
		if err != nil {
			return Response{}, err
		}
		res = res.add(again)
		if out, invalid = schema.output(content); invalid != nil {
			return Response{}, fmt.Errorf("%w: %w", ErrSchemaMismatch, invalid)
		}
	}
	res.Output, res.Duration = out, c.now().Sub(start)
	return res, nil
}

// The usage of one call and the content of its first choice
func (c *ChatCompletions) call(ctx context.Context, req Request, messages []chatMessage) (Response, string, error) {
	body := chatRequest{Model: cmp.Or(req.Model, c.model), Messages: messages, ResponseFormat: responseFormat{Type: "json_schema"}}
	body.ResponseFormat.JSONSchema.Name, body.ResponseFormat.JSONSchema.Schema = "answer", req.Schema
	b, err := json.Marshal(body)
	if err != nil {
		return Response{}, "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return Response{}, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.key)
	}
	answer, err := c.do(ctx, httpReq)
	if err != nil {
		return Response{}, "", err
	}
	var wire chatResponse
	if err := json.Unmarshal(answer, &wire); err != nil {
		return Response{}, "", fmt.Errorf("%w: %w", ErrResponseInvalid, err)
	}
	if len(wire.Choices) == 0 {
		return Response{}, "", fmt.Errorf("%w: no choice", ErrResponseInvalid)
	}
	u := wire.Usage
	// prompt_tokens counts the cached tokens too while InputTokens leaves them to CacheRead as claude reports them
	return Response{
		InputTokens:    u.PromptTokens - u.PromptTokensDetails.CachedTokens,
		OutputTokens:   u.CompletionTokens,
		ThinkingTokens: u.CompletionTokensDetails.ReasoningTokens,
		CacheRead:      u.PromptTokensDetails.CachedTokens,
		Model:          wire.Model,
	}, wire.Choices[0].Message.Content, nil
}

// The body of a 2xx answer
// A timeout or a cancel returns the context error
func (c *ChatCompletions) do(ctx context.Context, req *http.Request) ([]byte, error) {
	res, err := c.client.Do(req)
	if err != nil && ctx.Err() != nil {
		return nil, fmt.Errorf("llm: chat %w", ctx.Err())
	}
	if err != nil {
		return nil, err
	}
	// A body fully read leaves nothing for Close to report
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, bodyLimit+1))
	if err != nil && ctx.Err() != nil {
		return nil, fmt.Errorf("llm: chat %w", ctx.Err())
	}
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, fmt.Errorf("%w: %d %s", ErrStatus, res.StatusCode, b[:min(len(b), bodyQuote)])
	}
	if len(b) > bodyLimit {
		return nil, fmt.Errorf("%w: body over %d bytes", ErrResponseInvalid, bodyLimit)
	}
	return b, nil
}

// The request schema as the validator reads it
type answerSchema struct {
	resolved *jsonschema.Resolved
}

func newAnswerSchema(raw json.RawMessage) (answerSchema, error) {
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return answerSchema{}, fmt.Errorf("%w: %w", ErrSchemaInvalid, err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return answerSchema{}, fmt.Errorf("%w: %w", ErrSchemaInvalid, err)
	}
	return answerSchema{resolved: resolved}, nil
}

// The content as compact JSON or why it is refused
func (s answerSchema) output(content string) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	if err := s.resolved.Validate(v); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Compact(&out, []byte(content)); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
