package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/retain"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

const (
	// The producer of the runs a Claude Code conversation records
	sessionProducer = "session"
	// The session mode of the process over session_mode of config.json
	envSession = "NODLOOP_SESSION"
	// The directory Claude Code started in
	// It stays put when a Bash call runs cd
	envProjectDir = "CLAUDE_PROJECT_DIR"
	// Runes of an answer a run keeps
	answerRunes = 20000
	// Claude Code moves hook context past 10000 characters into a file and shows a preview only
	contextRunes = 10_000
	// Runes of the items and their lead
	// The session note follows only in the room left under contextRunes so it never pushes an item out
	promptRunes = 9_800
	// How far back the hooks look for the previous run of a session
	// 1. the hooks read the records from the end and stop here so the first prompt of a session reads one window of runs
	// 2. a session resumed after it starts without a previous run
	// The busiest day so far recorded 573 runs so a week of such days is about 4000 runs
	// 3. the shortest retention so a prune never removes the run a turn names
	sessionWindow = retain.MinDays * 24 * time.Hour
)

// The hooks of an agent conversation
// 1. every failure goes to stderr and the exit code is 0 because a hook that fails must never block a prompt or a stop
// 2. stdin is the hook input JSON of the host --host names
func runHook(args []string, getenv func(string) string, start starter, now func() time.Time, stdin io.Reader, stdout, stderr io.Writer) int {
	action, h, err := hookArgs(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "nodloop hook:", err)
		return 0
	}
	a, err := recordFlags{}.app(getenv, now)
	if err != nil {
		fmt.Fprintln(stderr, "nodloop hook:", err)
		return 0
	}
	mode, err := sessionModeOf(getenv(envSession), a.cfg.sessionMode)
	if err != nil {
		fmt.Fprintln(stderr, "nodloop hook:", err)
		return 0
	}
	if mode == sessionOff {
		return 0
	}
	in, err := h.input(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "nodloop hook:", err)
		return 0
	}
	hold, err := a.itemHold()
	if err != nil {
		fmt.Fprintln(stderr, "nodloop hook:", err)
	}
	cmd := hookCommand{
		app: a, detach: start, out: stdout, mode: mode, host: h,
		plugin: cmp.Or(getenv(envPluginVersion), buildVersion()), holdout: a.cfg.holdout, hold: hold,
	}
	if a.cfg.otel != nil {
		cmd.export = start
	}
	if action == "prompt" {
		if cmd.reaction, err = a.plan(classify.PointReaction, classify.Member{Name: conversationMember, Classifier: conversation{}}, getenv, reactionTimeout); err != nil {
			fmt.Fprintln(stderr, "nodloop hook:", err)
		}
	}
	labels := h.labels(workDir(in.Cwd).within(workDir(getenv(envProjectDir))).labels())
	if err := cmd.run(context.Background(), action, in, labels); err != nil {
		fmt.Fprintln(stderr, "nodloop hook:", err)
	}
	return 0
}

func (c hookCommand) run(ctx context.Context, action string, in hookInput, labels trace.Labels) error {
	switch action {
	case "prompt":
		return c.prompt(ctx, in.SessionID, in.Prompt, labels)
	case "stop":
		return c.stop(ctx, in.SessionID, labels, reply(in.LastAssistantMessage), in.StopHookActive)
	case "start":
		return c.start(ctx, in.SessionID, in.Source, labels)
	case "subagent":
		_, err := c.addItems(ctx, "SubagentStart", in.SessionID, labels)
		return err
	}
	return fmt.Errorf("%w %q", errUnknownAction, action)
}

// The fields of the Claude Code hook input the hooks read
// Every host input is read into it
type hookInput struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	// How the session started, read by the start hook
	Source string `json:"source"`
	// Read only for the reaction point and never stored except as the reason of a reject it records
	Prompt               string `json:"prompt"`
	LastAssistantMessage string `json:"last_assistant_message"`
	// True when the stop ends a turn a stop hook continued
	StopHookActive bool `json:"stop_hook_active"`
}

