package curate

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

const (
	// The cause of an anchor version whose refs a record changed that names no decision
	// A command records its intervention after the record it wrote so its approval names none
	CauseApproval = "approval"
	// The cause of an anchor version whose files alone changed
	// A person designates or edits a file with no record behind it
	CauseFiles = "files"
)

// One version of the person's judgment on record as its anchor event records it
type AnchorVersion struct {
	Version int `json:"version"`
	// The approved versions whose latest approve or reaffirm names a person and those a reanchor took
	Refs  []knowledge.Ref `json:"refs"`
	Files []AnchorFile    `json:"files"`
	// The trace of the decision of the person that changed it or CauseApproval or CauseFiles
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

// The version an anchor trace holds and version 0 for no trace
func RestoreAnchor(t trace.Trace) (AnchorVersion, error) {
	var a AnchorVersion
	if t.ID == "" {
		return a, nil
	}
	if err := json.Unmarshal(t.Input, &a); err != nil {
		return AnchorVersion{}, fmt.Errorf("%w: trace %s: %w", ErrAnchorInvalid, t.ID, err)
	}
	return a, nil
}

// The refs that entered or left since the version
func (a AnchorVersion) changed(refs []knowledge.Ref) []knowledge.Ref {
	var out []knowledge.Ref
	for _, r := range refs {
		if !slices.Contains(a.Refs, r) {
			out = append(out, r)
		}
	}
	for _, r := range a.Refs {
		if !slices.Contains(refs, r) {
			out = append(out, r)
		}
	}
	return out
}

// Whether the files name the same paths with the same HMAC as the version
// A memory type the host reports another way changes no version
func (a AnchorVersion) sameFiles(files []AnchorFile) bool {
	sums := func(fs []AnchorFile) map[string]string {
		out := map[string]string{}
		for _, f := range fs {
			out[f.Path] = f.HMAC
		}
		return out
	}
	return maps.Equal(sums(a.Files), sums(files))
}

// A rules file a session loaded as its instructions trace recorded it
type Loaded struct {
	Session string
	File    AnchorFile
}

// The person's judgment the records and the files hold now
// curate reads it and never writes into it so only a person's decision changes it
type Anchor struct {
	// Every record of the ledger newest first
	all knowledge.Set
	// The designated files that could be read in the order the person saved them
	designated []AnchorFile
	// The rules files sessions loaded since the cutoff of the window newest first
	loaded []Loaded
}

func NewAnchor(all knowledge.Set, designated []AnchorFile, loaded []Loaded) Anchor {
	return Anchor{all: all, designated: designated, loaded: loaded}
}

// The versions a person approved in id order
func (a Anchor) Items() knowledge.Set {
	return a.all.PersonApproved()
}

// The files every place reads
func (a Anchor) Designated() []AnchorFile {
	return a.designated
}

// The person's own decisions as they stood before start that the few-shot sets of a judge started then are drawn from
// in the order of the records
// 1. an approval of a drafted lesson as drafted and a retire a person gave for the direction of the lesson
// 2. only the newest decision of each version before start counts so a lesson approved and then retired never shows as approved
// and one retired after start still shows as the set drew it
// 3. a version nodloop decided and an approval of a text a person rewrote are no example of the person's judgment
func (a Anchor) Shots(start time.Time) Shots {
	seen := map[knowledge.Ref]bool{}
	var out Shots
	for _, k := range a.all {
		ref := knowledge.Ref{ID: k.ID, Version: k.Version}
		kept := k.Status == knowledge.StatusApproved && k.Drafted
		refused := k.Status == knowledge.StatusRetired && k.Reason == knowledge.ReasonPerson
		if seen[ref] || !k.Decided() || !k.Time.Before(start) {
			continue
		}
		seen[ref] = true
		if k.Approver != knowledge.ApproverNodloop && (kept || refused) {
			out = append(out, Shot{Ref: ref, At: k.Time, Content: k.Content, Kept: kept})
		}
	}
	return out
}

// The anchor as the version after newest and false when nothing it holds differs
// 1. it differs when a ref differs or a path or the HMAC of a file
// 2. a change of the refs names the decision of the newest record of a ref that entered or left
// CauseApproval stands in when that record names none
// 3. a change of the files alone names CauseFiles
func (a Anchor) Next(newest AnchorVersion) (AnchorVersion, bool) {
	files := slices.Concat(a.designated, a.loadedBy(func(string) bool { return true }))
	next := AnchorVersion{Version: newest.Version + 1, Refs: a.Items().Refs(), Files: files}
	switch changed := newest.changed(next.Refs); {
	case len(changed) > 0:
		next.Cause = a.cause(changed)
	case !newest.sameFiles(next.Files):
		next.Cause = CauseFiles
	default:
		return newest, false
	}
	return next, true
}

// The decision of the newest record of one of the refs and CauseApproval when it names none
func (a Anchor) cause(refs []knowledge.Ref) string {
	i := slices.IndexFunc(a.all, func(k knowledge.Knowledge) bool {
		return slices.Contains(refs, knowledge.Ref{ID: k.ID, Version: k.Version})
	})
	if i < 0 || a.all[i].Decision == "" {
		return CauseApproval
	}
	return a.all[i].Decision
}

// The newest rules file of each path the sessions loaded that no designated file names in path order
func (a Anchor) loadedBy(sessions func(string) bool) []AnchorFile {
	seen := map[string]bool{}
	for _, f := range a.designated {
		seen[f.Path] = true
	}
	out := []AnchorFile{}
	for _, l := range a.loaded {
		if !seen[l.File.Path] && sessions(l.Session) {
			seen[l.File.Path] = true
			out = append(out, l.File)
		}
	}
	slices.SortFunc(out, func(x, y AnchorFile) int { return strings.Compare(x.Path, y.Path) })
	return out
}

// The anchor one place reads beside the designated files
type AnchorPlace struct {
	// The producer and the labels of its runs without the host
	Place knowledge.RunScope
	// The versions a person approved whose scope admits a run of the place
	Items knowledge.Set
	// The rules files its sessions loaded that no designated file names in path order
	Loaded []AnchorFile
}

// The anchor of every place the runs carry in the order of their scope text
// 1. a place is the producer of a run with its labels without the host so one place spans every host
// 2. an item reaches it when its scope admits one of its runs with the host
// 3. a run without a session loads no file
func (a Anchor) Places(runs trace.Traces) []AnchorPlace {
	type runsOf struct {
		place    knowledge.RunScope
		labels   []trace.Labels
		sessions map[string]bool
	}
	byPlace := map[string]*runsOf{}
	for _, r := range runs {
		if r.Name != trace.NameRun {
			continue
		}
		place := knowledge.RunScope{Producer: r.Producer, Labels: r.Labels.Place()}
		of, ok := byPlace[place.String()]
		if !ok {
			of = &runsOf{place: place, sessions: map[string]bool{}}
			byPlace[place.String()] = of
		}
		of.labels = append(of.labels, r.Labels)
		if r.SessionID != "" {
			of.sessions[r.SessionID] = true
		}
	}
	out := make([]AnchorPlace, 0, len(byPlace))
	for _, key := range slices.Sorted(maps.Keys(byPlace)) {
		of := byPlace[key]
		items := knowledge.Set{}
		for _, k := range a.Items() {
			if k.Run != nil && slices.ContainsFunc(of.labels, func(l trace.Labels) bool { return k.Run.Admits(of.place.Producer, l) }) {
				items = append(items, k)
			}
		}
		out = append(out, AnchorPlace{Place: of.place, Items: items, Loaded: a.loadedBy(func(s string) bool { return of.sessions[s] })})
	}
	return out
}
