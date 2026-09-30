// Package guard is the PreToolUse interlock that blocks tool calls matching a veto
package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
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
// 4. a veto file that was read but is not valid YAML: ExitBlock for every call but one that reads or edits a veto file or the file named on the command line
// 5. any other load failure without a match: ExitFail after a warning on stderr
// 6. the vetoes that did load block first in every case
// Rule 4 keeps a file broken by one write from turning every veto in it off
// Exit 1 lets the call run so failing open there would do exactly that
// A file no Edit or Write can repair such as one without permission or a directory gets rule 5 so it never locks the session
func Run(stdin io.Reader, stderr io.Writer, load Loader, named string) Exit {
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
	if errors.Is(err, veto.ErrYAMLInvalid) && !in.repairs(named) {
		fmt.Fprintf(stderr, "nodloop guard: %s call blocked because a veto file is not valid YAML\n"+
			"Fix the file named above. Reading and editing a veto file still pass\n", in.ToolName)
		return ExitBlock
	}
	if err != nil {
		return ExitFail
	}
	return ExitPass
}

// Whether the call reads or edits a veto file by its file_path so a broken file can be repaired
// A file named on the command line counts under any name because the lockout covers it too
func (in input) repairs(named string) bool {
	path, _ := in.ToolInput["file_path"].(string)
	return vetofile.Named(path) || (named != "" && filepath.Clean(path) == named)
}