type hookCommand struct {
	app    app
	detach starter
	out    io.Writer
	mode   sessionMode
	host   host
	// The version the launcher ran this binary for and else the build version
	plugin  string
	holdout knowledge.Holdout
	// The share of sessions that miss each version nodloop approved
	// 0 until curate.json turns it on so every hook gives what it gave before the hold
	hold knowledge.ItemHold
	// The reaction point the user set up and nil when the conversation judges alone
	reaction *classify.Plan
	// Starts the OTLP export after every recorded run and nil when config.json holds no otel target
	export starter
}

// The approved items of this place the context lacks and the session note as context of the prompt
// Nothing when neither has a line
// 1. a failed read of the runs still adds the items
// 2. a corrupt line in the records leaves out that record and the rest still reach the prompt
// 3. a turn of a session the holdout draws gets the note and no item
// 4. the newest answer of the session with the run before it and the newest refill tell which items are in the context already
// 5. the start of the turn is recorded first so the stop knows how long the turn took
// 6. the note and the reaction point name the newest answer since a run a stop hook continued answers the stop and never the person
// 7. the versions the item hold keeps from the session are left out of every turn
func (c hookCommand) prompt(ctx context.Context, sessionID, message string, labels trace.Labels) error {
	turnErr := c.recordTurn(ctx, sessionID)
	all, allErr := c.all(ctx)
	if allErr != nil && !errors.Is(allErr, knowledgefile.ErrCorrupt) {
		return allErr
	}
	runs, runsErr := c.recentRuns(ctx, sessionID)
	refill, refillErr := c.refill(ctx, sessionID)
	runsErr = errors.Join(runsErr, refillErr)
	answered := runs.answered()
	previous := trace.Traces(answered).Newest()
	items := hookItems(all.For(sessionProducer, labels))
	if c.holdout.WithholdsRun(sessionProducer, sessionID) {
		items = nil
	}
	held := c.hold.Held(items.fitting(), sessionProducer, sessionID)
	recorded, reactErr := c.react(ctx, turn{run: previous, message: message})
	note := c.note(sessionID, previous)
	note.recorded = recorded
	text := items.context(answered.inContext(knowledge.Set(items), refill), held, note)
	runsErr = errors.Join(turnErr, allErr, runsErr, reactErr)
	if text == "" {
		return runsErr
	}
	return errors.Join(c.addContext("UserPromptSubmit", text), runsErr)
}

// The text as context the conversation receives on the hook event
func (c hookCommand) addContext(event, text string) error {
	return c.host.context(c.out, event, text)
}

// The note on the newest answer of the session
// A mode that infers nothing and a prompt without a session id get no note
func (c hookCommand) note(sessionID string, run trace.Trace) sessionNote {
	if !c.mode.infers() || sessionID == "" {
		return sessionNote{}
	}
	return sessionNote{run: run.ID}
}

// The SessionStart sources that empty the context of a session at work
// startup and resume add nothing since the first prompt adds the items
var refillSources = []string{"compact", "clear"}

// Every fitting item of the place again after a compaction or a clear emptied the context
// 1. a host without a prompt hook gets every fitting item from each session start since no other hook adds them
// 2. on a host with a prompt hook the items added are recorded as a refill of the session so the next prompt adds none of them again
// A clear starts a new session id with no run so without the refill that prompt would add every item a second time
func (c hookCommand) start(ctx context.Context, sessionID, source string, labels trace.Labels) error {
	if c.host.prompts() && !slices.Contains(refillSources, source) {
		return nil
	}
	added, err := c.addItems(ctx, "SessionStart", sessionID, labels)
	if !c.host.prompts() || sessionID == "" || len(added) == 0 {
		return err
	}
	return errors.Join(err, c.recordRefill(ctx, sessionID, added))
}

