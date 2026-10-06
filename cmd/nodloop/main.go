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

	"github.com/jeon-jihyeon/nodloop/internal/llm"
)

const usage = `usage: nodloop <command>

Records the output of any producer as a run, takes a person's verdict on it and carries the approved
corrections to the next run in the same place

commands:
  run record --producer <p> --output <file> [--label <key=value>] [--subject <s>] [--applied <id:version>]
                            Record one output of any producer as a run and print its id for feedback.
                            Output that is not JSON is kept as text
  hook prompt | hook stop   The Claude Code conversation hooks the plugin registers. prompt adds the approved items of
                            producer session for the repo and dir of the place as context. The place is cwd while it lies
                            inside CLAUDE_PROJECT_DIR and that directory otherwise. It then names the previous run of the
                            session so the conversation records the user's verdict on it and drafts a correction in the same
                            turn, and counts the candidates waiting for approval: all of them on the first prompt and those
                            made since the previous answer later. stop records the answer as a run and in the background
                            drafts the lesson of the previous run when the conversation recorded a correction no record cites.
                            Both always exit 0 and do nothing when NODLOOP_SESSION is off. manual leaves out the note
  feedback list [--trace <id>] [--verdict <v>] [--reviewer <r>] [--limit <n>]
                            List feedback newest first with the reason code or a dash after the verdict
  feedback add --trace <id> --verdict <v> [--reason-code <c>] [--reason <r>] [--edited <file>] [--reviewer <r>] [--audit]
                            Append one verdict on a run. An edit carries the corrected output in full.
                            An edit or reject may name what the output got wrong: fact, approach, scope, form or other
  feedback outcome --trace <id> --result <r> [--cause <text>] [--note <n>] [--reviewer <r>]
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
                            reaches with the critic, a second claude call or the classifiers set up for the critic point, and
                            propose an add or an update. A duplicate or a conflict names the item
  knowledge for --producer <p> [--label <key=value>]
                            The approved items a run of the producer with these labels applies, and their size
  knowledge waiting --producer <p> [--label <key=value>]
                            The candidates a run of the producer with these labels would receive once approved, with the
                            runs whose verdicts taught them
  knowledge list [--status <s>] [--kind <k>] [--stale]
                            Current version per id with a stale column. --status lists every version of that status
  knowledge show <id>       Every record of one id
  knowledge health          Verdict and outcome counts per knowledge version, retire candidates and review deadlines,
                            idle versions no run applied for 30 days and contested ones corrected again for their reason
                            with the items to compact them with
  knowledge audit           References of current items that no longer resolve
  knowledge reaffirm <id> --approver <name> [--version <n>]
                            Record that a named person rechecked the approved version. Its review deadline starts again
  knowledge narrow <id> --version <n> --key <label key> [--author <a>]
                            Propose the next version that excepts the values of that label its refuted runs carried
  knowledge promote <id> --version <n> [--author <a>]
                            Propose the next version with basis verified and the runs whose outcome confirmed it
  knowledge overlaps <id>   Current items of the same kind that one run may carry together
  knowledge approve <id> --version <n> --approver <name>
                            Refused when one run would carry more than the caps, when it would lift the veto of the
                            approved version or reach runs it never reached, or when it was not built from that version
  knowledge retire <id> --version <n> --approver <name>
  knowledge import --file <jsonl>
                            Append the records of a file not yet recorded
  knowledge compact <id> [--model <m>] [--author <a>]
                            Draft through claude -p a smaller set of items for the runs the item reaches, with nothing said
                            twice and nothing lost, and propose it
  knowledge check <compaction id> [--model <m>]
                            One claude -p call lists for every old item the new items that state it and the facts they lose,
                            and records it as the coverage check
  knowledge compaction <compaction id>
                            Print the new and old items and the newest coverage check
  knowledge approve-compaction <compaction id> --approver <name>
                            Approve the new items and retire the old ones once the coverage check passed
  knowledge export          Write the approved vetoes and approved.md of the record directory again after a failed export
                            and print the line that imports approved.md from a CLAUDE.md
  queue [--limit <n>] [--audit-rate <share>] [--seed <n>]
                            Runs without a verdict in the order to check them, with a random audit share
  report online [--since <RFC3339>]
                            Weekly verdict rates, waits and edit widths, and the runs with and without knowledge compared
  report loop               A totals line of runs, verdicts by a person and by a session, corrections, candidates waiting
                            and approved items. A scope line per plugin version: current items, those whose scope admits
                            one session at most and approved ones never applied. A drafts line per plugin version and
                            drafting path: drafted, approved, dropped, waiting and the median time to a decision. Then per
                            approved item: runs that applied it, how many a person approved of
                            those judged, how many were corrected again for the reason that taught it, the same two counts
                            over verdicts a session inferred, and the time from that correction to the approval
  report effect             Runs that applied items against runs whose turn the holdout of config holdout drew:
                            runs, judged, corrected and corrected again for a reason an item was taught by
  report extract            Per plugin version of the corrected run and per drafting path: extractions by how they
                            ended, refused drafts by what refused them, and the critic questions answered false
  report critic             Per critic: drafts judged and how often a pass or a refusal matched what a person later
                            decided on candidates of the same run, with the runs nobody decided yet
  trace list [--name <n>] [--session <id>] [--subject <s>] [--limit <n>]
                            List traces newest first
  trace show <id>           Print one trace as JSON
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
  mcp                       Serve the MCP tools on stdio. --list prints the tool names
  llm probe [--model <m>]   Send a minimal structured-output request through claude -p and print cost
  config approver [<name>]  Print the name saved in ~/.nodloop/config.json to approve under, or save one
  config holdout [<share>]  Print or save the share of conversation turns whose prompt gets no item so report effect
                            can compare. 0 by default
  classifier add <name> --url <u> [--model <m>] [--key-env <ENV>]
                            Add an endpoint of the Jev wire format, such as laya-serve at
                            http://localhost:8000/v1/systemone. The key is read from the env at call time and never saved
  classifier use <point> --members <a,b> [--mode single|cascade|parallel] [--threshold <t>] [--combine all|any]
                            Set which classifiers answer a decision point and how. claude is the built in member.
                            Points: critic, the second reader of a drafted lesson, where claude is the claude critic, and
                            reaction, which judges whether the user's message corrects or approves the previous answer
                            before the conversation does, where claude defers to the conversation. Every answer is
                            recorded as a classify trace
  classifier reset <point>  The point asks claude alone again
  classifier remove <name>  Remove an endpoint no point uses
  classifier list           Endpoints and the setup of every point
  classifier probe <name>   Ask the endpoint one question and print its answer and time
  version                   Print the build version

Every command that reads records accepts --record-dir, which overrides NODLOOP_RECORD_DIR, then record_dir of
~/.nodloop/config.json, then ~/.nodloop/records
An approved version is stale 90 days after its approval or last reaffirm. Nothing is retired without a named approver
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
	commands := map[string]func(args []string) int{
		"trace": func(args []string) int { return runTrace(args, getenv, time.Now, stdout, stderr) },
		"run":   func(args []string) int { return runRun(args, getenv, time.Now, stdout, stderr) },
		"hook": func(args []string) int {
			return runHook(args, getenv, detached(homeDir(getenv("HOME")).hookLog()), time.Now, stdin, stdout, stderr)
		},
		"feedback": func(args []string) int { return runFeedback(args, getenv, time.Now, stdout, stderr) },
		"queue":    func(args []string) int { return runQueue(args, getenv, time.Now, stdout, stderr) },
		"report":   func(args []string) int { return runReport(args, getenv, time.Now, stdout, stderr) },
		"guard":    func(args []string) int { return runGuard(args, getenv, os.Executable, time.Now, stdin, stdout, stderr) },
		"llm":      func(args []string) int { return runLLM(args, claudeCLI(getenv), time.Now, stdout, stderr) },
		"knowledge": func(args []string) int {
			return runKnowledge(args, getenv, claudeCLI(getenv), time.Now, stdout, stderr)
		},
		"mcp":        func(args []string) int { return runMCP(args, getenv, time.Now, stdin, stdout, stderr) },
		"config":     func(args []string) int { return runConfig(args, getenv, stdout, stderr) },
		"classifier": func(args []string) int { return runClassifier(args, getenv, time.Now, stdout, stderr) },
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

// The model client of every command that calls claude
// The env names the binary and the default model
func claudeCLI(getenv func(string) string) *llm.ClaudeCLI {
	return llm.NewClaudeCLI(getenv(envClaudeBin), getenv(envLLMModel), "", 0)
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

// One line on stderr per failed command
// A usage error adds the usage text
func fail(stderr io.Writer, command string, err error) int {
	fmt.Fprintf(stderr, "nodloop %s: %v\n", command, err)
	if errors.Is(err, errUnknownAction) || errors.Is(err, errRequired) || errors.Is(err, errUnknownTraceName) {
		fmt.Fprintf(stderr, "\n%s", usage)
	}
	return 1
}
