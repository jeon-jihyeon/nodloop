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

commands:
  guard [--vetoes <path>]   PreToolUse hook. Reads hook input from stdin and blocks calls that match a veto.
                            Without --vetoes, loads every .claude/nodloop/vetoes.yaml from the hook cwd up to the git root
                            and from $HOME and then every approved veto file under $HOME/.claude/nodloop
  guard check               Load the veto files guard reads for the current directory, report counts or errors
                            and whether the hook is installed
  guard install             Register this binary as a PreToolUse hook in ~/.claude/settings.json (backs up first)
                            Registers ~/.nodloop/bin/nodloop when it links to this binary and replaces a stale hook
  guard uninstall           Remove the hook registered by guard install
  setup --data-dir <dir> [--record-dir <dir>]
                            Point nodloop at a reference data directory such as examples/demo of the repository.
                            Records go to ~/.nodloop/records unless --record-dir or NODLOOP_RECORD_DIR names another
                            A rerun without --record-dir keeps the record dir saved before and prints the records in use
  llm probe [--model <m>]   Send a minimal structured-output request through claude -p and print cost
  evidence events           List events from the configured source
  evidence event --id <id>  Print the change context and the series of one event
  evidence procedures       List procedures with their scope and paragraph count
  evidence paragraphs       List procedure paragraph ids
  evidence labels           List ground truth labels. Empty when the data directory has none
  analysis observe --event <id>
                            Print the observations of one event under the policy
  analysis policy           Print the policy.yaml of the data directory
  trace list [--name <n>] [--session <id>] [--subject <s>] [--limit <n>]
                            List traces newest first
  trace show <id>           Print one trace as JSON
  trace pending             List context traces that no review recorded
  queue [--limit <n>] [--audit-rate <share>] [--seed <n>]
                            Conversation reviews without a verdict in the order to check them, with a random audit share
  report online [--since <RFC3339>]
                            Weekly verdict rates and waits, first status against the settled status, knowledge cohorts
  feedback list [--trace <id>] [--verdict <v>] [--reviewer <r>] [--limit <n>]
                            List feedback newest first
  feedback add --trace <id> --verdict <v> [--reason <r>] [--edited <file>] [--reviewer <r>] [--audit]
                            Append one feedback record
  feedback outcome --trace <id> --result <r> [--cause <text>] [--note <n>] [--reviewer <r>]
                            Record what a real check found: confirmed, refuted or inconclusive
  knowledge propose --kind <k> --content <text> [--id <id>] [--basis stated or verified] [--author <a>] [--scope-context <c>] [--scope-metric <m>] [--exception <c>]
                    [--evidence-paragraph <id>] [--evidence-feedback <id>] [--evidence-outcome <id>] [--trace <id>]
                    [--veto-tool <t> --veto-field <f> --veto-match <re> [--veto-unless <re>] --veto-example <json>]
                            Add a candidate knowledge record and list its overlaps. The author is "author" unless given
                            A judgment with a veto becomes a guard veto once approved
  knowledge propose --from <trace id> --kind <k> [--content <text>] [--model <m>] [the other propose flags]
                            Fill scope, evidence and basis from a review corrected by edit or reject. Without --content
                            claude -p drafts one sentence from the correction and the candidate is marked drafted
  knowledge list [--status <s>] [--kind <k>] [--stale]
                            Current version per id with a stale column. --status lists every version of that status
  knowledge show <id>       Every record of one id
  knowledge health          Verdict and outcome counts per knowledge version, retire candidates and review deadlines
  knowledge audit           References of current items that no longer resolve
  knowledge reaffirm <id> --approver <name> [--version <n>]
                            Record that a named person rechecked the approved version. Its review deadline starts again
  knowledge narrow <id> --version <n> [--author <a>]
                            Propose the next version without the change contexts where its reviews were refuted
                            Fails naming the retire command when every change context of the version was refuted
  knowledge overlaps <id>   Current items of the same kind with an intersecting scope
  knowledge approve <id> --version <n> --approver <name>
                            Refused when its folder may outgrow the review. Retire or replace an item, scope it to other
                            change contexts or compact the folder. Refused when it would lift the veto of the approved
                            version or was not built from that version. Says when the folder holds more
                            than five items and whether a compaction is due or blocked for lack of an expected status
  knowledge retire <id> --version <n> --approver <name>
  knowledge import --file <jsonl>
                            Append the records of a file not yet recorded, such as the knowledge.jsonl of a data set
                            An invalid record or one older than the recorded history of its version lands nothing
  knowledge compact <id> [--model <m>] [--author <a>]
                            Draft through claude -p a smaller set of items that replaces the folder of an item
                            and propose it. Items that cite only procedure paragraphs are left out
  knowledge compaction <compaction id>
                            Print the new and old items and the replay result
  knowledge replay <compaction id> [--model <m>] [--events <ids>] [--parallel <n>]
                            Review every event the old items came from again with the new items through claude -p
  knowledge approve-compaction <compaction id> --approver <name>
                            Approve the new items and retire the old ones once the replay passed
  knowledge export          Write the approved vetoes again after a failed export
  diagnose --event <id> [--examples <n>] [--knowledge none or selected or all] [--model <m>] [--session <s>] [--tag <t>]
                            Batch review of one event through claude -p. JSON on stdout and the trace id on stderr
  mcp                       Serve the MCP tools on stdio. --list prints the tool names without opening any data
  eval seed --session <s> [--model <m>] [--events <ids>] [--parallel <n>] [--repeat <n>]
                            Review every seed event so a reviewer can annotate the results
  eval holdout --session <s> [--model <m>] [--examples <n>] [--conditions <list>] [--events <ids>] [--parallel <n>] [--repeat <n>]
                            Review every holdout event under feedback:off, feedback:on, knowledge:on and knowledge:all
  eval report --session <s> [--triage]
                            Print the metrics table with condition pairs and write eval-<s>.json to the record directory
                            --triage adds how many wrong statuses the top 5 of the queue order hold
  version                   Print the build version