// Every fitting item of the place as context of the hook event and the items it added
// 1. a session the holdout withholds gets no item as from every other hook
// A subagent carries the session id of its conversation so it lands in the same arm
// 2. a corrupt line leaves out that record and the rest are still added
// 3. no prompt hook runs inside a subagent so its start is the only way an item reaches it
// 4. the versions the item hold keeps from the session are left out as from the prompts
func (c hookCommand) addItems(ctx context.Context, event, sessionID string, labels trace.Labels) (knowledge.Set, error) {
	if c.holdout.WithholdsRun(sessionProducer, sessionID) {
		return nil, nil
	}
	all, err := c.all(ctx)
	if err != nil && !errors.Is(err, knowledgefile.ErrCorrupt) {
		return nil, err
	}
	items := hookItems(all.For(sessionProducer, labels))
	held := c.hold.Held(items.fitting(), sessionProducer, sessionID)
	text := items.context(nil, held, sessionNote{})
	if text == "" {
		return nil, err
	}
	if addErr := c.addContext(event, text); addErr != nil {
		return nil, errors.Join(addErr, err)
	}
	return items.fitting().Without(held), err
}

// The start of a turn of the session as a turn trace with no text
// A turn without a session id could never be told from the turns of other sessions so it records none
func (c hookCommand) recordTurn(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	store, err := c.app.traces()
	if err != nil {
		return err
	}
	now := c.app.now()
	tr := trace.Trace{ID: trace.NewID(now), Name: trace.NameTurn, SessionID: sessionID, Time: now, Input: json.RawMessage("{}")}
	return store.Append(ctx, tr.Record())
}

// When the turn the stop ends began
// 1. the newest turn trace of the session after the previous run
// The prompt that began the turn wrote it
// 2. else the previous run when a stop hook continued the turn since the turn went on from that stop
// 3. else the zero time and the run records no duration
func (c hookCommand) turnStart(ctx context.Context, sessionID string, previous trace.Trace, continued bool) (time.Time, error) {
	if sessionID == "" {
		return time.Time{}, nil
	}
	store, err := c.app.traces()
	if err != nil {
		return time.Time{}, err
	}
	since := cmp.Or(previous.Time, c.app.now().Add(-sessionWindow))
	found, err := store.List(ctx, record.TraceFilter{Name: string(trace.NameTurn), SessionID: sessionID, Since: since, Limit: 1})
	if turn := trace.Traces(trace.RestoreAll(found)).Newest(); turn.ID != "" && turn.Time.After(previous.Time) {
		return turn.Time, err
	}
	if continued && previous.ID != "" {
		return previous.Time, err
	}
	return time.Time{}, err
}

// The items a session start added as a refill trace of the session
func (c hookCommand) recordRefill(ctx context.Context, sessionID string, added knowledge.Set) error {
	in, err := knowledge.NewRunInput(added.Refs(), nil, c.plugin)
	if err != nil {
		return err
	}
	input, err := json.Marshal(in)
	if err != nil {
		return err
	}
	store, err := c.app.traces()
	if err != nil {
		return err
	}
	now := c.app.now()
	tr := trace.Trace{ID: trace.NewID(now), Name: trace.NameRefill, SessionID: sessionID, Time: now, Input: input}
	return store.Append(ctx, tr.Record())
}

// The newest refill of the session within sessionWindow or none
func (c hookCommand) refill(ctx context.Context, sessionID string) (trace.Trace, error) {
	if sessionID == "" {
		return trace.Trace{}, nil
	}
	store, err := c.app.traces()
	if err != nil {
		return trace.Trace{}, err
	}
	since := c.app.now().Add(-sessionWindow)
	found, err := store.List(ctx, record.TraceFilter{Name: string(trace.NameRefill), SessionID: sessionID, Since: since, Limit: 1})
	return trace.Traces(trace.RestoreAll(found)).Newest(), err
}

// The runs of the session within sessionWindow newest first back to the run before its newest answer
// 1. the run before the answer bounds the turn the answer ended so the read reaches it too
// 2. the read starts at two runs and doubles past continued runs since another stop hook may continue a turn more than once
// 3. it ends at the first read that holds both or returns fewer runs than it asked for
func (c hookCommand) recentRuns(ctx context.Context, sessionID string) (sessionTurns, error) {
	for limit := 2; ; limit *= 2 {
		runs, err := c.sessionRuns(ctx, sessionID, limit)
		if len(runs) < limit || len(runs.answered()) > 1 {
			return runs, err
		}
	}
}

