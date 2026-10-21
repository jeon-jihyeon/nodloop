package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jeon-jihyeon/nodloop/internal/atomicfile"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/textkey"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The directory Claude Code keeps its config in when it is not .claude under home
const envClaudeConfigDir = "CLAUDE_CONFIG_DIR"

// A rules file a session loaded as its instructions trace records it
// The content is kept as its HMAC so no trace holds the text of a rules file
type instructionsInput struct {
	Path string `json:"path"`
	// HMAC-SHA256 of the content under the key of the record directory
	HMAC string `json:"hmac"`
	// What the InstructionsLoaded hook reported of the file
	MemoryType string `json:"memory_type,omitempty"`
	LoadReason string `json:"load_reason,omitempty"`
}

// The instructions trace of the file in the session
// A session and a path with a 64 hex HMAC are required
func (in instructionsInput) trace(session string, now time.Time) (trace.Trace, error) {
	if b, err := hex.DecodeString(in.HMAC); session == "" || in.Path == "" || err != nil || len(b) != 32 {
		return trace.Trace{}, fmt.Errorf("%w: a rules file of a session names its path and the HMAC of its content", errSessionTrace)
	}
	// Four strings always encode
	input, _ := json.Marshal(in)
	return trace.Trace{ID: trace.NewID(now), Name: trace.NameInstructions, SessionID: session, Time: now, Input: input}, nil
}

// A rules file the session loaded as an instructions trace so curate reads the person's rules files as part of the anchor
// 1. a file whose lines no person confirmed is left out
// 2. a file the host never loads as the person's instructions is refused and records nothing
// Any process can pipe a path into the hook so a model would otherwise set a file it wrote as the person's direction
// 3. the content is kept only as its HMAC and nothing reaches the context
// 4. a path that names no regular file or one past inputLimit is refused so the hook never waits on a fifo
func (c hookCommand) instructions(ctx context.Context, in hookInput) error {
	switch {
	case c.rules.unconfirmed(in.FilePath, in.MemoryType):
		return nil
	case !c.rules.loads(in.FilePath, in.MemoryType, in.LoadReason, in.ParentFilePath):
		return fmt.Errorf("%w: %s of memory type %q", errInstructionsForeign, in.FilePath, in.MemoryType)
	}
	content, err := atomicfile.Read(in.FilePath, inputLimit)
	if err != nil {
		return err
	}
	store, err := c.app.traces()
	if err != nil {
		return err
	}
	key, err := textkey.Load(c.app.cfg.recordDir)
	if err != nil {
		return err
	}
	loaded := instructionsInput{Path: in.FilePath, HMAC: key.Sum(string(content)), MemoryType: in.MemoryType, LoadReason: in.LoadReason}
	tr, err := loaded.trace(in.SessionID, c.app.now())
	if err != nil {
		return err
	}
	return store.Append(ctx, tr.Record())
}

const (
	// The memory type Claude Code reports for an auto memory file
	autoMemoryType = "memory"
	// The names of the instructions file the host loads from a folder
	claudeFile      = "CLAUDE.md"
	claudeLocalFile = "CLAUDE.local.md"
	// The load reason Claude Code reports for a file an @path of another instructions file imports
	includeReason = "include"
)

// The memory types the Claude Code hooks reference names for the instructions of the user and of a project
// Managed instructions an organization sets are no direction of the person
var personMemoryTypes = []string{"User", "Project", "Local"}

// The rules files the host loads as the person's instructions for a session
type rulesFiles struct {
	// approved.md of the record directory
	// It holds the versions nodloop approved too and a CLAUDE.md imports it
	approved filePath
	// The Claude Code config with the instructions of the user and the projects directory of auto memory
	// Empty when no home is known
	config filePath
	// The folder the session started in
	project filePath
	// The base of a home relative import
	// Empty when no home is known
	home homeDir
}

// The rules files of a session that started in the project folder under the Claude Code config
func newRulesFiles(recordDir string, home homeDir, configDir, project string) rulesFiles {
	return rulesFiles{
		approved: filePath(knowledgefile.RulesPath(recordDir)), config: claudeConfig(home, configDir), project: filePath(project), home: home,
	}
}

// The Claude Code config folder
// 1. CLAUDE_CONFIG_DIR and else .claude under home
// 2. empty when no home is known
func claudeConfig(home homeDir, configDir string) filePath {
	switch {
	case configDir != "":
		return filePath(configDir)
	case home != "":
		return filePath(filepath.Join(string(home), ".claude"))
	}
	return ""
}

// The projects directory of the Claude Code config that holds a folder per project with its transcripts and auto memory
// Empty when no home is known
func claudeProjects(home homeDir, configDir string) filePath {
	config := claudeConfig(home, configDir)
	if config == "" {
		return ""
	}
	return filePath(filepath.Join(string(config), "projects"))
}

// Whether the file holds lines no person confirmed
// 1. approved.md of the record directory
// 2. an auto memory file under a project directory or named as such by the host
func (r rulesFiles) unconfirmed(path, memoryType string) bool {
	file := filePath(path)
	switch {
	case memoryType == autoMemoryType || file.resolved() == r.approved.resolved():
		return true
	case r.config == "":
		return false
	}
	parts := file.below(filePath(filepath.Join(string(r.config), "projects")))
	return len(parts) > 2 && parts[1] == "memory"
}

