package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

const usage = `usage: nodloop <command>

Records the output of any producer as a run, takes a person's verdict on it and carries the approved
corrections to the next run in the same place

commands:
  run record --producer <p> --output <file> [--label <key=value>] [--subject <s>] [--applied <id:version>]
             [--withheld <id:version>] [--holdout-key <k>]
                            Record one output of any producer as a run and print its id for feedback.
                            Output that is not JSON is kept as text. --withheld names the items knowledge for printed as
                            withheld and --holdout-key the key it drew with so report effect counts the run in its arm
  hook start | hook prompt | hook stop | hook subagent | hook instructions [--host claude-code|codex|cursor|copilot]
                            The conversation hooks the plugin registers. prompt adds the approved items of
                            producer session for the repo and dir of the place that the context lacks, so each version is
                            added once per conversation. The place is cwd while it lies inside CLAUDE_PROJECT_DIR and that
                            directory otherwise. It then names the newest answer of the session that no stop hook
                            continued so the conversation records the user's verdict on it. start adds every item again
                            after a compact or a clear and subagent adds them to a subagent. stop records the answer as a
                            run, marked continued when a stop hook continued the turn, and drafts the lesson of the newest
                            answer before it in the background when the conversation recorded a correction no record
                            cites. instructions records the path and an HMAC of the content of each CLAUDE.md and rules
                            file Claude Code loads, leaving out approved.md and auto memory. All exit 0.
                            --host reads the input and writes the answer of that agent and labels the run host,
                            claude-code by default. codex runs every hook. cursor and copilot run start and stop alone:
                            start adds every item and stop records no run since their input holds no answer. Their hook
                            files are in examples/hooks. The session mode is NODLOOP_SESSION, then config session_mode,
                            then deferred: deferred records verdicts silently and drafts their lessons after the turn,
                            manual leaves out the note and off records nothing. immediate of an earlier release reads as
                            deferred
  feedback list [--trace <id>] [--verdict <v>] [--reviewer <r>] [--limit <n>]
                            List feedback newest first with the reason code or a dash after the verdict
  feedback add --trace <id> --verdict <v> --reviewer <r> [--reason-code <c>] [--reason <r>] [--edited <file>] [--audit]
                            Append one verdict on a run under the name of who judged. An edit carries the corrected output in full.
                            An edit or reject may name what the output got wrong: fact, approach, scope, form or other.
                            withdraw takes back the verdict before it so the run has none, as when a session inferred one wrongly.
                            The reviewer nodloop is refused, since only the capture of a session records verdicts under it
  feedback outcome --trace <id> --result <r> --reviewer <r> [--cause <text>] [--note <n>]
                            Record what a real check found: confirmed, refuted or inconclusive
  knowledge propose --kind <k> --content <text> (--producer <p> [--label <key=value>] | --from <run id>)
                    [--except <key=value>] [--id <id>] [--basis stated or verified] [--author <a>] [--trace <run id>]
                    [--evidence-feedback <id>] [--evidence-outcome <id>] [--new-labels]
                    [--veto-tool <t> --veto-field <f> --veto-match <re> [--veto-unless <re>] --veto-example <json>]
                            Add a candidate scoped to the runs of a producer and list its overlaps. Every label must be one
                            a recorded run carries unless --new-labels allows new values such as a new tenant. --from fills
                            producer, labels and evidence from a run a person corrected. A judgment with a veto becomes a
                            guard veto once approved
  knowledge extract --from <run id> [--model <m>] [--author <a>]
                            Draft through claude -p what a correction of the run taught, check it against the items the run
                            reaches with the critic, a second claude call or the endpoint set for the critic point, and
                            propose an add or an update. A duplicate or a conflict names the item
  knowledge replay <id> [--version <n>] [--model <m>]
                            Judge the version through claude -p against the outputs it should change, the corrected runs it
                            cites, and those it should leave alone, the newest ten approved runs its scope reaches. A lesson
                            that would change an approved output reaches too far. Recorded as a replay trace and shown by
                            knowledge waiting. It never blocks an approval
  knowledge consolidate [--daily] [--model <m>]
                            Ask the model in one call per cluster of waiting candidates one run may carry together which
                            ones say the same lesson, and print one line per group. Recorded as a consolidate trace per
                            producer and nothing else changes. --daily runs only when consolidate.json says a day passed
                            and prints nothing, as hook stop starts it in a process of its own once two candidates wait
  knowledge for --producer <p> [--label <key=value>] [--holdout-key <k>]
                            The approved items a run of the producer with these labels applies, and their size. With a
                            holdout key a share of runs gets a withheld line per item and no item. Pass the same key and
                            those refs to run record
  knowledge waiting --producer <p> [--label <key=value>]
                            The candidates a run of the producer with these labels would receive once approved, newest per
                            id and those taught by more runs first, with the runs whose verdicts taught them
  knowledge list [--status <s>] [--kind <k>] [--stale]
                            Current version per id with a stale column. --status lists every version of that status
  knowledge show <id>       Every record of one id
  knowledge health          Verdict and outcome counts per knowledge version, retire candidates and review deadlines,
                            idle versions no run applied for 30 days and contested ones corrected again for their reason
                            with the items to compact them with
  knowledge audit           References of current items that no longer resolve
  knowledge reaffirm <id> [--approver <name>] [--version <n>]
                            Record that you rechecked the approved version. Its review deadline starts again
  knowledge narrow <id> --version <n> --key <label key> [--author <a>]
                            Propose the next version that excepts the values of that label its refuted runs carried
  knowledge promote <id> --version <n> [--author <a>]
                            Propose the next version with basis verified and the runs whose outcome confirmed it
  knowledge overlaps <id>   Current items of the same kind that one run may carry together
  knowledge approve <id> --version <n> [--approver <name>] [--label <key=value> | --everywhere]
                            Refused when one run would carry more than 9000 chars, when it would lift the veto of the
                            approved version or reach runs it never reached, or when it was not built from that version.
                            --label approves a candidate for the runs that carry these labels instead of the proposed
                            ones and --everywhere for every run of its producer. Each label must be one a recorded run
                            carries
  knowledge retire <id> --version <n> [--approver <name>]
                            approve, retire, reaffirm and approve-compaction decide only at an interactive terminal outside
                            an agent session, under --approver, then config approver, then person. From a script or the
                            shell of an agent they record a proposal the person is asked about and change nothing.
                            The name nodloop is refused
  knowledge import --file <jsonl>
                            Append the records of a file not yet recorded, only at an interactive terminal outside an
                            agent session since the records name their approvers
  knowledge compact <id> [--model <m>] [--author <a>]
                            Draft through claude -p a smaller set of items for the runs the item reaches, with nothing said
                            twice and nothing lost, and propose it
  knowledge check <compaction id> [--model <m>] [--replay]
                            One claude -p call lists for every old item the new items that state it and the facts they lose,
                            and records it as the coverage check. --replay then replays every new item, whose evidence joins
                            the corrections of the items it replaces, and totals what they miss and where they reach too far
  knowledge compaction <compaction id>
                            Print the new and old items and the newest coverage check
  knowledge approve-compaction <compaction id> [--approver <name>]
                            Approve the new items and retire the old ones once the coverage check passed
  knowledge export          Write the approved vetoes and approved.md of the record directory again after a failed export
                            and print the line that imports approved.md from a CLAUDE.md
  queue [--limit <n>] [--audit-rate <share>] [--seed <n>]
                            Runs without a verdict in the order to check them, with a random audit share
  report online [--since <RFC3339>]
                            Weekly verdict rates, waits and edit widths, and the runs with and without knowledge compared
  report loop               A window line with the start of the runs read or all. A totals line of runs, verdicts by a person and by a session, corrections, candidates waiting
                            and approved items. A scope line per plugin version: current items, those whose scope admits
                            one session at most and approved ones never applied. A drafts line per plugin version and
                            drafting path: drafted, approved, dropped, waiting and the median time to a decision. Then per
                            approved item: runs that applied it, how many a person approved of
                            those judged, how many were corrected again for the reason that taught it, the same two counts
                            over verdicts a session inferred, and the time from that correction to the approval
  report effect             A window line with the start of the runs read and the drawn runs per arm. Then per producer,
                            drawn runs that applied items against drawn runs the holdout kept them from: runs, judged,
                            corrected and corrected again for a reason an item was taught by, then the draw units, the
                            corrected rate with its 95% Wilson interval widened for runs of one unit, the judged rate, the expected share, audit and outcome counts, runs
                            recorded without a holdout key, the sample ratio p value, the difference of the rates with its
                            95% Newcombe interval and the warnings srm, judged_rate, insufficient and mixed_shares
  report extract            Per plugin version of the corrected run and per drafting path: extractions by how they
                            ended, refused drafts by what refused them, and the critic questions answered false
  report critic             A window line, then per critic: drafts judged and how often a pass or a refusal matched what a person later
                            decided on candidates of the same run, with the runs nobody decided yet. Then per member of the
                            reaction point: answers asked and sure, and how they matched the verdict the conversation recorded
                            on the same run as agree, disagree, missed correction, missed approval and deferred
  report replay             Per knowledge version its newest replay: passed or failed, corrected outputs it missed and
                            approved outputs it would have changed
                            --json on loop, effect, extract, critic and replay prints the report as one JSON value
  trace list [--name <n>] [--session <id>] [--subject <s>] [--limit <n>]
                            List traces newest first
  trace show <id>           Print one trace as JSON
  schema [<kind>]           Print the published JSON Schema of the newest version of a record kind, or every kind with
                            its newest version and the file it is stored in. Every record carries schema_version, and a
                            record of an earlier release without it is version 1
  records prune [--dry-run | --daily]
                            Remove the runs older than config retention that no verdict, outcome or knowledge version
                            cites, with the classify traces of those runs, whatever the daily marker says. The first
                            run that received and the newest run that applied each approved item stay. --dry-run
                            prints what it would remove and keep.
                            --daily prunes only when prune.json says a day passed and prints nothing. Every prune keeps
                            its cutoff in prune.json and every report reads from the later of that cutoff and config
                            retention. run record and the MCP run tool prune at most once a day after a run and hook stop
                            starts records prune --daily in a process of its own
  guard [--vetoes <path>]   PreToolUse hook. Reads hook input from stdin and blocks calls that match a veto,
                            or asks the person when the veto says action ask and no matching veto blocks.
                            Without --vetoes, loads every .claude/nodloop/vetoes.yaml from the hook cwd up to the git root
                            and from $HOME and then every approved veto file under $HOME/.claude/nodloop.
                            Logs every block and ask to ~/.nodloop/guard.jsonl
  guard log [--limit <n>]   Logged blocks and asks newest first. 20 by default and 0 prints every one
  guard call --tool <t> [--input <json>] [--dir <d>]
                            Check one tool call an agent outside Claude Code is about to make against the vetoes the hook
                            would apply in dir. Prints action allow, block or ask with the veto and its reason as JSON
  guard check               Load the veto files guard reads for the current directory, report counts or errors
                            and whether the hook is installed
  guard install             Register this binary as a PreToolUse hook in ~/.claude/settings.json (backs up first)
                            Registers ~/.nodloop/bin/nodloop when it links to this binary and replaces a stale hook
  guard uninstall           Remove the hook registered by guard install
  mcp [--agent]             Serve the MCP tools on stdio. --list prints the tool names. --agent serves the server the plugin
                            starts: no approve, reaffirm or approve_compaction, and every verdict and outcome recorded as the
                            session's. Over stdio every call that decides records a proposal
  llm probe [--model <m>]   Send a minimal structured-output request through the configured backend and print cost.
                            The model is haiku on claude and the configured model on chat by default
  llm set chat --base-url <u> --model <m> [--key-env <ENV>] | llm set claude [--model <m>]
                            Call an OpenAI compatible Chat Completions endpoint such as http://localhost:11434/v1 of
                            Ollama instead of claude -p in every command that calls a model, or claude with a default
                            model NODLOOP_LLM_MODEL still overrides. The key is read from the env when a command starts
                            and never saved
  llm unset                 Every command calls claude -p again
  config                    Every setting with its value and where it came from: an environment variable, config.json
                            or the default
  config approver [<name>]  Print the name saved in ~/.nodloop/config.json to decide under, or save one, which only an
                            interactive terminal outside an agent session does. The name nodloop is refused
  config holdout [<share>]  Print or save the share of draws that get no item so report effect can compare: each Claude
                            Code conversation is one draw, and each holdout key a producer passes to knowledge_for, to
                            knowledge for --holdout-key or to the nodloop-server it reads at start is one draw.
                            The Go package takes WithHoldout instead. 0 by default
  config retention [<days>] Print or save the whole days of unjudged and uncited runs the records keep. 0 by default
                            keeps every run and fewer than 7 is refused. Reports then read the runs of that window and
                            the text of report loop, critic and effect names its start in a first window line
  config session_mode [<mode>]
                            Print or save how a conversation records the verdicts it infers: deferred, manual or off.
                            deferred by default and NODLOOP_SESSION overrides it
  anchor [add <path> | remove <path>]
                            List what nodloop reads as your judgment on record in every place: the lessons you approved,
                            the files you designate and the CLAUDE.md and rules files your sessions there loaded, and
                            record a new anchor version when one of them changed. add and remove save a file you keep as
                            direction, such as a plan or a lessons file, in config.json, only at an interactive terminal
                            outside an agent session. A file that cannot be read decides nothing
  classifier set <point> --url <u> [--model <m>] [--key-env <ENV>]
                            Ask an endpoint of the Jev wire format the README shows at a decision point before claude,
                            such as http://localhost:8000/v1/classify. claude answers when the endpoint fails or is less
                            than 0.8 sure of any answer. Points: critic, the second reader of a drafted lesson, reaction,
                            which judges whether the user's message corrects or approves the previous answer before the
                            conversation does, and judge, where the endpoint answers beside the three claude judges
                            whether your recorded decisions settle a change curate prepared. The key is read from the
                            env at call time and never saved. Every answer is recorded as a classify trace
  classifier unset <point>  The point asks claude alone again
  classifier list           Every point with its endpoint or claude alone
  classifier probe <point>  Ask the endpoint of the point one question and print its answer and time
  server                    Moved to its own binary nodloop-server in 0.7.0
  otel set --endpoint <url> [--protocol http/protobuf|http/json] [--headers-env <ENV>] [--no-logs] [--content]
                            Export every record on its own from now on: the stop hook, nodloop mcp and the background
                            extractions start export otel --pending after what they record. The endpoint is the base URL
                            /v1/traces and /v1/logs join, such as http://127.0.0.1:4318. Headers are read from the variable
                            --headers-env names at each export and never saved. --no-logs sends traces only, for Langfuse,
                            Phoenix or Datadog. --content also sends outputs, reasons, notes and candidate text
  otel unset                Export nothing on its own again
  otel                      Print the saved target
  export otel [--endpoint <url>] [--protocol <p>] [--header <Name=value>] [--since <RFC3339> | --pending] [--with-content]
                            Send the records as OTLP traces and logs of the OpenTelemetry GenAI and OpenInference conventions:
                            a span per turn from prompt to stop, verdicts, outcomes and decisions as evaluation spans
                            with log records, and every model step with its usage. Ids derive from the records
                            so a record sent again is never counted twice. --endpoint is a traces URL and sends traces only.
                            Without it the target of otel set is used, then OTEL_EXPORTER_OTLP_* of the environment.
                            --pending sends only what came after export.json of the record directory and moves it
  doctor [--repair]         Read every record file and the guard log, and name the lines that fail to decode.
                            A read skips such a line, and --repair moves them to <file>.corrupt. Run it while nothing writes
  version                   Print the build version
  help [<command>]          Print this text or the part of one command. -h or --help as the first flag does the same

Every command that reads records accepts --record-dir, which overrides NODLOOP_RECORD_DIR, then record_dir of
~/.nodloop/config.json, then ~/.nodloop/records
An approved version is stale 90 days after its approval or last reaffirm. Nothing is approved or retired without a person
`