// At most limit runs of the session within sessionWindow newest first
// 1. an empty id would match the runs of every session so it has none
// 2. a corrupt line returns the runs found with the error
func (c hookCommand) sessionRuns(ctx context.Context, sessionID string, limit int) (sessionTurns, error) {
	if sessionID == "" {
		return nil, nil
	}
	store, err := c.app.traces()
	if err != nil {
		return nil, err
	}
	since := c.app.now().Add(-sessionWindow)
	runs, err := store.List(ctx, record.TraceFilter{Name: string(trace.NameRun), SessionID: sessionID, Since: since, Limit: limit})
	return sessionTurns(trace.RestoreAll(runs)), err
}

// The runs of a session newest first
type sessionTurns trace.Traces

// The runs from the newest answer back and none when every run was continued
// 1. an answer is a run no stop hook continued and so the answer the person read before they typed
// 2. an input that does not decode reads as an answer like every run of an earlier release
func (ts sessionTurns) answered() sessionTurns {
	i := slices.IndexFunc(ts, func(run trace.Trace) bool {
		var in knowledge.RunInput
		return json.Unmarshal(run.Input, &in) != nil || !in.Continued
	})
	if i < 0 {
		return nil
	}
	return ts[i:]
}

// The versions of the items that are in the context of the session already
// The runs start at the newest answer
// 1. the newest answer recorded the fitting items of its stop as applied since each was in the context then
// 2. a version approved after the run before it may have come during that turn after its prompt
// A stop of that turn recorded it though no hook added it so it counts as missing and the next prompt adds it
// 3. a first turn has no run before it so a version approved during it counts as in the context
// 4. an unreadable input names none so every fitting item is added again
// 5. a refill after the newest answer added every fitting item of its time so its refs are the context
func (ts sessionTurns) inContext(items knowledge.Set, refill trace.Trace) []knowledge.Ref {
	if refill.ID != "" && (len(ts) == 0 || refill.Time.After(ts[0].Time)) {
		var in knowledge.RunInput
		if err := json.Unmarshal(refill.Input, &in); err != nil {
			return nil
		}
		return in.Applied
	}
	if len(ts) == 0 {
		return nil
	}
	var in knowledge.RunInput
	if err := json.Unmarshal(ts[0].Input, &in); err != nil {
		return nil
	}
	if len(ts) == 1 {
		return in.Applied
	}
	return slices.DeleteFunc(in.Applied, func(r knowledge.Ref) bool {
		k, err := items.Version(r.ID, r.Version)
		return err == nil && k.ApprovedAt.After(ts[1].Time)
	})
}

// The answer as a run of the session and then the background work the turn left
// 1. an empty answer such as an interrupted turn records nothing
// 2. the newest answer before the stop is the one the prompt hook named this turn so each run is looked at for a lesson once
// A stop of a turn a stop hook continued looks at none since the first stop of its turn looked at that answer
// 3. a correction a knowledge record already cites is not drafted again in the background
// The conversation usually drafted it in the same turn
// 4. the holdout draws once per session so every turn of a conversation is in one arm
// A lesson shown in one turn stays in the context of the next so a draw per turn would leak it into the withheld arm
// 5. the extraction and the daily retention prune and the daily consolidate and the OTLP export start in processes of their own so the turn never waits for them
// A failure to start one is printed and the others still start
// A failed record starts no export since no new run waits to be sent
// 6. a host whose stop input carries no answer records no run and still starts the background work
func (c hookCommand) stop(ctx context.Context, sessionID string, labels trace.Labels, answer reply, continued bool) error {
	if c.host.answers() && strings.TrimSpace(string(answer)) == "" {
		return nil
	}
	runs, runsErr := c.recentRuns(ctx, sessionID)
	if runsErr != nil && !errors.Is(runsErr, tracefile.ErrCorrupt) {
		return runsErr
	}
	previous := trace.Traces(runs).Newest()
	all, allErr := c.all(ctx)
	if allErr != nil && !errors.Is(allErr, knowledgefile.ErrCorrupt) {
		return allErr
	}
	corrupt := errors.Join(runsErr, allErr)
	var failed error
	if c.host.answers() {
		failed = c.record(ctx, sessionID, labels, answer, all, previous, continued)
	}
	if c.export != nil && c.host.answers() && failed == nil {
		failed = c.app.startExport(c.export)
	}
	failed = errors.Join(failed, c.prune(ctx), c.consolidate(all))
	if judged := trace.Traces(runs.answered()).Newest(); !continued && !all.Cites(judged.ID) {
		failed = errors.Join(failed, c.extract(ctx, judged.ID, judged.Time))
	}
	return errors.Join(corrupt, failed)
}

