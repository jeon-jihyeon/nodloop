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
	"github.com/jeon-jihyeon/nodloop/internal/curate"
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
	// How far back the hooks and the capture look for the runs, turns, refills, captures and exposes of a session
	// 1. the reads go from the end of the records and stop here so a new session reads one window and never the records of every session
	// The busiest day so far recorded 573 runs so a week of such days is about 4000 runs
	// 2. a session resumed after it starts without a previous run
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
	mode, err := sessionModeOf(a.cfg.sessionMode)
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
	mark, markErr := a.mark()
	if markErr != nil {
		fmt.Fprintln(stderr, "nodloop hook:", markErr)
	}
	cmd := hookCommand{
		app: a, detach: start, out: stdout, mode: mode, host: h,
		plugin: cmp.Or(getenv(envPluginVersion), buildVersion()), holdout: a.cfg.holdout, hold: mark.Knobs.Hold(), mark: mark, marked: markErr == nil,
		rules: newRulesFiles(a.cfg.recordDir, a.cfg.home, getenv(envClaudeConfigDir), cmp.Or(getenv(envProjectDir), in.Cwd)),
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

// hookArgs admits only the actions of hostActions so the one left after the others is instructions
func (c hookCommand) run(ctx context.Context, action string, in hookInput, labels trace.Labels) error {
	switch action {
	case "prompt":
		return c.prompt(ctx, in.SessionID, in.Prompt, labels)
	case "stop":
		return c.stop(ctx, in.SessionID, labels, reply(in.LastAssistantMessage), in.StopHookActive, transcript(in.TranscriptPath))
	case "start":
		return c.start(ctx, in.SessionID, in.Source, labels)
	case "subagent":
		_, err := c.addItems(ctx, "SubagentStart", in.SessionID, labels)
		return err
	}
	return c.instructions(ctx, in)
}

// The fields of the Claude Code hook input the hooks read
// Every host input is read into it
type hookInput struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	// How the session started, read by the start hook
	Source string `json:"source"`
	// Read only for the reaction point and the first word of a reply and never stored except as the reason of a reject the point records
	Prompt               string `json:"prompt"`
	LastAssistantMessage string `json:"last_assistant_message"`
	// True when the stop ends a turn a stop hook continued
	StopHookActive bool `json:"stop_hook_active"`
	// The transcript of the session the stop hands to the capture and never read in the hook
	TranscriptPath string `json:"transcript_path"`
	// The rules file the instructions hook hashes with what the host reported of it
	FilePath   string `json:"file_path"`
	MemoryType string `json:"memory_type"`
	LoadReason string `json:"load_reason"`
	// The instructions file whose @path imports the file when the load reason is include
	ParentFilePath string `json:"parent_file_path"`
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
	// curate.json as the hooks read it once per call with the questions due
	mark curate.Mark
	// Whether curate.json could be read
	// One that could not is reported once and starts no daily pass since the pass could not hold it either
	marked bool
	// The reaction point the user set up and nil when the conversation judges alone
	reaction *classify.Plan
	// Starts the OTLP export after every recorded run and nil when config.json holds no otel target
	export starter
	// The rules files the host loads as the person's instructions and those whose lines no person confirmed
	rules rulesFiles
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
// 8. a first word that names a choice of the question shown after that answer is never judged
// The capture records it as the person's answer from the transcript after the turn
func (c hookCommand) prompt(ctx context.Context, sessionID, message string, labels trace.Labels) error {
	turnErr := c.recordTurn(ctx, sessionID)
	all, allErr := c.all(ctx)
	if allErr != nil && !errors.Is(allErr, knowledgefile.ErrCorrupt) {
		return allErr
	}
	recent, runsErr := c.recent(ctx, sessionID, trace.NameRefill)
	answered := recent.runs().answered()
	previous := trace.Traces(answered).Newest()
	refill := recent.newest(trace.NameRefill, previous.Time)
	items := hookItems(all.For(sessionProducer, labels))
	if c.holdout.WithholdsRun(sessionProducer, sessionID) {
		items = nil
	}
	held := c.hold.Held(items.fitting(), sessionProducer, sessionID)
	replied, replyErr := c.reply(ctx, sessionID, previous, message)
	var recorded feedback.Verdict
	var reactErr error
	if replied == "" {
		recorded, reactErr = c.react(ctx, turn{run: previous, message: message})
	}
	note := c.note(sessionID, previous)
	note.recorded, note.replied = recorded, replied
	text := items.context(answered.inContext(knowledge.Set(items), refill), held, note)
	runsErr = errors.Join(turnErr, allErr, runsErr, replyErr, reactErr)
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
func (ts sessionTraces) turnStart(previous trace.Trace, continued bool) time.Time {
	if turn := ts.newest(trace.NameTurn, previous.Time); turn.ID != "" && turn.Time.After(previous.Time) {
		return turn.Time
	}
	if continued && previous.ID != "" {
		return previous.Time
	}
	return time.Time{}
}

// The items a session start added as a refill trace of the session
func (c hookCommand) recordRefill(ctx context.Context, sessionID string, added knowledge.Set) error {
	in, err := knowledge.NewRunInput(added.Refs(), nil, c.plugin)
	if err != nil {
		return err
	}
	// Refs and strings always encode
	input, _ := json.Marshal(in)
	store, err := c.app.traces()
	if err != nil {
		return err
	}
	now := c.app.now()
	tr := trace.Trace{ID: trace.NewID(now), Name: trace.NameRefill, SessionID: sessionID, Time: now, Input: input}
	return store.Append(ctx, tr.Record())
}

// The runs of the session within sessionWindow newest first back to the run before its newest answer with the traces of the other names since
// 1. the run before the answer bounds the turn the answer ended so the read reaches it too
// 2. the read starts at two traces and doubles past continued runs since another stop hook may continue a turn more than once
// 3. it ends at the first read that holds both or returns fewer traces than it asked for
// 4. the other names come in the same read so a session with no run yet reads its window once
// 5. an empty id would match the traces of every session so it has none
// 6. a corrupt line returns the traces found with the error
func (c hookCommand) recent(ctx context.Context, sessionID string, others ...trace.Name) (sessionTraces, error) {
	if sessionID == "" {
		return nil, nil
	}
	store, err := c.app.traces()
	if err != nil {
		return nil, err
	}
	f := record.TraceFilter{Names: []string{string(trace.NameRun)}, SessionID: sessionID, Since: c.app.now().Add(-sessionWindow)}
	for _, name := range others {
		f.Names = append(f.Names, string(name))
	}
	for f.Limit = 2; ; f.Limit *= 2 {
		found, err := store.List(ctx, f)
		recent := sessionTraces(trace.RestoreAll(found))
		if len(found) < f.Limit || len(recent.runs().answered()) > 1 {
			return recent, err
		}
	}
}

// The traces of a session newest first
type sessionTraces trace.Traces

func (ts sessionTraces) runs() sessionTurns {
	return sessionTurns(ts.of(trace.NameRun))
}

// The traces of the name newest first
func (ts sessionTraces) of(name trace.Name) trace.Traces {
	return slices.DeleteFunc(slices.Clone(trace.Traces(ts)), func(t trace.Trace) bool { return t.Name != name })
}

// The newest trace of the name at the time or later and the zero trace when none is
func (ts sessionTraces) newest(name trace.Name, since time.Time) trace.Trace {
	i := slices.IndexFunc(ts, func(t trace.Trace) bool { return t.Name == name && !t.Time.Before(since) })
	if i < 0 {
		return trace.Trace{}
	}
	return ts[i]
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
// 2. the holdout draws once per session so every turn of a conversation is in one arm
// A lesson shown in one turn stays in the context of the next so a draw per turn would leak it into the withheld arm
// 3. the capture, the daily retention prune, the daily consolidate, the daily curate pass and the OTLP export start in processes of their own
// so the turn never waits for them
// A failure to start one is printed and the others still start
// A failed record starts no export since no new run waits to be sent
// 4. the capture reads the person's words and extracts the run they corrected so the stop drafts nothing itself
// 5. a host whose stop input carries no answer records no run and still starts the background work
// 6. the stop talks to the person last and only when everything before it went through and no stop hook continued the turn
func (c hookCommand) stop(ctx context.Context, sessionID string, labels trace.Labels, answer reply, continued bool, t transcript) error {
	if c.host.answers() && strings.TrimSpace(string(answer)) == "" {
		return nil
	}
	recent, runsErr := c.recent(ctx, sessionID, trace.NameTurn)
	if runsErr != nil && !errors.Is(runsErr, tracefile.ErrCorrupt) {
		return runsErr
	}
	previous := trace.Traces(recent.runs()).Newest()
	all, allErr := c.all(ctx)
	if allErr != nil && !errors.Is(allErr, knowledgefile.ErrCorrupt) {
		return allErr
	}
	corrupt := errors.Join(runsErr, allErr)
	var failed error
	var run trace.Trace
	if c.host.answers() {
		run, failed = c.record(ctx, sessionID, labels, answer, all, recent.turnStart(previous, continued), continued)
	}
	if c.export != nil && c.host.answers() && failed == nil {
		failed = c.app.startExport(c.export)
	}
	failed = errors.Join(failed, c.capture(sessionID, t, continued), c.prune(ctx), c.consolidate(all), c.curate())
	if failed == nil && !continued && sessionID != "" {
		failed = c.talk(ctx, sessionID, knowledge.RunScope{Producer: sessionProducer, Labels: labels}, run.ID, all)
	}
	return errors.Join(corrupt, failed)
}

// Starts the capture of the session in a process of its own after a stop of a host that records runs
// 1. a stop without a session id starts none since the capture reads one session
// 2. with a transcript the capture reads it starts in every mode and after a turn a stop hook continued too
// A click on a question asked in manual mode or during that turn is read there
// 3. without one, as on Codex, it starts only after an answer of a mode that infers
// The capture then reads no message and extracts the answer the turn followed when the conversation corrected it
// So that correction is decided at the next stop as on Claude Code and never waits for the daily pass
func (c hookCommand) capture(sessionID string, t transcript, continued bool) error {
	switch {
	case !c.host.answers() || sessionID == "":
		return nil
	case c.host.transcripts() && t != "":
		return c.detach([]string{"session", "capture", "--session", sessionID, "--transcript", string(t)})
	case continued || !c.mode.infers():
		return nil
	}
	return c.detach([]string{"session", "capture", "--session", sessionID})
}

// The answer as a run of the session with every fitting item of the place in its context and the duration of its turn
// 1. a zero start leaves the duration out and still records the run
// 2. the run of a turn a stop hook continued is marked so no reader takes it for the answer the person judged
// 3. the item hold takes the versions it keeps from the session out of applied and records every fitting version with its chance
func (c hookCommand) record(
	ctx context.Context, sessionID string, labels trace.Labels, answer reply, all knowledge.Set, start time.Time, continued bool,
) (trace.Trace, error) {
	shown := hookItems(all.For(sessionProducer, labels)).fitting()
	applied := shown.Refs()
	var withheld []knowledge.Ref
	if c.holdout.WithholdsRun(sessionProducer, sessionID) {
		applied, withheld = nil, applied
	}
	in, err := knowledge.NewRunInput(applied, withheld, c.plugin)
	if err != nil {
		return trace.Trace{}, err
	}
	in.Continued = continued
	in = c.hold.Drawn(in, shown, sessionProducer, sessionID)
	// Refs, strings, flags and shares in [0, 1) always encode
	input, _ := json.Marshal(c.holdout.Drawn(in, sessionProducer, sessionID))
	tr, err := trace.NewRun(sessionProducer, "", labels, input, []byte(answer.text()), c.app.now())
	if err != nil {
		return trace.Trace{}, err
	}
	tr.SessionID = sessionID
	if !start.IsZero() {
		if tr, err = tr.Took(tr.Time.Sub(start)); err != nil {
			return trace.Trace{}, err
		}
	}
	store, err := c.app.traces()
	if err != nil {
		return trace.Trace{}, err
	}
	return tr, store.Append(ctx, tr.Record())
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
	// The choice the first word named of the question shown after that answer so the message judges nothing
	replied curate.Choice
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
	case n.replied != "":
		return fmt.Sprintf(replyNote, n.replied)
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

// The mode a value of config.json reads as
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

// The mode session_mode of config.json saves and deferred when it saves none
// 1. no variable overrides it since the env of a project settings file reaches the hooks and an agent may write that file
// 2. a value outside the set is refused so the hooks record nothing the person did not choose
func sessionModeOf(configured string) (sessionMode, error) {
	mode := sessionMode(configured).read()
	switch {
	case configured == "":
		return sessionDeferred, nil
	case !mode.valid():
		return "", fmt.Errorf("%w: session_mode in %s is %q. %s", errSessionModeUnknown, configFile, configured, sessionModeHint)
	}
	return mode, nil
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
