// Package schema reads the published record schemas, turns a stored record into the record every transport sends and pages the records of a kind
package schema

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"

	published "github.com/jeon-jihyeon/nodloop/schema"
)

const (
	// The base of the $id of every published file: the module path of the repository
	Base = "https://github.com/jeon-jihyeon/nodloop/schema/"
	// The JSON Schema dialect of every file
	Dialect = "https://json-schema.org/draft/2020-12/schema"
	// The published index of the kinds beside the files
	IndexFile = "kinds.json"
)

// The file of a kind at a version such as run.v1.json
func FileName(kind string, version int) string {
	return kind + ".v" + strconv.Itoa(version) + ".json"
}

// The append only file a kind is stored in
// The rule of its record_id follows from it
type Store string

const (
	StoreTraces    Store = "traces.jsonl"    // every trace name with the id of the trace as its record_id
	StoreVerdicts  Store = "feedback.jsonl"  // verdicts keyed by run and time
	StoreOutcomes  Store = "outcomes.jsonl"  // outcomes keyed by run and time
	StoreKnowledge Store = "knowledge.jsonl" // knowledge records keyed by id, version, status and time
	StoreGuard     Store = "guard.jsonl"     // the guard log of the machine keyed by session, time, tool and veto
)

// One kind of the published index
type Kind struct {
	Name string `json:"kind"`
	// The newest version
	Version int `json:"version"`
	// Every version a file is kept for since a breaking change adds a file beside the old one
	Versions []int `json:"versions"`
	Store    Store `json:"store"`
	// The $id of the file of the newest version
	Schema string `json:"schema"`
	// What one record of the kind is
	Description string `json:"description"`
	// Dotted paths of the fields that join a record to its cause and its effect
	// A path goes through arrays as if each element stood alone
	Links []string `json:"links"`
	// Dotted paths of the free text a transport leaves out unless the content opt in
	Text  []string `json:"text"`
	files Files
}

// A set of published files with the schemas it resolved so a page of records resolves each file once
type Files struct {
	fs       fs.FS
	resolved *sync.Map
}

func NewFiles(fsys fs.FS) Files {
	return Files{fs: fsys, resolved: &sync.Map{}}
}

// The files embedded in the binary
var Published = NewFiles(published.Files)

// The published index of every kind
type Index struct {
	Kinds []Kind `json:"kinds"`
}

// The index of the files with every kind bound to them
func (f Files) Index() (Index, error) {
	b, err := fs.ReadFile(f.fs, IndexFile)
	if err != nil {
		return Index{}, fmt.Errorf("%w: %w", ErrIndexInvalid, err)
	}
	var ix Index
	if err := json.Unmarshal(b, &ix); err != nil {
		return Index{}, fmt.Errorf("%w: %s: %w", ErrIndexInvalid, IndexFile, err)
	}
	for i := range ix.Kinds {
		ix.Kinds[i].files = f
	}
	return ix, nil
}

// The kind of the name and ErrKindUnknown naming every kind for another name
func (ix Index) Kind(name string) (Kind, error) {
	i := slices.IndexFunc(ix.Kinds, func(k Kind) bool { return k.Name == name })
	if i < 0 {
		return Kind{}, fmt.Errorf("%w: %q. Use one of %v", ErrKindUnknown, name, ix.names())
	}
	return ix.Kinds[i], nil
}

func (ix Index) names() []string {
	out := make([]string, 0, len(ix.Kinds))
	for _, k := range ix.Kinds {
		out = append(out, k.Name)
	}
	return out
}

// The published file of the kind at the version
func (k Kind) File(version int) ([]byte, error) {
	if !slices.Contains(k.Versions, version) {
		return nil, fmt.Errorf("%w: %s has no version %d", ErrVersionUnknown, k.Name, version)
	}
	b, err := fs.ReadFile(k.files.fs, FileName(k.Name, version))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIndexInvalid, err)
	}
	return b, nil
}

// Checks a transported record against the file of its kind at the version it carries
func (k Kind) Validate(r Record) error {
	var instance any
	if err := json.Unmarshal(r.JSON, &instance); err != nil {
		return fmt.Errorf("%w: %w", ErrRecordInvalid, err)
	}
	object, _ := instance.(map[string]any)
	version, ok := object["schema_version"].(float64)
	if !ok {
		return fmt.Errorf("%w: %s %s carries no schema_version number", ErrRecordInvalid, k.Name, r.ID)
	}
	rs, err := k.resolve(int(version))
	if err != nil {
		return err
	}
	if err := rs.Validate(instance); err != nil {
		return fmt.Errorf("%w: %s %s: %w", ErrInvalid, k.Name, r.ID, err)
	}
	return nil
}

func (k Kind) resolve(version int) (*jsonschema.Resolved, error) {
	name := FileName(k.Name, version)
	if rs, ok := k.files.resolved.Load(name); ok {
		return rs.(*jsonschema.Resolved), nil
	}
	b, err := k.File(version)
	if err != nil {
		return nil, err
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrIndexInvalid, name, err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrIndexInvalid, name, err)
	}
	k.files.resolved.Store(name, rs)
	return rs, nil
}

// Checks a stored record of the kind in the form every transport sends against its published schema
// Every writer's test calls it on a record it wrote
func (f Files) Validate(kind string, stored any) error {
	ix, err := f.Index()
	if err != nil {
		return err
	}
	k, err := ix.Kind(kind)
	if err != nil {
		return err
	}
	r, err := k.Record(stored)
	if err != nil {
		return err
	}
	return k.Validate(r)
}

// Checks a stored record against the files embedded in the binary
func Validate(kind string, stored any) error {
	return Published.Validate(kind, stored)
}