// The answer as a run of the session with every fitting item of the place in its context and the duration of its turn
// 1. a turn start that cannot be read leaves the duration out and still records the run
// 2. a corrupt line is left to the read of the previous run that reports it already
// 3. the run of a turn a stop hook continued is marked so no reader takes it for the answer the person judged
// 4. the item hold takes the versions it keeps from the session out of applied and records every fitting version with its chance
func (c hookCommand) record(
	ctx context.Context, sessionID string, labels trace.Labels, answer reply, all knowledge.Set, previous trace.Trace, continued bool,
) error {
	shown := hookItems(all.For(sessionProducer, labels)).fitting()
	applied := shown.Refs()
	var withheld []knowledge.Ref
	if c.holdout.WithholdsRun(sessionProducer, sessionID) {
		applied, withheld = nil, applied
	}
	in, err := knowledge.NewRunInput(applied, withheld, c.plugin)
	if err != nil {
		return err
	}
	in.Continued = continued
	in = c.hold.Drawn(in, shown, sessionProducer, sessionID)
	input, err := json.Marshal(c.holdout.Drawn(in, sessionProducer, sessionID))
	if err != nil {
		return err
	}
	tr, err := trace.NewRun(sessionProducer, "", labels, input, []byte(answer.text()), c.app.now())
	if err != nil {
		return err
	}
	tr.SessionID = sessionID
	start, startErr := c.turnStart(ctx, sessionID, previous, continued)
	if errors.Is(startErr, tracefile.ErrCorrupt) {
		startErr = nil
	}
	if !start.IsZero() {
		if tr, err = tr.Took(tr.Time.Sub(start)); err != nil {
			return err
		}
	}
	store, err := c.app.traces()
	if err != nil {
		return err
	}
	return errors.Join(store.Append(ctx, tr.Record()), startErr)
}

// The extraction of the run started when the conversation alone judged it and its latest verdict corrects it
// It runs as `knowledge extract` in a process of its own so the stop never waits for a model
func (c hookCommand) extract(ctx context.Context, runID string, at time.Time) error {
	if !c.mode.infers() || runID == "" {
		return nil
	}
	inferred, err := c.inferredCorrection(ctx, runID, at)
	if err != nil || !inferred {
		return err
	}
	return c.detach([]string{"knowledge", "extract", "--from", runID})
}

// Whether the conversation alone judged the run and its latest verdict corrects it
// 1. a run with a verdict of a person is left to the nod skill even after a later inferred one because the person's verdict wins
// A person's withdraw is such a verdict so a withdrawn run is never drafted
// 2. the verdicts are read from the end of the records back to the run since none comes before it
func (c hookCommand) inferredCorrection(ctx context.Context, runID string, at time.Time) (bool, error) {
	verdicts, err := c.app.feedback()
	if err != nil {
		return false, err
	}
	list, err := verdicts.List(ctx, record.FeedbackFilter{TraceID: runID, Since: at})
	if err != nil {
		return false, err
	}
	records := feedback.RestoreAll(list)
	latest := records.Latest()
	return len(records.Human()) == 0 && len(latest) > 0 && latest[0].Corrects(), nil
}

