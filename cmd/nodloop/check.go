package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	evidencefile "github.com/jeon-jihyeon/nodloop/internal/evidence/file"
)

// Prints what the data dir holds as one JSON object and exits 1 when an error stopped the check
// The report is printed either way so a proposal can start from the profile before any policy exists
func runCheck(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "reference data directory in the canonical layout")
	policy := fs.String("policy", "", "policy file to check in place of policy.yaml of the data directory")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dataDir == "" {
		return fail(stderr, "check", fmt.Errorf("--data-dir %w", errRequired))
	}
	report, err := dataCheck{dir: *dataDir, policyPath: *policy}.run(context.Background())
	if err != nil {
		report.Error = err.Error()
	}
	report.Version = buildVersion()
	if w := pluginVersion(getenv(envPluginVersion)).mismatch(report.Version, executable()); w != "" {
		report.Warnings = append(report.Warnings, w)
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if encErr := enc.Encode(report); encErr != nil {
		return fail(stderr, "check", encErr)
	}
	if err != nil {
		return 1
	}
	return 0
}

// What a data dir holds as every command reads it
// Filled as far as the check got
type dataReport struct {
	// The version of this binary so a user can tell it from the one the plugin expects
	Version    string             `json:"version"`
	DataDir    string             `json:"data_dir"`
	Events     int                `json:"events"`
	Profile    *analysis.Profile  `json:"profile,omitempty"`
	Contexts   evidence.Contexts  `json:"contexts"`
	Procedures []procedureSummary `json:"procedures"`
	// Absent when no policy loaded
	Policy   *policySummary `json:"policy,omitempty"`
	Warnings []string       `json:"warnings"`
	// The error that stopped the check
	Error string `json:"error,omitempty"`
}

type procedureSummary struct {
	Slug  string         `json:"slug"`
	Scope evidence.Scope `json:"scope"`
}

type policySummary struct {
	Version   string `json:"version"`
	Analyzers int    `json:"analyzers"`
}

// Checks the canonical layout of one data dir and writes nothing
type dataCheck struct {
	dir string
	// A draft checked in place of policy.yaml of the dir
	// Empty reads policy.yaml of the dir
	policyPath string
}

// Each step stops at its first error and the steps before it stay in the report
// 1. the change contexts come from the contexts key alone so a draft with only that key already yields the profile
// 2. contexts.csv is read against them with every event so an undeclared value fails naming the set
// 3. procedures fail on bad front matter or a scope naming an undeclared context or a metric no event carries
// 4. labels fail on a bad line when the file exists
// 5. the policy comes last because a proposal needs everything above to be written
// 6. a policy that does not load or names a metric or dimension no event carries fails here as setup would refuse it
func (d dataCheck) run(ctx context.Context) (dataReport, error) {
	abs, err := filepath.Abs(d.dir)
	report := dataReport{DataDir: abs, Contexts: evidence.Contexts{}, Procedures: []procedureSummary{}, Warnings: []string{}}
	if err != nil {
		return report, err
	}
	if _, err := os.Stat(filepath.Join(abs, "events.csv")); err != nil {
		return report, fmt.Errorf("%w: %s", errNoEvents, abs)
	}
	text, textErr := d.policyText(abs)
	if textErr != nil && !errors.Is(textErr, errPolicyMissing) {
		return report, textErr
	}
	if report.Contexts, err = d.contexts(text); err != nil {
		return report, err
	}
	src, err := evidencefile.New(abs, report.Contexts)
	if err != nil {
		return report, err
	}
	events, err := src.All(ctx)
	if err != nil {
		return report, err
	}
	profile := analysis.NewProfile(events)
	report.Events, report.Profile = len(events), &profile
	if err := report.readProcedures(ctx, src, profile.MetricNames()); err != nil {
		return report, err
	}
	if err := report.warn(ctx, src); err != nil {
		return report, err
	}
	if _, err := src.Labels(ctx); err != nil {
		return report, err
	}
	if textErr != nil {
		return report, textErr
	}
	policy, err := analysis.LoadPolicy(text, commandRunner{dir: abs})
	if err != nil {
		return report, err
	}
	report.Policy = &policySummary{Version: policy.Version, Analyzers: len(policy.Analyzers)}
	return report, policy.Observed(profile.MetricNames(), profile.DimensionNames()).CheckObserved()
}

// The draft at policyPath or policy.yaml of the dir
func (d dataCheck) policyText(abs string) ([]byte, error) {
	if d.policyPath == "" {
		return app{cfg: config{dataDir: abs}}.policyText()
	}
	return os.ReadFile(d.policyPath)
}

// The default five when no policy text exists
func (dataCheck) contexts(text []byte) (evidence.Contexts, error) {
	if text == nil {
		return evidence.DefaultContexts(), nil
	}
	return analysis.LoadContexts(text)
}

// Procedures with their scope
// A scope metric no event carries fails because the procedure would never reach a review
func (r *dataReport) readProcedures(ctx context.Context, src *evidencefile.Source, metrics []string) error {
	procedures, err := src.Procedures(ctx)
	if err != nil {
		return err
	}
	for _, p := range procedures {
		for _, m := range p.Scope.Metrics {
			if !slices.Contains(metrics, m) {
				return fmt.Errorf("%w: %s names %s", errScopeUnobserved, p.File, m)
			}
		}
		r.Procedures = append(r.Procedures, procedureSummary{Slug: p.Slug, Scope: p.Scope})
	}
	return nil
}

// 1. Markdown under procedures that no review reads warns with each entry named
// 2. a data dir without contexts.csv warns because every event then reads change context unknown
func (r *dataReport) warn(ctx context.Context, src *evidencefile.Source) error {
	skipped, err := src.Skipped(ctx)
	if err != nil {
		return err
	}
	if len(skipped) > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("reviews never read %s. "+
			"Only .md files directly under procedures are procedures and a folder setup cannot open is passed over", strings.Join(skipped, ", ")))
	}
	if _, err := os.Stat(filepath.Join(r.DataDir, "contexts.csv")); errors.Is(err, os.ErrNotExist) {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%s has no contexts.csv so every event reads change context unknown. "+
			"Add one with the columns event_id and change_context to name what changed around each event", r.DataDir))
	}
	return nil
}