// Set by goreleaser through ldflags
// dev in a source build
var version = "dev"

// The ldflags version when set and otherwise the module version go install recorded
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

// The version the plugin launcher runs this binary for
// Empty outside the plugin
type pluginVersion string

// One sentence naming both versions and the binary when they differ
// 1. a leading v on either side is the same version
// 2. empty outside the plugin because nothing is expected there
// 3. a dev build differs from every release because its tools may be older or newer than the skills
func (want pluginVersion) mismatch(have, exe string) string {
	expected := strings.TrimPrefix(string(want), "v")
	if expected == "" || expected == strings.TrimPrefix(have, "v") {
		return ""
	}
	return fmt.Sprintf("%s is nodloop %s while the plugin runs version %s, so a tool or a flag the skills name may be missing or work differently. "+
		"Restart Claude Code with network access so the plugin fetches v%s, or put a build of v%s on PATH", exe, have, expected, expected, expected)
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr))
}

// Subcommands return their exit code and only main exits the process
// The process values come in as arguments so tests run in parallel and the clock is set here once
func run(args []string, getenv func(string) string, stdin io.Reader, stdout io.WriteCloser, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 1
	}
	if words, ok := helpWords(args); ok {
		return runHelp(words, stdout)
	}
	// Read once from the process so a command that decides for a person records what proved the person or a proposal
	// A verdict of feedback add carries it too
	person := terminalProof(stdin, stdout, getenv)
	commands := map[string]func(args []string) int{
		"help":    func(args []string) int { return runHelp(args, stdout) },
		"trace":   func(args []string) int { return runTrace(args, getenv, time.Now, stdout, stderr) },
		"records": func(args []string) int { return runRecords(args, getenv, time.Now, stdout, stderr) },
		"schema":  func(args []string) int { return runSchema(args, stdout, stderr) },
		"run":     func(args []string) int { return runRun(args, getenv, time.Now, stdout, stderr) },
		"hook": func(args []string) int {
			return runHook(args, getenv, detached(homeDir(getenv("HOME")).hookLog()), time.Now, stdin, stdout, stderr)
		},
		"feedback": func(args []string) int { return runFeedback(args, getenv, time.Now, person, stdout, stderr) },
		"queue":    func(args []string) int { return runQueue(args, getenv, time.Now, stdout, stderr) },
		"report":   func(args []string) int { return runReport(args, getenv, time.Now, stdout, stderr) },
		"guard":    func(args []string) int { return runGuard(args, getenv, os.Executable, time.Now, stdin, stdout, stderr) },
		"llm":      func(args []string) int { return runLLM(args, getenv, configuredLLM(getenv), time.Now, stdout, stderr) },
		"knowledge": func(args []string) int {
			return runKnowledge(args, getenv, configuredLLM(getenv), time.Now, person, stdout, stderr)
		},
		"mcp": func(args []string) int {
			return runMCP(args, getenv, detached(homeDir(getenv("HOME")).hookLog()), time.Now, stdin, stdout, stderr)
		},
		"config":     func(args []string) int { return runConfig(args, getenv, person, stdout, stderr) },
		"anchor":     func(args []string) int { return runAnchor(args, getenv, time.Now, person, stdout, stderr) },
		"classifier": func(args []string) int { return runClassifier(args, getenv, time.Now, stdout, stderr) },
		"server":     func([]string) int { return fail(stderr, "server", errServerMoved) },
		"doctor":     func(args []string) int { return runDoctor(args, getenv, time.Now, stdout, stderr) },
		"export":     func(args []string) int { return runExport(args, getenv, time.Now, stdout, stderr) },
		"otel":       func(args []string) int { return runOTel(args, getenv, stdout, stderr) },
		"version": func([]string) int {
			fmt.Fprintln(stdout, buildVersion())
			return 0
		},
	}
	command, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "nodloop: unknown command %q\n\n%s", args[0], usage)
		return 1
	}
	return command(args[1:])
}