// Starts records prune --daily in a process of its own when the daily prune is due
// 1. the marker is read here without its lock so most turns start nothing and the started process claims the marker itself
// 2. the process reads the record directory and the retention from the same environment and config.json as the hook
func (c hookCommand) prune(ctx context.Context) error {
	if c.app.cfg.retention == 0 {
		return nil
	}
	daily, err := c.app.daily()
	if err != nil {
		return err
	}
	due, err := daily.Due(ctx)
	if err != nil || !due {
		return err
	}
	return c.detach([]string{"records", "prune", "--daily"})
}

// Starts nodloop with the args in a process that outlives the caller
type starter func(args []string) error

func (c hookCommand) all(ctx context.Context) (knowledge.Set, error) {
	ledger, err := c.app.ledger()
	if err != nil {
		return nil, err
	}
	return ledger.All(ctx)
}

// The approved items a prompt receives
type hookItems knowledge.Set

// The fitting items the context lacks one line each under their lead and the session note after them
// 1. after versions in the context only the fitting ones not among them come under DeltaLead
// A new version of an item in the context comes as a line that replaces its earlier one
// 2. with none in the context every fitting item comes under PromptLead
// The items are cut at promptRunes with the count left out so the model knows the list is partial
// 3. the note is left out when it would pass contextRunes
// 4. the held versions are left out after the cut so whether one version reaches the session never depends on the draws of the others
func (items hookItems) context(inContext, held []knowledge.Ref, note sessionNote) string {
	var b strings.Builder
	fitted := items.fitting()
	shown, left := fitted.Without(held), len(items)-len(fitted)
	switch added := shown.Without(inContext); {
	case len(inContext) > 0 && len(added) > 0:
		b.WriteString(knowledge.DeltaLead)
		for _, k := range added {
			b.WriteString(k.PromptLine())
		}
	case len(inContext) == 0 && (len(shown) > 0 || left > 0):
		b.WriteString(knowledge.PromptLead)
		for _, k := range shown {
			b.WriteString(k.PromptLine())
		}
		if left > 0 {
			fmt.Fprintf(&b, "- %d more items left out over the size cap\n", left)
		}
	}
	if text := note.text(); utf8.RuneCountInString(b.String())+utf8.RuneCountInString(text) <= contextRunes {
		b.WriteString(text)
	}
	return b.String()
}

// The items that fit the prompt in order before the item hold leaves its versions out
// The prompt hook shows them and the stop hook records them as applied, so both name the same items
func (items hookItems) fitting() knowledge.Set {
	size := utf8.RuneCountInString(knowledge.PromptLead)
	var shown knowledge.Set
	for _, k := range items {
		runes := utf8.RuneCountInString(k.PromptLine())
		if size+runes > promptRunes {
			break
		}
		size += runes
		shown = append(shown, k)
	}
	return shown
}

// What the prompt hook tells the conversation about its session
type sessionNote struct {
	// The newest answer of the session that no stop hook continued and empty before its first
	run string
	// The verdict the reaction point recorded on that answer and empty when the conversation judges it
	recorded feedback.Verdict
}

// The reaction a conversation records is an inferred verdict, so only words that judge the previous answer count
// The verdict is recorded without a word to the user and its lesson is drafted after the turn so a discussion goes on undisturbed
const reactionNote = "nodloop: your previous answer in this conversation is run %s. " +
	"If the user's message says that answer was wrong or right, call the nodloop feedback tool on trace %s with reviewer session: " +
	"verdict reject with what was wrong in the user's words and a reason code, or approve. " +
	"Do it without telling the user and go on with what they asked. Its lesson is drafted after the turn. " +
	"Skip it for a new request, a follow up, a change of topic, or when the user calls /nodloop:nod.\n"

// The reaction point already recorded a reject so the conversation records nothing more
const recordedNote = "nodloop: the user's message was recorded as a reject of your previous answer, run %s. " +
	"Do what the user asked. Its lesson is drafted after the turn.\n"