Data commands accept --source, --data-dir and --record-dir. Each overrides the matching NODLOOP_* variable
analysis, diagnose, eval and mcp read the analyzers from policy.yaml in the data directory and fail without it
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
		"evidence": func(args []string) int { return runEvidence(args, getenv, time.Now, stdout, stderr) },
		"analysis": func(args []string) int { return runAnalysis(args, getenv, time.Now, stdout, stderr) },
		"trace":    func(args []string) int { return runTrace(args, getenv, time.Now, stdout, stderr) },
		"feedback": func(args []string) int { return runFeedback(args, getenv, time.Now, stdout, stderr) },
		"queue":    func(args []string) int { return runQueue(args, getenv, time.Now, stdout, stderr) },
		"report":   func(args []string) int { return runReport(args, getenv, time.Now, stdout, stderr) },
		"guard":    func(args []string) int { return runGuard(args, getenv, os.Executable, stdin, stdout, stderr) },
		"setup":    func(args []string) int { return runSetup(args, getenv, stdout, stderr) },
		"llm":      func(args []string) int { return runLLM(args, claudeCLI(getenv), time.Now, stdout, stderr) },
		"knowledge": func(args []string) int {
			return runKnowledge(args, getenv, claudeCLI(getenv), time.Now, stdout, stderr)
		},
		"diagnose": func(args []string) int { return runDiagnose(args, getenv, claudeCLI(getenv), time.Now, stdout, stderr) },
		"mcp":      func(args []string) int { return runMCP(args, getenv, time.Now, stdin, stdout, stderr) },
		"eval":     func(args []string) int { return runEval(args, getenv, claudeCLI(getenv), time.Now, stdout, stderr) },
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

// Default count of past edit and reject feedback a review takes as examples
const defaultExamples = 3

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
	if errors.Is(err, errUnknownAction) || errors.Is(err, errRequired) || errors.Is(err, errUnknownTraceName) ||
		errors.Is(err, errSessionPath) {
		fmt.Fprintf(stderr, "\n%s", usage)
	}
	return 1
}
