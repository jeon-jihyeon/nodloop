package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
)

// The question a probe asks
// Any decision model answers it with a yes near 1
var probeQuestion = classify.Request{State: "The sky is blue.", Questions: classify.Questions{"blue": "The text says the sky is blue."}}

// The member every point falls back to
const builtinMember = "claude"

// What config.json holds for the classifiers
// Before 0.7.0 classifiers named endpoints and decisions set up how each point combined them
type classifierConfig struct {
	// The endpoint each decision point asks before claude
	// An endpoint name of a config before 0.7.0
	Classifiers map[string]classify.Endpoint `json:"classifiers,omitempty"`
	// Only in a config before 0.7.0
	Decisions map[classify.Point]legacySetup `json:"decisions,omitempty"`
}

// The setup of one point before 0.7.0 as far as its members tell the endpoint it asked first
type legacySetup struct {
	Mode    string   `json:"mode"`
	Members []string `json:"members"`
}

// The endpoint of each point
// 1. a config before 0.7.0 is read through its decisions
// 2. a point that asked claude alone has no endpoint
// 3. a point whose single or cascade asked one endpoint and then claude asks that endpoint
// 4. any other setup fails and names the point, since no endpoint of it alone answers as it did
func (c classifierConfig) endpoints() (classify.Endpoints, error) {
	if !c.legacy() {
		out := classify.Endpoints{}
		for name, e := range c.Classifiers {
			out[classify.Point(name)] = e
		}
		return out, out.Check()
	}
	out := classify.Endpoints{}
	for _, p := range slices.Sorted(maps.Keys(c.Decisions)) {
		s := c.Decisions[p]
		if slices.Equal(s.Members, []string{builtinMember}) {
			continue
		}
		e, ok := s.endpoint(c.Classifiers)
		if !ok {
			return nil, fmt.Errorf("%w: decisions.%s in %s asks %v. "+
				"Run nodloop classifier set %s --url <url> to ask one endpoint before claude", errSetupRetired, p, configFile, s.Members, p)
		}
		out[p] = e
	}
	return out, out.Check()
}

// The same config without the decision of the point so setting that point repairs a config before 0.7.0
func (c classifierConfig) without(p classify.Point) classifierConfig {
	if c.Decisions == nil {
		return c
	}
	c.Decisions = maps.Clone(c.Decisions)
	delete(c.Decisions, p)
	return c
}

// The endpoint a single or a cascade asked before claude and false for any other setup
func (s legacySetup) endpoint(named map[string]classify.Endpoint) (classify.Endpoint, bool) {
	if s.Mode == "parallel" || len(s.Members) == 0 || len(s.Members) > 2 || (len(s.Members) == 2 && s.Members[1] != builtinMember) {
		return classify.Endpoint{}, false
	}
	e, ok := named[s.Members[0]]
	return e, ok
}

// Whether the config was written before 0.7.0
// Such a config holds decisions or names an endpoint by a name that is no point
func (c classifierConfig) legacy() bool {
	return c.Decisions != nil || slices.ContainsFunc(slices.Collect(maps.Keys(c.Classifiers)), func(name string) bool {
		return !classify.Point(name).Valid()
	})
}

func runClassifier(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "classifier", errNoAction)
	}
	fs := newFlagSet("classifier "+args[0], stderr)
	var f classifierFlags
	f.bind(fs)
	name, err := parseID(fs, args[1:])
	if err != nil {
		return parseFailed(err)
	}
	h := homeDir(getenv("HOME"))
	if h == "" {
		return fail(stderr, "classifier", errHomeUnknown)
	}
	uc, err := h.readConfig()
	if err != nil {
		return fail(stderr, "classifier", err)
	}
	cmd := classifierCommand{home: h, now: now, out: stdout}
	point := classify.Point(name)
	switch args[0] {
	case "set":
		err = cmd.set(uc.classifierConfig, point, classify.Endpoint{URL: f.url, Model: f.model, KeyEnv: f.keyEnv})
	case "unset":
		err = cmd.unset(uc.classifierConfig, point)
	case "list":
		err = cmd.list(uc.classifierConfig)
	case "probe":
		err = cmd.probe(context.Background(), uc.classifierConfig, point, getenv)
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, args[0])
	}
	if err != nil {
		return fail(stderr, "classifier", err)
	}
	return 0
}

type classifierFlags struct {
	url, model, keyEnv string
}

func (f *classifierFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.url, "url", "", "set: the whole URL a request posts to, such as http://localhost:8000/v1/systemone")
	fs.StringVar(&f.model, "model", "", "set: the model field of a request. Empty leaves it out")
	fs.StringVar(&f.keyEnv, "key-env", "", "set: the env variable that holds the API key at call time")
}

type classifierCommand struct {
	home homeDir
	now  func() time.Time
	out  io.Writer
}

// A config before 0.7.0 is read without the decision of the point so set repairs a setup nodloop no longer runs
func (c classifierCommand) set(cfg classifierConfig, point classify.Point, e classify.Endpoint) error {
	current, err := cfg.without(point).endpoints()
	if err != nil {
		return err
	}
	endpoints, err := current.With(point, e)
	if err != nil {
		return err
	}
	if err := c.save(endpoints); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "point\t%s\t%s\n", point, e.URL)
	return nil
}

// The point asks claude alone again
func (c classifierCommand) unset(cfg classifierConfig, point classify.Point) error {
	current, err := cfg.endpoints()
	if err != nil {
		return err
	}
	endpoints, err := current.Without(point)
	if err != nil {
		return err
	}
	if err := c.save(endpoints); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "point\t%s\tclaude (default)\n", point)
	return nil
}

// Writes the endpoints and drops the decisions of a config before 0.7.0
func (c classifierCommand) save(endpoints classify.Endpoints) error {
	if err := c.home.save("classifiers", endpoints); err != nil {
		return err
	}
	return c.home.save("decisions", nil)
}

// Every point with its endpoint or claude alone
func (c classifierCommand) list(cfg classifierConfig) error {
	endpoints, err := cfg.endpoints()
	if err != nil {
		return err
	}
	for _, point := range classify.Points() {
		e, ok := endpoints[point]
		if !ok {
			fmt.Fprintf(c.out, "point\t%s\tclaude (default)\n", point)
			continue
		}
		fmt.Fprintf(c.out, "point\t%s\t%s", point, e.URL)
		if e.Model != "" {
			fmt.Fprintf(c.out, "\tmodel %s", e.Model)
		}
		if e.KeyEnv != "" {
			fmt.Fprintf(c.out, "\tkey $%s", e.KeyEnv)
		}
		fmt.Fprintln(c.out)
	}
	return nil
}

// One question to the endpoint of the point so a user sees it answers before the point relies on it
func (c classifierCommand) probe(ctx context.Context, cfg classifierConfig, point classify.Point, getenv func(string) string) error {
	if err := point.Check(); err != nil {
		return err
	}
	endpoints, err := cfg.endpoints()
	if err != nil {
		return err
	}
	e, ok := endpoints[point]
	if !ok {
		return fmt.Errorf("%w: %s", classify.ErrPointUnset, point)
	}
	start := c.now()
	answers, err := e.Classifier(getenv, classifierTimeout).Classify(ctx, probeQuestion)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s\t%s\t%s\t%dms\n", point, e.Name(), answers, c.now().Sub(start).Milliseconds())
	return nil
}