func (n sessionNote) text() string {
	switch {
	case n.run == "", n.recorded == feedback.VerdictApprove:
		return ""
	case n.recorded == feedback.VerdictReject:
		return fmt.Sprintf(recordedNote, n.run)
	}
	return fmt.Sprintf(reactionNote, n.run, n.run)
}

// How a conversation records the verdicts it infers
type sessionMode string

const (
	sessionDeferred sessionMode = "deferred" // infers each verdict silently and drafts its lesson after the turn
	sessionManual   sessionMode = "manual"   // records answers and items and leaves every verdict to an explicit nod
	sessionOff      sessionMode = "off"      // records nothing
	// A mode of an earlier release that drafted a correction in its turn
	// It reads as deferred so a setting that still names it keeps recording
	sessionImmediate sessionMode = "immediate"
)

var sessionModes = []sessionMode{sessionDeferred, sessionManual, sessionOff}

const sessionModeHint = "Use deferred, manual or off"

func (m sessionMode) valid() bool {
	return slices.Contains(sessionModes, m)
}

// The mode a value of the env or config.json reads as
func (m sessionMode) read() sessionMode {
	if m == sessionImmediate {
		return sessionDeferred
	}
	return m
}

// Whether the conversation records the verdicts it reads and the stop hook drafts their lessons
func (m sessionMode) infers() bool {
	return m == sessionDeferred
}

// The mode of the process
// 1. NODLOOP_SESSION wins, then session_mode in config.json, then deferred
// 2. a value outside the set is refused with where it came from so the hooks record nothing the user did not choose
func sessionModeOf(env, configured string) (sessionMode, error) {
	switch fromEnv, fromConfig := sessionMode(env).read(), sessionMode(configured).read(); {
	case env != "" && !fromEnv.valid():
		return "", fmt.Errorf("%w: %s is %q. %s", errSessionModeUnknown, envSession, env, sessionModeHint)
	case env != "":
		return fromEnv, nil
	case configured != "" && !fromConfig.valid():
		return "", fmt.Errorf("%w: session_mode in %s is %q. %s", errSessionModeUnknown, configFile, configured, sessionModeHint)
	case configured != "":
		return fromConfig, nil
	}
	return sessionDeferred, nil
}

// The last answer of a turn
type reply string

// The answer with its secrets redacted and cut at answerRunes with the cut marked
func (r reply) text() string {
	text := feedback.Redact(string(r))
	if utf8.RuneCountInString(text) <= answerRunes {
		return text
	}
	return string([]rune(text)[:answerRunes]) + "\n[cut by nodloop]"
}

// The working directory of a conversation
type workDir string

// The working directory while it lies inside the project and the project otherwise
// A cd into a scratchpad or a clone elsewhere would label the run with a place the next session never works in
func (w workDir) within(project workDir) workDir {
	switch {
	case project == "":
		return w
	case w == "":
		return project
	}
	rel, err := filepath.Rel(string(project), string(w))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return project
	}
	return w
}

// repo names the repository and dir is the path below the root of the checkout
// 1. a worktree has a .git file whose gitdir sits under the .git of the main checkout, so it takes the name of the main checkout
// 2. outside a repository only dir is set, to the absolute path
func (w workDir) labels() trace.Labels {
	abs, err := filepath.Abs(string(w))
	if err != nil || w == "" {
		return nil
	}
	for root := abs; ; root = filepath.Dir(root) {
		if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
			rel, _ := filepath.Rel(root, abs)
			return trace.Labels{"repo": {repoName(root)}, "dir": {filepath.ToSlash(rel)}}
		}
		if filepath.Dir(root) == root {
			return trace.Labels{"dir": {filepath.ToSlash(abs)}}
		}
	}
}

// The base name of the main checkout of the repository whose root holds .git
// A .git file of a worktree reads gitdir: <main>/.git/worktrees/<name>
func repoName(root string) string {
	b, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil {
		return filepath.Base(root)
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
	if main, _, ok := strings.Cut(filepath.ToSlash(gitdir), "/.git/worktrees/"); ok {
		return filepath.Base(main)
	}
	return filepath.Base(root)
}
