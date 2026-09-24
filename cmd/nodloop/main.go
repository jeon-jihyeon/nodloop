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
                            Without --vetoes, loads .claude/nodloop/vetoes.yaml from the hook cwd and from $HOME
  guard check               Load veto files from the current directory and $HOME and report counts or errors
  guard install             Register this binary as a PreToolUse hook in ~/.claude/settings.json (backs up first)
  guard uninstall           Remove the hook registered by guard install
  setup --demo | --data-dir <dir> [--record-dir <dir>]
                            Point nodloop at a reference data directory, or unpack the demo set under ~/.nodloop/demo.
                            Records go to ~/.nodloop/records unless --record-dir or NODLOOP_RECORD_DIR names another
  llm probe [--model <m>]   Send a minimal structured-output request through claude -p and print cost
  evidence events           List events from the configured source
  evidence event --id <id>  Print the change context and the series of one event
  evidence paragraphs       List runbook paragraph ids
  evidence labels           List ground truth labels. Empty when the data directory has none
  analysis observe --event <id>
                            Print the observations of one event under the policy
  analysis policy           Print the loaded policy
  trace list [--name <n>] [--session <id>] [--subject <s>] [--limit <n>]
                            List traces newest first
  trace show <id>           Print one trace as JSON
  trace pending             List context traces that no review recorded
  feedback list [--trace <id>] [--verdict <v>] [--reviewer <r>] [--limit <n>]
                            List feedback newest first
  feedback add --trace <id> --verdict <v> [--reason <r>] [--edited <file>] [--reviewer <r>]
                            Append one feedback record
  feedback outcome --trace <id> --result <r> [--cause <text>] [--note <n>] [--reviewer <r>]
                            Record what a real check found: confirmed, refuted or inconclusive
  knowledge propose --kind <k> --content <text> [--id <id>] [--basis stated or verified] [--author <a>] [--scope-context <c>] [--scope-metric <m>] [--exception <c>]
                    [--evidence-paragraph <id>] [--evidence-feedback <id>] [--evidence-outcome <id>] [--trace <id>]
                            Add a candidate knowledge record and list its overlaps. The author is "author" unless given
  knowledge list [--status <s>] [--kind <k>]
                            Current version per id
  knowledge show <id>       Every record of one id
  knowledge overlaps <id>   Current items of the same kind with an intersecting scope
  knowledge approve <id> --version <n> --approver <name>
  knowledge retire <id> --version <n> --approver <name>
  knowledge import --file <jsonl>
                            Append records from a file, such as the demo knowledge
  diagnose --event <id> [--examples <n>] [--knowledge none or selected or all] [--model <m>] [--session <s>] [--tag <t>]
                            Batch review of one event through claude -p. JSON on stdout and the trace id on stderr
  mcp                       Serve the MCP tools on stdio. --list prints the tool names without opening any data
  eval seed --session <s> [--model <m>] [--events <ids>] [--parallel <n>]
                            Review every seed event so a reviewer can annotate the results
  eval holdout --session <s> [--model <m>] [--examples <n>] [--conditions <list>] [--events <ids>] [--parallel <n>]
                            Review every holdout event under feedback:off, feedback:on, knowledge:on and knowledge:all
  eval report --session <s> Print the metrics table and write eval-<s>.json to the record directory
  version                   Print the build version

Data commands accept --source, --data-dir and --record-dir. Each overrides the matching NODLOOP_* variable
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
	switch args[0] {
	case "guard":
		return runGuard(args[1:], getenv, stdin, stdout, stderr)
	case "setup":
		return runSetup(args[1:], getenv, stdout, stderr)
	case "llm":
		return runLLM(args[1:], claudeCLI(getenv), time.Now, stdout, stderr)
	case "evidence":
		return runEvidence(args[1:], getenv, time.Now, stdout, stderr)
	case "analysis":
		return runAnalysis(args[1:], getenv, time.Now, stdout, stderr)
	case "trace":
		return runTrace(args[1:], getenv, time.Now, stdout, stderr)
	case "feedback":
		return runFeedback(args[1:], getenv, time.Now, stdout, stderr)
	case "knowledge":
		return runKnowledge(args[1:], getenv, time.Now, stdout, stderr)
	case "diagnose":
		return runDiagnose(args[1:], getenv, claudeCLI(getenv), time.Now, stdout, stderr)
	case "mcp":
		return runMCP(args[1:], getenv, time.Now, stdin, stdout, stderr)
	case "eval":
		return runEval(args[1:], getenv, claudeCLI(getenv), time.Now, stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, buildVersion())
		return 0
	default:
		fmt.Fprintf(stderr, "nodloop: unknown command %q\n\n%s", args[0], usage)
		return 1
	}
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
	if errors.Is(err, errUnknownAction) || errors.Is(err, errRequired) || errors.Is(err, errUnknownTraceName) {
		fmt.Fprintf(stderr, "\n%s", usage)
	}
	return 1
}
