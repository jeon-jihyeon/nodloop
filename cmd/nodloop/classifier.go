package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
)

// The question a probe asks
// Any decision model answers it with a yes near 1
var probeQuestion = classify.Request{State: "The sky is blue.", Questions: classify.Questions{"blue": "The text says the sky is blue."}}

func runClassifier(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "classifier", errNoAction)
	}
	fs := flag.NewFlagSet("classifier "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f classifierFlags
	f.bind(fs)
	name, err := parseID(fs, args[1:])
	if err != nil {
		return 1
	}
	h := homeDir(getenv("HOME"))
	if h == "" {
		return fail(stderr, "classifier", errHomeUnknown)
	}
	uc, err := h.readConfig()
	if err != nil {
		return fail(stderr, "classifier", err)
	}
	cmd := classifierCommand{home: h, endpoints: uc.Classifiers, decisions: uc.Decisions, now: now, out: stdout}
	switch args[0] {
	case "add":
		err = cmd.add(name, classify.Endpoint{URL: f.url, Model: f.model, KeyEnv: f.keyEnv})
	case "use":
		err = f.use(cmd, classify.Point(name))
	case "reset":
		err = cmd.reset(classify.Point(name))
	case "remove":
		err = cmd.remove(name)
	case "list":
		cmd.list()
	case "probe":
		err = cmd.probe(context.Background(), name, getenv)
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
	members, mode      string
	combine            string
	threshold          float64
}

func (f *classifierFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.url, "url", "", "add: the whole URL a request posts to, such as http://localhost:8000/v1/systemone")
	fs.StringVar(&f.model, "model", "", "add: the model field of a request. Empty leaves it out")
	fs.StringVar(&f.keyEnv, "key-env", "", "add: the env variable that holds the API key at call time")
	fs.StringVar(&f.members, "members", "", "use: classifiers in the order they are asked, comma separated. claude is built in")
	fs.StringVar(&f.mode, "mode", "", "use: single, cascade or parallel. Empty is single for one member and cascade for more")
	fs.Float64Var(&f.threshold, "threshold", 0, "use: cascade stops at the first member whose every answer reaches it. 0.8 when empty")
	fs.StringVar(&f.combine, "combine", "", "use: parallel combines all or any. all when empty")
}

func (f classifierFlags) use(cmd classifierCommand, point classify.Point) error {
	if f.members == "" {
		return fmt.Errorf("use: --members %w", errRequired)
	}
	setup, err := classify.NewSetup(strings.Split(f.members, ","), classify.Mode(f.mode), f.threshold, classify.Combine(f.combine))
	if err != nil {
		return err
	}
	return cmd.use(point, setup)
}

type classifierCommand struct {
	home      homeDir
	endpoints classify.Endpoints
	decisions classify.Decisions
	now       func() time.Time
	out       io.Writer
}

func (c classifierCommand) add(name string, e classify.Endpoint) error {
	endpoints, err := c.endpoints.With(name, e)
	if err != nil {
		return err
	}
	if err := c.home.save("classifiers", endpoints); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "classifier\t%s\t%s\n", name, e.URL)
	return nil
}

func (c classifierCommand) use(point classify.Point, setup classify.Setup) error {
	if !point.Valid() {
		return fmt.Errorf("%w: %q. Use one of %v", classify.ErrPointUnknown, point, classify.Points())
	}
	if err := c.endpoints.Check(setup); err != nil {
		return err
	}
	decisions := maps.Clone(c.decisions)
	if decisions == nil {
		decisions = classify.Decisions{}
	}
	decisions[point] = setup
	if err := c.home.save("decisions", decisions); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "point\t%s\t%s\n", point, setup)
	return nil
}

// The point asks claude alone again
func (c classifierCommand) reset(point classify.Point) error {
	if !point.Valid() {
		return fmt.Errorf("%w: %q. Use one of %v", classify.ErrPointUnknown, point, classify.Points())
	}
	decisions := maps.Clone(c.decisions)
	delete(decisions, point)
	if decisions == nil {
		decisions = classify.Decisions{}
	}
	if err := c.home.save("decisions", decisions); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "point\t%s\tclaude (default)\n", point)
	return nil
}

// Refused while a point names it so no setup is left with a member that cannot be built
func (c classifierCommand) remove(name string) error {
	if _, ok := c.endpoints[name]; !ok {
		return fmt.Errorf("%w: %s", classify.ErrClassifierUnknown, name)
	}
	if points := c.decisions.Using(name); len(points) > 0 {
		return fmt.Errorf("%w: %s is a member of %v. Run nodloop classifier use or reset first", classify.ErrClassifierInUse, name, points)
	}
	endpoints := maps.Clone(c.endpoints)
	delete(endpoints, name)
	return c.home.save("classifiers", endpoints)
}

// Endpoints by name, then every point with its setup or claude alone
func (c classifierCommand) list() {
	for _, name := range slices.Sorted(maps.Keys(c.endpoints)) {
		e := c.endpoints[name]
		fmt.Fprintf(c.out, "classifier\t%s\t%s", name, e.URL)
		if e.Model != "" {
			fmt.Fprintf(c.out, "\tmodel %s", e.Model)
		}
		if e.KeyEnv != "" {
			fmt.Fprintf(c.out, "\tkey $%s", e.KeyEnv)
		}
		fmt.Fprintln(c.out)
	}
	for _, point := range classify.Points() {
		text := "claude (default)"
		if setup, ok := c.decisions[point]; ok {
			text = setup.String()
		}
		fmt.Fprintf(c.out, "point\t%s\t%s\n", point, text)
	}
}

// One question to the endpoint so a user sees it answers before a point relies on it
func (c classifierCommand) probe(ctx context.Context, name string, getenv func(string) string) error {
	e, ok := c.endpoints[name]
	if !ok {
		return fmt.Errorf("%w: %s", classify.ErrClassifierUnknown, name)
	}
	start := c.now()
	answers, err := e.Classifier(getenv, classifierTimeout).Classify(ctx, probeQuestion)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s\t%s\t%dms\n", name, answers, c.now().Sub(start).Milliseconds())
	return nil
}
