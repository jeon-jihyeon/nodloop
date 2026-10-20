package curate

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The cause of an anchor version a person approved or reaffirmed an item for through a command
const CauseApproval = "approval"

// One version of the person's judgment on record as its anchor event records it
type AnchorVersion struct {
	Version int `json:"version"`
	// The approved versions whose latest approve or reaffirm names a person and those a reanchor took
	Refs  []knowledge.Ref `json:"refs"`
	Files []AnchorFile    `json:"files"`
	// The trace of the decision of the person that changed it or CauseApproval
	Cause string `json:"cause"`
}

// A file of the anchor kept as its path and the HMAC of its content and never as its text
type AnchorFile struct {
	Path string `json:"path"`
	// HMAC-SHA256 of the content under the key of the record directory
	HMAC string `json:"hmac"`
	// The memory type the host reported for a rules file a session loaded
	// Empty for a file the person designated
	MemoryType string `json:"memory_type,omitempty"`
}

// The anchor trace of the version
// 1. a version counts from 1 and names its cause
// 2. every ref names a version and every file its path and the 64 hex of its HMAC
func (a AnchorVersion) Trace(now time.Time) (trace.Trace, error) {
	if a.Version < 1 || a.Cause == "" {
		return trace.Trace{}, fmt.Errorf("%w: version %d with cause %q", ErrAnchorInvalid, a.Version, a.Cause)
	}
	for _, r := range a.Refs {
		if r.ID == "" || r.Version < 1 {
			return trace.Trace{}, fmt.Errorf("%w: ref %s v%d", ErrAnchorInvalid, r.ID, r.Version)
		}
	}
	for _, f := range a.Files {
		if b, err := hex.DecodeString(f.HMAC); f.Path == "" || err != nil || len(b) != 32 {
			return trace.Trace{}, fmt.Errorf("%w: a file names its path and the HMAC of its content", ErrAnchorInvalid)
		}
	}
	if a.Refs == nil {
		a.Refs = []knowledge.Ref{}
	}
	if a.Files == nil {
		a.Files = []AnchorFile{}
	}
	// Refs and files of strings and numbers always encode
	input, _ := json.Marshal(a)
	return trace.Trace{ID: trace.NewID(now), Name: trace.NameAnchor, Subject: fmt.Sprintf("v%d", a.Version), Time: now, Input: input}, nil
}
