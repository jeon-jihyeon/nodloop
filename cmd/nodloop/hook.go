package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
	sessionWindow = 7 * 24 * time.Hour
)

// The hooks of a Claude Code conversation
// 1. every failure goes to stderr and the exit code is 0 because a hook that fails must never block a prompt or a stop
// 2. stdin is the hook input JSON of Claude Code
func runHook(args []string, getenv func(string) string, start starter, now func() time.Time, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "nodloop hook:", errNoAction)
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
	var in hookInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		fmt.Fprintln(stderr, "nodloop hook:", err)
		return 0
	}
	cmd := hookCommand{
		app: a, start: start, out: stdout, mode: mode,
		plugin: cmp.Or(getenv(envPluginVersion), buildVersion()), holdout: a.cfg.holdout,
	}
	if args[0] == "prompt" {
		if cmd.reaction, err = a.plan(classify.PointReaction, conversation{}, getenv, reactionTimeout); err != nil {
			fmt.Fprintln(stderr, "nodloop hook:", err)
		}
	}
	labels := workDir(in.Cwd).within(workDir(getenv(envProjectDir))).labels()
	ctx := context.Background()
	switch args[0] {
	case "prompt":
		err = cmd.prompt(ctx, in.SessionID, in.Prompt, labels)
	case "stop":
		err = cmd.stop(ctx, in.SessionID, labels, reply(in.LastAssistantMessage))
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, args[0])
	}
	if err != nil {
		fmt.Fprintln(stderr, "nodloop hook:", err)
	}
	return 0
}

