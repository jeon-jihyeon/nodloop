package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/textkey"
)

// One pass of curate over the waiting candidates and the signals since the last one
// 1. the pass holds curate.json for its whole run so two passes never decide one change twice
// 2. --daily runs only when the mark is a day old and first extracts the corrected runs no extraction read
// It claims the day before its model calls so a pass that fails waits a day
// 3. a pass without --daily keeps the time of the last daily pass so the daily work still runs once a day
// 4. the mark is written only from a pass that reached its cycle so a pass that failed to start leaves the hooks what they had
func (f knowledgeFlags) runCurate(ctx context.Context, a app, client llm.Client, getenv func(string) string, stdout io.Writer) error {
	dir, err := a.makeRecordDir()
	if err != nil {
		return err
	}
	held, err := curate.NewMarker(dir).Hold()
	switch {
	case err != nil:
		return err
	case held == nil && f.daily:
		return nil
	case held == nil:
		return errCurateRunning
	}
	defer func() { _ = held.Release() }()
	daily := held.Mark.At
	var gaps error
	if f.daily {
		if !held.Mark.Daily(a.now()) {
			return nil
		}
		claimed := held.Mark
		claimed.At = a.now().UTC()
		if err := held.Write(claimed); err != nil {
			return err
		}
		daily, gaps = claimed.At, f.extractGaps(ctx, a, client, getenv, stdout)
	}
	curator, setting, err := f.curator(ctx, a, client, getenv)
	if err != nil {
		return errors.Join(gaps, err)
	}
	res, err := curator.Pass(ctx, setting)
	for _, d := range res.Decided {
		fmt.Fprintf(stdout, "%s\t%s\t%s\ttier %d\t%s\n", d.Decision.Action, d.Change.Kind, versionColumns(d.Change), d.Decision.Tier,
			cmp.Or(string(d.Decision.RuleCode), string(d.Decision.Outcome), "-"))
	}
	if res.Event.CycleID == "" {
		return errors.Join(gaps, err)
	}
	return errors.Join(gaps, err, held.Write(res.Mark(daily)))
}

// The id and version columns of a decide line
// An offer of the gate acts on no version so a widening names the kind and bin it opens and the activation nothing
func versionColumns(c curate.Change) string {
	switch {
	case c.Opens != nil:
		return strings.TrimSpace(string(c.Opens.Kind)+" "+string(c.Opens.Bin)) + "\t-"
	case c.ID == "":
		return "-\t-"
	}
	return fmt.Sprintf("%s\tv%d", c.ID, c.Version)
}

// Extracts each corrected run of the window no extraction read
// A run whose extraction fails or is refused prints why and the next one still runs
func (f knowledgeFlags) extractGaps(ctx context.Context, a app, client llm.Client, getenv func(string) string, stdout io.Writer) error {
	curator, err := a.curator(client, nil)
	if err != nil {
		return err
	}
	since, err := a.window(ctx)
	if err != nil {
		return err
	}
	ids, err := curator.Gaps(ctx, since)
	if err != nil {
		return err
	}
	for _, id := range ids {
		fmt.Fprintf(stdout, "gap\t%s\n", id)
		if err := f.extractRun(ctx, a, client, getenv, id, stdout); err != nil {
			fmt.Fprintf(stdout, "gap\t%s\tnot extracted: %v\n", id, err)
		}
	}
	return nil
}

// The curator of the record directory with what its pass reads besides the records
// 1. the anchor is refreshed first so every decide names the version it was judged against
// 2. the numbers of report curation go to the knobs as plain values since curate never reads loop
// 3. the judge runs the model --model names or else the model of a chat endpoint or else the default of curate
// 4. an endpoint set for the judge point joins the claude variants as a member of another model family
func (f knowledgeFlags) curator(ctx context.Context, a app, client llm.Client, getenv func(string) string) (*curate.Curator, curate.Setting, error) {
	state, err := anchorCommand{app: a, out: io.Discard}.refresh(ctx)
	if err != nil {
		return nil, curate.Setting{}, err
	}
	stores, err := a.reportStores(ctx)
	if err != nil {
		return nil, curate.Setting{}, err
	}
	report, err := stores.Curation(ctx, a.now())
	if err != nil {
		return nil, curate.Setting{}, err
	}
	var members []classify.Member
	endpoints, err := a.cfg.classifiers.endpoints()
	if err != nil {
		return nil, curate.Setting{}, err
	}
	if e, ok := endpoints[classify.PointJudge]; ok {
		members = append(members, classify.Member{Name: e.Name(), Classifier: e.Classifier(getenv, curate.JudgeTimeout)})
	}
	curator, err := a.curator(client, members)
	if err != nil {
		return nil, curate.Setting{}, err
	}
	judge := curate.DefaultJudge
	if a.cfg.llm.Backend == llm.BackendChat {
		judge.Model = a.cfg.llm.Model
	}
	judge.Model = cmp.Or(f.model, judge.Model)
	var seed [16]byte
	// crypto rand Read never returns an error
	_, _ = rand.Read(seed[:])
	return curator, curate.Setting{
		Build: buildVersion(), Judge: judge, Seed: hex.EncodeToString(seed[:]), Anchor: state.anchor, Version: state.version, Since: state.at,
		Window: state.since, Temp: tempDirs(), Read: readText, Numbers: curationNumbers(report),
	}, nil
}

// The curator over the stores of the record directory with the members of the judge another model family adds
func (a app) curator(client llm.Client, members []classify.Member) (*curate.Curator, error) {
	ledger, err := a.ledger()
	if err != nil {
		return nil, err
	}
	traces, err := a.traces()
	if err != nil {
		return nil, err
	}
	verdicts, err := a.feedback()
	if err != nil {
		return nil, err
	}
	key, err := textkey.Load(a.cfg.recordDir)
	if err != nil {
		return nil, err
	}
	return curate.New(ledger, traces, verdicts, client, members, key.Sum, a.now), nil
}

// The temporary directories of the OS with the paths their links resolve to
// macOS links /tmp and the per user temp dir into /private so a run labeled with either form counts
func tempDirs() curate.TempDirs {
	var out curate.TempDirs
	for _, dir := range []string{os.TempDir(), "/tmp"} {
		out = append(out, filepath.Clean(dir))
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			out = append(out, resolved)
		}
	}
	return out
}

// The text of an anchor file read at check time
func readText(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// Starts knowledge curate --daily in a process of its own when its mark is a day old
// 1. the mark is read here without its lock so most stops start nothing and the started process holds the mark itself
// 2. only a mode that infers starts it since the pass works on what the capture and the extraction prepared
func (c hookCommand) curate() error {
	if !c.mode.infers() {
		return nil
	}
	dir, err := c.app.makeRecordDir()
	if err != nil {
		return err
	}
	mark, err := curate.ReadMark(dir)
	if err != nil || !mark.Daily(c.app.now()) {
		return err
	}
	return c.detach([]string{"knowledge", "curate", "--daily"})
}