// flag stops at the first positional argument so an id before the flags is taken out first
// 1. flags may follow the id as in `knowledge show <id> --record-dir <dir>`
// 2. an id after the flags is the first argument left
func parseID(fs *flag.FlagSet, args []string) (string, error) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], fs.Parse(args[1:])
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	return fs.Arg(0), nil
}

// Whether the argument asks for the usage instead of naming a command or an action
func helps(arg string) bool {
	return arg == "-h" || arg == "--help"
}

// The command and action whose usage the arguments ask for
// 1. -h or --help first asks for the usage of the words after it
// 2. otherwise -h or --help as the first flag asks for the usage of the command and action before it
// 3. a later -h is the value of a flag such as --reason -h and runs the command
// The check comes before any command so an action without flags never takes -h as its value
func helpWords(args []string) ([]string, bool) {
	if helps(args[0]) {
		return args[1:], true
	}
	for i, arg := range args[1:] {
		if strings.HasPrefix(arg, "-") {
			return args[:min(i+1, 2)], helps(arg)
		}
	}
	return nil, false
}

// One line on stderr per failed command
// 1. the command names the line once even when the error starts with it as a sentinel of the package of that name does
// 2. a usage error adds the usage of the command
func fail(stderr io.Writer, command string, err error) int {
	fmt.Fprintf(stderr, "nodloop %s: %s\n", command, strings.TrimPrefix(err.Error(), command+": "))
	if errors.Is(err, errUnknownAction) || errors.Is(err, errRequired) || errors.Is(err, errUnknownTraceName) ||
		errors.Is(err, errPruneFlags) {
		fmt.Fprintf(stderr, "\n%s", usageText(usage).of(command))
	}
	return 1
}