// The fields of the Claude Code hook input the two hooks read
type hookInput struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	// Read only for the reaction point and never stored except as the reason of a reject it records
	Prompt               string `json:"prompt"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

type hookCommand struct {
	app   app
	start starter
	out   io.Writer
	mode  sessionMode
	// The version the launcher ran this binary for and else the build version
	plugin  string
	holdout holdout
	// The reaction point the user set up and nil when the conversation judges alone
	reaction *classify.Plan
}

// The approved items of this place and the session note added to the prompt as context, or nothing when neither has a line
// 1. a failed read of the runs still adds the items
// 2. a corrupt line in the records leaves out that record and the rest still reach the prompt
// 3. a turn the holdout draws gets the note and no item
func (c hookCommand) prompt(ctx context.Context, sessionID, message string, labels trace.Labels) error {
	all, allErr := c.all(ctx)
	if allErr != nil && !errors.Is(allErr, knowledgefile.ErrCorrupt) {
		return allErr
	}
	previous, runsErr := c.previousRun(ctx, sessionID)
	items := hookItems(all.For(sessionProducer, labels))
	if c.holdout.withholds(sessionID, previous.ID) {
		items = nil
	}
	recorded, reactErr := c.react(ctx, turn{run: previous, message: message})
	note := c.note(sessionID, labels, all, previous)
	note.recorded = recorded
	text := items.context(note)
	runsErr = errors.Join(allErr, runsErr, reactErr)
	if text == "" {
		return runsErr
	}
	// Items quote commands such as a && b so the text stays as written
	enc := json.NewEncoder(c.out)
	enc.SetEscapeHTML(false)
	return errors.Join(enc.Encode(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "UserPromptSubmit", "additionalContext": text,
	}}), runsErr)
}

// The previous run of the session and the candidates waiting in the place
// 1. the first prompt counts every candidate waiting
// 2. a later prompt in immediate mode counts the candidates drafted since the previous answer so each is asked about once in the session that corrected it
// 3. a later prompt in deferred mode counts none so the review comes once per session
// 4. a mode that infers nothing and a prompt without a session id get no note
func (c hookCommand) note(sessionID string, labels trace.Labels, all knowledge.Set, run trace.Trace) sessionNote {
	if !c.mode.infers() || sessionID == "" {
		return sessionNote{}
	}
	waiting := all.Waiting(sessionProducer, labels)
	switch {
	case run.ID == "":
	case c.mode.inTurn():
		waiting = waiting.Since(run.Time)
	default:
		waiting = nil
	}
	return sessionNote{mode: c.mode, run: run.ID, waiting: len(waiting)}
}

// The newest run of the session within sessionWindow or none
// 1. an empty id would match the runs of every session so it has none
// 2. a corrupt line returns the run found with the error
func (c hookCommand) previousRun(ctx context.Context, sessionID string) (trace.Trace, error) {
	if sessionID == "" {
		return trace.Trace{}, nil
	}
	store, err := c.app.traces()
	if err != nil {
		return trace.Trace{}, err
	}
	since := c.app.now().Add(-sessionWindow)
	runs, err := store.List(ctx, trace.Filter{Name: trace.NameRun, SessionID: sessionID, Since: since, Limit: 1})
	return runs.Newest(), err
}

// The answer as a run of the conversation with the items its prompt received, then the lesson of the previous run
// 1. an empty answer such as an interrupted turn records nothing
// 2. the previous run is the one the prompt hook named this turn, so each run is looked at for a lesson once
// 3. a correction a knowledge record already cites is not drafted again in the background
// The conversation usually drafted it in the same turn
func (c hookCommand) stop(ctx context.Context, sessionID string, labels trace.Labels, answer reply) error {
	if strings.TrimSpace(string(answer)) == "" {
		return nil
	}
	previous, runsErr := c.previousRun(ctx, sessionID)
	if runsErr != nil && !errors.Is(runsErr, tracefile.ErrCorrupt) {
		return runsErr
	}
	all, allErr := c.all(ctx)
	if allErr != nil && !errors.Is(allErr, knowledgefile.ErrCorrupt) {
		return allErr
	}
	corrupt := errors.Join(runsErr, allErr)
	shown, _ := hookItems(all.For(sessionProducer, labels)).fitting()
	refs := make([]knowledge.Ref, 0, len(shown))
	for _, k := range shown {
		refs = append(refs, knowledge.Ref{ID: k.ID, Version: k.Version})
	}
	in := runInput{Applied: refs, Plugin: c.plugin}
	if c.holdout.withholds(sessionID, previous.ID) {
		in.Applied, in.Withheld = []knowledge.Ref{}, refs
	}
	input, err := json.Marshal(in)
	if err != nil {
		return err
	}
	tr, err := trace.NewRun(sessionProducer, "", labels, input, []byte(answer.text()), c.app.now())
	if err != nil {
		return err
	}
	tr.SessionID = sessionID
	store, err := c.app.traces()
	if err != nil {
		return err
	}
	if err := store.Append(ctx, tr); err != nil {
		return err
	}
	if all.Cites(previous.ID) {
		return corrupt
	}
	return errors.Join(corrupt, c.extract(ctx, previous.ID, previous.Time))
}

// The extraction of the run started when the conversation alone judged it and its latest verdict corrects it
// 1. a run with a verdict of a person is left to the nod skill even after a later inferred one because the person's verdict wins
// A person's withdraw is such a verdict so a withdrawn run is never drafted
// 2. it runs as `knowledge extract` in a process of its own because Claude Code may end an async hook when it exits
// 3. the verdicts are read from the end of the records back to the run since none comes before it
func (c hookCommand) extract(ctx context.Context, runID string, at time.Time) error {
	if !c.mode.infers() || runID == "" {
		return nil
	}
	verdicts, err := c.app.feedback()
	if err != nil {
		return err
	}
	list, err := verdicts.List(ctx, feedback.Filter{TraceID: runID, Since: at})
	if err != nil {
		return err
	}
	records := feedback.Records(list)
	if len(records.Human()) > 0 {
		return nil
	}
	latest := records.Latest()
	if len(latest) == 0 || !latest[0].Corrects() {
		return nil
	}
	return c.start([]string{"knowledge", "extract", "--from", runID})
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

// One line per item under the lead, then the session note
// 1. the items are cut at promptRunes with the count left out so the model knows the list is partial
// 2. the note is left out when it would pass contextRunes
func (items hookItems) context(note sessionNote) string {
	var b strings.Builder
	if len(items) > 0 {
		shown, lines := items.fitting()
		b.WriteString(knowledge.PromptLead)
		for _, line := range lines {
			b.WriteString(line)
		}
		if left := len(items) - len(shown); left > 0 {
			fmt.Fprintf(&b, "- %d more items left out over the size cap\n", left)
		}
	}
	if text := note.text(); utf8.RuneCountInString(b.String())+utf8.RuneCountInString(text) <= contextRunes {
		b.WriteString(text)
	}
	return b.String()
}

// The items that fit the prompt in order with their lines
// The prompt hook shows them and the stop hook records them as applied, so both name the same items
func (items hookItems) fitting() (knowledge.Set, []string) {
	size := utf8.RuneCountInString(knowledge.PromptLead)
	var shown knowledge.Set
	var lines []string
	for _, k := range items {
		line := k.PromptLine()
		if size+utf8.RuneCountInString(line) > promptRunes {
			break
		}
		size += utf8.RuneCountInString(line)
		shown, lines = append(shown, k), append(lines, line)
	}
	return shown, lines
}

// What the prompt hook tells the conversation about its session
type sessionNote struct {
	mode sessionMode
	// The previous run of the session, empty on its first prompt
	run string
	// Candidates waiting for approval in the place
	waiting int
	// The verdict the reaction point recorded on the previous run and empty when the conversation judges it
	recorded feedback.Verdict
}

// The reaction a conversation records is an inferred verdict, so only words that judge the previous answer count
// The verdict is recorded without a word to the user and its lesson is drafted after the turn so a discussion goes on undisturbed
const deferredReactionNote = "nodloop: your previous answer in this conversation is run %s. " +
	"If the user's message says that answer was wrong or right, call the nodloop feedback tool on trace %s with reviewer session: " +
	"verdict reject with what was wrong in the user's words and a reason code, or approve. " +
	"Do it without telling the user and go on with what they asked. Its lesson is drafted after the turn and reviewed in a later session. " +
	"Skip it for a new request, a follow up, a change of topic, or when the user calls /nodloop:nod.\n"

// In immediate mode a correction is drafted in the same turn so the user is asked while the correction is fresh
const reactionNote = "nodloop: your previous answer in this conversation is run %s. " +
	"If the user's message says that answer was wrong or right, first call the nodloop feedback tool on trace %s with reviewer session: " +
	"verdict reject with what was wrong in the user's words and a reason code, or approve. " +
	"After a reject or an edit, do what the user asked, then invoke the nodloop:nod skill and follow its section Draft the correction of this turn. " +
	"Skip all of it for a new request, a follow up, a change of topic, or when the user calls /nodloop:nod.\n"

// The review starts without /nodloop:nod and only the user's answer approves or retires a draft
const waitingNote = "nodloop: knowledge candidates for this place wait for approval: %d. " +
	"After your answer, review them without waiting for /nodloop:nod: invoke the nodloop:nod skill and follow its section " +
	"Review the waiting drafts, which asks the user about each draft.\n"

// The reaction point already recorded a reject so the conversation records nothing more
const deferredRecordedNote = "nodloop: the user's message was recorded as a reject of your previous answer, run %s. " +
	"Do what the user asked. Its lesson is drafted after the turn and reviewed in a later session.\n"

// In immediate mode the conversation drafts the correction the reaction point recorded
const recordedNote = "nodloop: the user's message was recorded as a reject of your previous answer, run %s. " +
	"Do what the user asked, then invoke the nodloop:nod skill and follow its section Draft the correction of this turn.\n"

func (n sessionNote) text() string {
	reaction, recorded := deferredReactionNote, deferredRecordedNote
	if n.mode.inTurn() {
		reaction, recorded = reactionNote, recordedNote
	}
	var b strings.Builder
	switch {
	case n.run == "", n.recorded == feedback.VerdictApprove:
	case n.recorded == feedback.VerdictReject:
		fmt.Fprintf(&b, recorded, n.run)
	default:
		fmt.Fprintf(&b, reaction, n.run, n.run)
	}
	if n.waiting > 0 {
		fmt.Fprintf(&b, waitingNote, n.waiting)
	}
	return b.String()
}

// How a conversation records and reviews the verdicts it infers
type sessionMode string

const (
	sessionDeferred  sessionMode = "deferred"  // infers each verdict silently, drafts after the turn and asks once per session
	sessionImmediate sessionMode = "immediate" // drafts a correction and asks about it in the same turn
	sessionManual    sessionMode = "manual"    // records answers and items and leaves every verdict to an explicit nod
	sessionOff       sessionMode = "off"       // records nothing
)

var sessionModes = []sessionMode{sessionDeferred, sessionImmediate, sessionManual, sessionOff}

const sessionModeHint = "Use deferred, immediate, manual or off"

func (m sessionMode) valid() bool {
	return slices.Contains(sessionModes, m)
}

// Whether the conversation records the verdicts it reads and the stop hook drafts their lessons
func (m sessionMode) infers() bool {
	return m == sessionDeferred || m == sessionImmediate
}

// Whether a correction is drafted and asked about in the turn that recorded it
func (m sessionMode) inTurn() bool {
	return m == sessionImmediate
}

// The mode of the process
// 1. NODLOOP_SESSION wins, then session_mode in config.json, then deferred
// 2. a value outside the set is refused with where it came from so the hooks record nothing the user did not choose
func sessionModeOf(env, configured string) (sessionMode, error) {
	switch {
	case env != "" && !sessionMode(env).valid():
		return "", fmt.Errorf("%w: %s is %q. %s", errSessionModeUnknown, envSession, env, sessionModeHint)
	case env != "":
		return sessionMode(env), nil
	case configured != "" && !sessionMode(configured).valid():
		return "", fmt.Errorf("%w: session_mode in %s is %q. %s", errSessionModeUnknown, configFile, configured, sessionModeHint)
	}
	return cmp.Or(sessionMode(configured), sessionDeferred), nil
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

// The share of turns whose prompt receives no item so report effect can compare turns with and without them
type holdout float64

// Whether the turn withholds every item
// 1. the draw hashes the session and the id of its previous run so the prompt hook and the stop hook of one turn agree
// The first turn of a session hashes an empty id
// SHA-256 because the high bits of FNV barely move between similar ids
// 2. a turn without a session id is never drawn
func (h holdout) withholds(sessionID, previous string) bool {
	if h <= 0 || sessionID == "" {
		return false
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s:%s", sessionID, previous))
	return float64(binary.BigEndian.Uint64(sum[:8]))/math.MaxUint64 < float64(h)
}
