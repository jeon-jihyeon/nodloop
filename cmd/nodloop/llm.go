package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

const probeSchema = `{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`

// The cheapest claude model because the probe only checks login and structured output
const probeModel = "haiku"

// The model client of every command that calls a model
// 1. the llm key of config.json picks claude or a chat endpoint and claude when it is absent
// 2. for claude NODLOOP_LLM_MODEL wins over the model of the config
// 3. the key of a chat endpoint is read from its env now and never saved
func llmClient(getenv func(string) string, cfg llm.Config) (llm.Client, error) {
	if err := cfg.Check(); err != nil {
		return nil, err
	}
	if cfg.Backend != llm.BackendChat {
		return llm.NewClaudeCLI(getenv(envClaudeBin), cmp.Or(getenv(envLLMModel), cfg.Model), "", 0), nil
	}
	key := ""
	if cfg.KeyEnv != "" {
		key = getenv(cfg.KeyEnv)
	}
	return llm.NewChatCompletions(cfg.BaseURL, cfg.Model, key, 0, time.Now), nil
}

// The client of the config under HOME
// A config that builds no client fails each model call instead of every command so a command that never calls a model still runs
func configuredLLM(getenv func(string) string) llm.Client {
	var uc userConfig
	if h := homeDir(getenv("HOME")); h != "" {
		var err error
		if uc, err = h.readConfig(); err != nil {
			return failedLLM{err: err}
		}
	}
	client, err := llmClient(getenv, uc.LLM)
	if err != nil {
		return failedLLM{err: err}
	}
	return client
}

// A client that answers every call with the error that kept the config from building one
type failedLLM struct {
	err error
}

func (f failedLLM) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, f.err
}

// The proof is the terminal proof of the process since the backend of every model call also answers as the judge of curate
func runLLM(args []string, getenv func(string) string, client llm.Client, now func() time.Time, proof trace.Proof, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "llm", errNoAction)
	}
	fs := newFlagSet("llm "+args[0], stderr)
	var f llmFlags
	f.bind(fs)
	backend, err := parseID(fs, args[1:])
	if err != nil {
		return parseFailed(err)
	}
	cmd := llmCommand{home: homeDir(getenv("HOME")), client: client, now: now, proof: proof, out: stdout}
	command := "llm " + args[0]
	switch args[0] {
	case "probe":
		err = cmd.probe(context.Background(), f.model)
	case "set":
		err = cmd.set(llm.Config{Backend: llm.Backend(backend), BaseURL: f.baseURL, Model: f.model, KeyEnv: f.keyEnv})
	case "unset":
		err = cmd.unset()
	default:
		command, err = "llm", fmt.Errorf("%w %q", errUnknownAction, args[0])
	}
	if err != nil {
		return fail(stderr, command, err)
	}
	return 0
}

type llmFlags struct {
	baseURL, model, keyEnv string
}

func (f *llmFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.baseURL, "base-url", "", "set chat: the URL /chat/completions is joined to, such as http://localhost:11434/v1")
	fs.StringVar(&f.model, "model", "", "probe: the model asked, haiku on claude and the configured model on chat by default. set: the default model")
	fs.StringVar(&f.keyEnv, "key-env", "", "set chat: the env variable that holds the API key when a command builds the client")
}

type llmCommand struct {
	home   homeDir
	client llm.Client
	now    func() time.Time
	// The terminal proof of the process
	// The backend changes only with it so an agent never swaps the model that judges for the person
	proof trace.Proof
	out   io.Writer
}

// Minimal real request that checks login and structured output and cost in one call
// An empty model asks haiku on claude and the model of the config on a chat endpoint
func (c llmCommand) probe(ctx context.Context, model string) error {
	cfg, err := c.config()
	if err != nil {
		return err
	}
	if model == "" && cfg.Backend != llm.BackendChat {
		model = probeModel
	}
	start := c.now()
	res, err := c.client.Complete(ctx, llm.Request{
		Prompt: "Return the number 42 as the field answer.",
		Schema: json.RawMessage(probeSchema),
		Model:  model,
	})
	if err != nil {
		return err
	}
	var out struct {
		Answer int `json:"answer"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		return fmt.Errorf("%w: %s: %w", errUnexpectedOutput, res.Output, err)
	}
	if out.Answer != 42 {
		return fmt.Errorf("%w: %d in %s", errWrongAnswer, out.Answer, res.Output)
	}
	fmt.Fprintf(c.out, "output %s\ncost $%.4f\ntokens in %d out %d cache_read %d cache_create %d\nduration %s (wall %s)\n",
		res.Output, res.CostUSD, res.InputTokens, res.OutputTokens, res.CacheRead, res.CacheCreate,
		res.Duration.Round(time.Millisecond), c.now().Sub(start).Round(time.Millisecond))
	return nil
}

// The llm key of config.json and claude without HOME
func (c llmCommand) config() (llm.Config, error) {
	if c.home == "" {
		return llm.Config{}, nil
	}
	uc, err := c.home.readConfig()
	return uc.LLM, err
}

// Saves the backend so every later command builds its client from it
func (c llmCommand) set(cfg llm.Config) error {
	if c.proof == trace.ProofNone {
		return fmt.Errorf("set: %w", errPersonOnly)
	}
	if c.home == "" {
		return errHomeUnknown
	}
	if cfg.Backend == "" {
		return fmt.Errorf("a backend %w", errRequired)
	}
	if err := cfg.Check(); err != nil {
		return err
	}
	if err := c.home.config().Save(map[string]any{"llm": cfg}); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "llm\t%s", cfg.Backend)
	if cfg.BaseURL != "" {
		fmt.Fprintf(c.out, "\t%s", cfg.BaseURL)
	}
	if cfg.Model != "" {
		fmt.Fprintf(c.out, "\tmodel %s", cfg.Model)
	}
	if cfg.KeyEnv != "" {
		fmt.Fprintf(c.out, "\tkey $%s", cfg.KeyEnv)
	}
	fmt.Fprintln(c.out)
	return nil
}

// Every command calls claude again
func (c llmCommand) unset() error {
	if c.proof == trace.ProofNone {
		return fmt.Errorf("unset: %w", errPersonOnly)
	}
	if c.home == "" {
		return errHomeUnknown
	}
	if err := c.home.config().Save(map[string]any{"llm": nil}); err != nil {
		return err
	}
	fmt.Fprintln(c.out, "llm\tclaude (default)")
	return nil
}
