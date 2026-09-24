// Package guard is the PreToolUse interlock that blocks tool calls matching a veto
package guard

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

// Fields of the PreToolUse hook input used for evaluation
// Other fields vary across Claude Code versions and are ignored
type input struct {
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
	Cwd       string         `json:"cwd"`
}

// Returns the vetoes to apply for the cwd of the hook input
// A non nil error may come with the vetoes that did load and those still apply
type Loader func(cwd string) (veto.Vetoes, error)

// Exit code of the hook process as Claude Code reads it
type Exit int

const (
	ExitPass  Exit = 0 // no veto matched and the normal permission flow decides
	ExitFail  Exit = 1 // hook input or veto load failure without a match
	ExitBlock Exit = 2 // a veto matched and its reason went to stderr
)

// Match hook input against vetoes and decide the exit code
// 1. no matching veto: ExitPass
// 2. matching veto: ExitBlock with the id and reason on stderr
// 3. input parse failure: ExitFail
// 4. veto load failure without a match: ExitFail after a warning on stderr
// 5. a load failure fails open for the broken file only and the vetoes that did load still block
func Run(stdin io.Reader, stderr io.Writer, load Loader) Exit {
	var in input
	err := json.NewDecoder(stdin).Decode(&in)
	if err == nil && in.ToolName == "" {
		err = ErrToolNameMissing
	}
	if err != nil {
		fmt.Fprintf(stderr, "nodloop guard: failed to parse hook input: %v\n", err)
		return ExitFail
	}
	vetoes, err := load(in.Cwd)
	if err != nil {
		fmt.Fprintf(stderr, "nodloop guard: failed to load vetoes, skipped: %v\n", err)
	}
	if v := vetoes.Match(in.ToolName, in.ToolInput); v != nil {
		fmt.Fprintf(stderr, "nodloop guard: %s call blocked by veto %s\n%s\n", in.ToolName, v.ID(), v.Reason())
		return ExitBlock
	}
	if err != nil {
		return ExitFail
	}
	return ExitPass
}
