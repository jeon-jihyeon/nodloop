package llm

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBin   = "claude"
	defaultModel = "sonnet"
	// Structured output is an internal tool call so at least 2 is needed
	defaultMaxTurns = 3
	defaultBudget   = 0.5
	// Concentration reviews with sonnet take 90 to 110 seconds so a 120 second timeout cut one review in ten
	defaultTimeout = 240 * time.Second
	stderrLimit    = 200
)

type ClaudeCLI struct {
	bin   string
	model string
	// Parent of the private work dir each call creates
	workParent string
	timeout    time.Duration
}

// An empty argument takes the package default
func NewClaudeCLI(bin, model, workParent string, timeout time.Duration) *ClaudeCLI {
	return &ClaudeCLI{
		bin:        cmp.Or(bin, defaultBin),
		model:      cmp.Or(model, defaultModel),
		workParent: cmp.Or(workParent, os.TempDir()),
		timeout:    cmp.Or(timeout, defaultTimeout),
	}
}

// Each call runs claude in a new empty directory so no project `CLAUDE.md` or settings load
// 1. MkdirTemp creates it with mode 0700 under a random name and never reuses a directory another user planted in a shared temp dir
// 2. it is removed after the call so concurrent calls never share one
func (c *ClaudeCLI) Complete(ctx context.Context, req Request) (Response, error) {
	if len(req.Schema) == 0 {
		return Response{}, ErrSchemaRequired
	}
	dir, err := os.MkdirTemp(c.workParent, "nodloop-llm-")
	if err != nil {
		return Response{}, fmt.Errorf("%w: %w", ErrWorkDir, err)
	}
	// A directory left behind holds nothing and is private so a failed removal never fails the call
	defer func() { _ = os.RemoveAll(dir) }()
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	stdout, stderr, runErr := c.run(ctx, dir, c.args(req.Schema, req.Model, req.System), req.Prompt)
	if ctx.Err() != nil {
		return Response{}, fmt.Errorf("llm: claude %w", ctx.Err())
	}
	// claude can print a result JSON and still exit 1
	// Budget and turn limits do that so ResultError takes precedence over the exit code
	res, parseErr := parseResult(stdout)
	var re *ResultError
	if errors.As(parseErr, &re) {
		return Response{}, re
	}
	if runErr == nil && parseErr == nil {
		return res, nil
	}
	head := strings.TrimSpace(string(stderr))
	// Cut by runes so a multibyte character is never split
	if runes := []rune(head); len(runes) > stderrLimit {
		head = string(runes[:stderrLimit]) + "..."
	}
	if runErr != nil {
		return Response{}, fmt.Errorf("%w: %w: %s", ErrClaudeFailed, runErr, head)
	}
	return Response{}, fmt.Errorf("%w: %s", parseErr, head)
}

func (c *ClaudeCLI) run(ctx context.Context, dir string, args []string, prompt string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	// WaitDelay lets cancellation clean up the process before returning
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

// No shell is involved so no quoting is needed
// 1. `--safe-mode` drops the user `CLAUDE.md` and hooks and plugins that the empty cwd alone still loads
// 2. it keeps the OAuth login that `--bare` refuses
// 3. `--setting-sources user` drops project settings that a `.claude` folder in a parent of the cwd could plant
// 4. such settings could send the prompt and the credential to another endpoint through their env
func (c *ClaudeCLI) args(schema json.RawMessage, model, system string) []string {
	argv := []string{
		"-p",
		"--output-format", "json",
		"--json-schema", string(schema),
		"--max-turns", strconv.Itoa(defaultMaxTurns),
		"--max-budget-usd", strconv.FormatFloat(defaultBudget, 'f', -1, 64),
		"--no-session-persistence",
		"--safe-mode",
		"--setting-sources", "user",
		"--model", cmp.Or(model, c.model),
	}
	if system == "" {
		return argv
	}
	return append(argv, "--append-system-prompt", system)
}

const (
	resultType     = "result"
	subtypeSuccess = "success"
)

// Fields of the `claude -p` result JSON that are used
// Other fields vary across versions and are ignored
type result struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Errors           []string        `json:"errors"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	DurationMS       int64           `json:"duration_ms"`
	Usage            usage           `json:"usage"`
}

type usage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	OutputTokensDetails struct {
		ThinkingTokens int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func parseResult(b []byte) (Response, error) {
	var r result
	if err := json.Unmarshal(bytes.TrimSpace(b), &r); err != nil {
		return Response{}, fmt.Errorf("%w: %w", ErrNotResult, err)
	}
	return r.response()
}

func (r result) response() (Response, error) {
	if r.Type != resultType {
		return Response{}, fmt.Errorf("%w: type %q", ErrNotResult, r.Type)
	}
	if r.IsError || r.Subtype != subtypeSuccess {
		return Response{}, &ResultError{Subtype: r.Subtype, Errors: r.Errors, CostUSD: r.TotalCostUSD}
	}
	if len(r.StructuredOutput) == 0 || string(r.StructuredOutput) == "null" {
		return Response{}, ErrNoOutput
	}
	return Response{
		Output:         r.StructuredOutput,
		CostUSD:        r.TotalCostUSD,
		InputTokens:    r.Usage.InputTokens,
		OutputTokens:   r.Usage.OutputTokens,
		ThinkingTokens: r.Usage.OutputTokensDetails.ThinkingTokens,
		CacheRead:      r.Usage.CacheReadInputTokens,
		CacheCreate:    r.Usage.CacheCreationInputTokens,
		Duration:       time.Duration(r.DurationMS) * time.Millisecond,
	}, nil
}
