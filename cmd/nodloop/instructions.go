package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
func (c hookCommand) instructions(ctx context.Context, in hookInput) error {
	if c.unconfirmed.holds(in.FilePath, in.MemoryType) {
		return nil
	}
	content, err := os.ReadFile(in.FilePath)
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
// The config is CLAUDE_CONFIG_DIR and else .claude under home
func newUnconfirmed(recordDir string, home homeDir, configDir string) unconfirmed {
	u := unconfirmed{rules: filePath(knowledgefile.RulesPath(recordDir))}
	switch {
	case configDir != "":
		u.projects = filePath(filepath.Join(configDir, "projects"))
	case home != "":
		u.projects = filePath(filepath.Join(string(home), ".claude", "projects"))
	}
	return u
}

// Whether the file holds lines no person confirmed
// 1. approved.md of the record directory
// 2. a file below <projects>/<project>/memory/ or one the host reports as auto memory
func (u unconfirmed) holds(path, memoryType string) bool {
	file := filePath(path).resolved()
	if memoryType == autoMemoryType || file == u.rules.resolved() {
		return true
	}
	if u.projects == "" {
		return false
	}
	rel, err := filepath.Rel(u.projects.resolved(), file)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	return err == nil && len(parts) > 2 && parts[0] != ".." && parts[1] == "memory"
}

// A path of a file the host reported or nodloop keeps
type filePath string

// The path with its links resolved and the path as given while it names nothing
// The host may report /private/tmp for a record directory named under /tmp
func (p filePath) resolved() string {
	if r, err := filepath.EvalSymlinks(string(p)); err == nil {
		return r
	}
	return filepath.Clean(string(p))
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