// Whether the host loads the file as the instructions of the person
// 1. the host reports it under a memory type of the user or a project
// 2. it is placed as an instructions file or the host reports it as an import of a parent placed so whose text imports it
// An import of an import is left out since the host names the parent alone and no file above it
func (r rulesFiles) loads(path, memoryType, loadReason, parent string) bool {
	if !slices.Contains(personMemoryTypes, memoryType) {
		return false
	}
	return r.placed(path) || loadReason == includeReason && parent != "" && r.placed(parent) && r.imports(parent, path)
}

// Whether the file lies where the host loads instructions from
// 1. a CLAUDE.md or CLAUDE.local.md lies in the Claude Code config or in a folder of the project tree
// 2. any other file lies below the rules directory of the Claude Code config or of a project config
// The folder of the file is compared with its links resolved and the name is the one the host reported
func (r rulesFiles) placed(path string) bool {
	dir := filePath(filepath.Dir(path)).resolved()
	if name := filepath.Base(path); name == claudeFile || name == claudeLocalFile {
		return r.config != "" && dir == r.config.resolved() || r.inProject(filePath(dir))
	}
	for d := dir; ; d = filepath.Dir(d) {
		switch parent := filepath.Dir(d); {
		case r.config != "" && d == filepath.Join(r.config.resolved(), "rules"):
			return true
		case filepath.Base(d) == "rules" && filepath.Base(parent) == ".claude" && r.inProject(filePath(filepath.Dir(parent))):
			return true
		case parent == d:
			return false
		}
	}
}

// Whether an @path of the text of the parent names the file as the host reads imports
// 1. a relative import starts from the parent folder and a home relative import from home
// 2. a backslash before a space keeps the space in the path
// 3. a code span and a fenced code block import nothing
// 4. a parent that cannot be read imports nothing
func (r rulesFiles) imports(parent, path string) bool {
	content, err := atomicfile.Read(parent, inputLimit)
	if err != nil {
		return false
	}
	want := filePath(path).resolved()
	fenced := false
	for line := range strings.Lines(string(content)) {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		// The parts at odd places lie inside code spans
		for i, part := range strings.Split(line, "`") {
			if i%2 == 0 && slices.ContainsFunc(importPaths(part), func(p string) bool { return r.importOf(parent, p).resolved() == want }) {
				return true
			}
		}
	}
	return false
}

// The paths the @ marks of the text name
// An @ inside a word such as an address names none
func importPaths(text string) []string {
	out := []string{}
	for i := strings.IndexByte(text, '@'); i >= 0; i = strings.IndexByte(text, '@') {
		before := text[:i]
		text = text[i+1:]
		if before != "" && !unicode.IsSpace(rune(before[len(before)-1])) {
			continue
		}
		end := 0
		for end < len(text) && !unicode.IsSpace(rune(text[end])) {
			if text[end] == '\\' && end+1 < len(text) && text[end+1] == ' ' {
				end++
			}
			end++
		}
		if end > 0 {
			out = append(out, strings.ReplaceAll(text[:end], "\\ ", " "))
		}
	}
	return out
}

// The file an import of the parent names
func (r rulesFiles) importOf(parent, imported string) filePath {
	if rest, ok := strings.CutPrefix(imported, "~/"); ok && r.home != "" {
		return filePath(filepath.Join(string(r.home), rest))
	}
	if filepath.IsAbs(imported) {
		return filePath(imported)
	}
	return filePath(filepath.Join(filepath.Dir(parent), imported))
}

// Whether the folder and the session project share one ancestor chain
// The host loads a CLAUDE.md from each folder above the start and from a folder below once the session reads there
func (r rulesFiles) inProject(dir filePath) bool {
	return r.project != "" && (dir.below(r.project) != nil || r.project.below(dir) != nil)
}

// A path of a file the host reported or nodloop keeps
type filePath string

// The path with its links resolved and the path as given while it names nothing
// 1. the host may report a different spelling of a linked temporary directory
// 2. a file that is not there yet takes the links of its folder so it compares with the folder resolved
func (p filePath) resolved() string {
	if r, err := filepath.EvalSymlinks(string(p)); err == nil {
		return r
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(string(p))); err == nil {
		return filepath.Join(dir, filepath.Base(string(p)))
	}
	return filepath.Clean(string(p))
}

// The parts of the path below the root with the links of both resolved and none outside it or without a root
func (p filePath) below(root filePath) []string {
	if root == "" {
		return nil
	}
	rel, err := filepath.Rel(root.resolved(), p.resolved())
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if err != nil || parts[0] == ".." {
		return nil
	}
	return parts
}

// Instructions traces newest first
type instructionsTraces trace.Traces

// The rules files the traces recorded as the anchor reads them
func (ts instructionsTraces) loaded() ([]curate.Loaded, error) {
	out := make([]curate.Loaded, 0, len(ts))
	for _, t := range ts {
		var in instructionsInput
		if err := json.Unmarshal(t.Input, &in); err != nil {
			return nil, fmt.Errorf("%w: instructions %s: %w", errSessionTrace, t.ID, err)
		}
		out = append(out, curate.Loaded{Session: t.SessionID, File: curate.AnchorFile{Path: in.Path, HMAC: in.HMAC, MemoryType: in.MemoryType}})
	}
	return out, nil
}
