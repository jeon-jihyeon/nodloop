package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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
// The session, the path and the 64 hex of the HMAC are named
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
// 2. the content is kept only as its HMAC and nothing reaches the context
// 3. a path that names no regular file or one past inputLimit is refused so the hook never waits on a fifo
func (c hookCommand) instructions(ctx context.Context, in hookInput) error {
	if c.unconfirmed.holds(in.FilePath, in.MemoryType) {
		return nil
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

// The memory type Claude Code reports for an auto memory file
const autoMemoryType = "memory"

// The rules files the instructions hook leaves out of the anchor since no person confirmed what they hold
type unconfirmed struct {
	// approved.md of the record directory
	// It holds the versions nodloop approved too and a CLAUDE.md imports it
	rules filePath
	// The projects directory of the Claude Code config whose memory folders hold the auto memory the host saves on its own
	// Empty when no home is known
	projects filePath
}

// The files left out for the record directory under the Claude Code config
func newUnconfirmed(recordDir string, home homeDir, configDir string) unconfirmed {
	return unconfirmed{rules: filePath(knowledgefile.RulesPath(recordDir)), projects: claudeProjects(home, configDir)}
}

// The projects directory of the Claude Code config that holds a folder per project with its transcripts and auto memory
// 1. the config is CLAUDE_CONFIG_DIR and else .claude under home
// 2. empty when no home is known
func claudeProjects(home homeDir, configDir string) filePath {
	switch {
	case configDir != "":
		return filePath(filepath.Join(configDir, "projects"))
	case home != "":
		return filePath(filepath.Join(string(home), ".claude", "projects"))
	}
	return ""
}

// Whether the file holds lines no person confirmed
// 1. approved.md of the record directory
// 2. a file below <projects>/<project>/memory/ or one the host reports as auto memory
func (u unconfirmed) holds(path, memoryType string) bool {
	file := filePath(path)
	if memoryType == autoMemoryType || file.resolved() == u.rules.resolved() {
		return true
	}
	parts := file.below(u.projects)
	return len(parts) > 2 && parts[1] == "memory"
}

// A path of a file the host reported or nodloop keeps
type filePath string

// The path with its links resolved and the path as given while it names nothing
// 1. the host may report /private/tmp for a record directory named under /tmp
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
