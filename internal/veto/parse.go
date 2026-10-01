package veto

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"

	"go.yaml.in/yaml/v3"
)

// YAML shape of a veto file
// Entries stay nodes so one broken entry fails alone
type document struct {
	Vetoes []yaml.Node `yaml:"vetoes"`
}

// One veto as a file states it
// It becomes a Veto only through its Veto method so the patterns are compiled and checked
type Spec struct {
	ID     string `yaml:"id"`
	Tool   string `yaml:"tool"`
	When   []When `yaml:"when"`
	Reason string `yaml:"reason"`
	// Empty means block
	Action Action `yaml:"action,omitempty"`
	// Where the veto came from such as a session id
	// Kept for the reader of the file and never read by code
	Source string `yaml:"source,omitempty"`
	// nil means true
	Enabled *bool `yaml:"enabled,omitempty"`
}

// One condition of a spec on a `tool_input` field
type When struct {
	Field  string `yaml:"field"`
	Match  string `yaml:"match"`
	Unless string `yaml:"unless,omitempty"`
}

// Keys an entry and a condition may hold
// A misspelled key such as unles fails the entry instead of silently changing what it blocks
var (
	specKeys = []string{"id", "tool", "when", "reason", "action", "source", "enabled"}
	whenKeys = []string{"field", "match", "unless"}
)

// Errors name the condition index
func (s Spec) Veto() (Veto, error) {
	when := make([]Condition, 0, len(s.When))
	for j, c := range s.When {
		cond, err := NewCondition(c.Field, c.Match, c.Unless)
		if err != nil {
			return Veto{}, fmt.Errorf("when[%d]: %w", j, err)
		}
		when = append(when, cond)
	}
	return New(s.ID, s.Tool, when, s.Reason, s.Action, s.Enabled == nil || *s.Enabled)
}

// Every entry that builds comes back with the joined errors of the rest
// 1. a document that is not valid YAML fails with ErrYAMLInvalid and returns no vetoes
// 2. a top level key other than vetoes is unknown unless its value defines an anchor for entries to share
// 3. without a vetoes key an unknown top level key fails the file like invalid YAML so a misspelled vetoes never loads as an empty file
// 4. beside vetoes an unknown top level key is reported like a broken entry and every entry still applies
// 5. a broken entry is left out and its error names the index and id so the other entries still apply
// 6. the first entry of an id wins and a later one is reported as a duplicate
// 7. an empty document holds no vetoes
func Parse(b []byte) (Vetoes, error) {
	var d document
	root, err := d.decode(b)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrYAMLInvalid, err)
	}
	errs := root.unknownTopKeys()
	if len(errs) > 0 && !root.has("vetoes") {
		return nil, fmt.Errorf("%w: %w", ErrYAMLInvalid, errors.Join(errs...))
	}
	vetoes := make(Vetoes, 0, len(d.Vetoes))
	for i := range d.Vetoes {
		n := node{&d.Vetoes[i]}
		v, err := entry(n)
		if err == nil && vetoes.Has(v.id) {
			err = ErrIDDuplicate
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("vetoes[%d] (%s): %w", i, n.value("id").Value, err))
			continue
		}
		vetoes = append(vetoes, v)
	}
	return vetoes, errors.Join(errs...)
}

// Returns the top level node of the document
// An empty input or one of comments alone leaves the document and the node empty
func (d *document) decode(b []byte) (node, error) {
	var root yaml.Node
	err := yaml.NewDecoder(bytes.NewReader(b)).Decode(&root)
	if errors.Is(err, io.EOF) {
		return node{&yaml.Node{}}, nil
	}
	if err != nil {
		return node{}, err
	}
	if err := root.Decode(d); err != nil {
		return node{}, err
	}
	return node{root.Content[0]}, nil
}

// Every top level key other than vetoes whose value defines no anchor
func (n node) unknownTopKeys() []error {
	var errs []error
	for _, f := range n.fields() {
		if f.key.Value != "vetoes" && !f.value.anchored() {
			errs = append(errs, fmt.Errorf("%w: line %d: %q", ErrKeyUnknown, f.key.Line, f.key.Value))
		}
	}
	return errs
}

// Merged fields count because the decoder reads them too
func (n node) has(key string) bool {
	for _, f := range n.fields() {
		if f.key.Value == key {
			return true
		}
	}
	return false
}

// Keys are checked after aliases and merge keys resolve so an entry built from a shared anchor passes
func entry(n node) (Veto, error) {
	var s Spec
	if err := n.Decode(&s); err != nil {
		return Veto{}, fmt.Errorf("%w: %w", ErrEntryInvalid, err)
	}
	if err := n.knownKeys(specKeys); err != nil {
		return Veto{}, err
	}
	for _, when := range n.value("when").Content {
		if err := (node{when}).knownKeys(whenKeys); err != nil {
			return Veto{}, err
		}
	}
	return s.Veto()
}

// A YAML node read the way the decoder reads it
// An alias stands for its anchor and a merge key for the fields it merges
type node struct {
	*yaml.Node
}

// One key and its value in a mapping
type field struct {
	key   node
	value node
}

func (n node) resolved() node {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = node{n.Alias}
	}
	return n
}

// Own fields in file order and then the merged fields so the first field of a key is the one the decoder keeps
// 1. of several merged mappings the earlier one wins as YAML merge keys define
// 2. a merge cycle never reaches here because the decode of the same node fails first
func (n node) fields() []field {
	n = n.resolved()
	if n.Kind != yaml.MappingNode {
		return nil
	}
	var own, merged []field
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := node{n.Content[i]}, node{n.Content[i+1]}.resolved()
		if !key.merge() {
			own = append(own, field{key, value})
			continue
		}
		sources := []*yaml.Node{value.Node}
		if value.Kind == yaml.SequenceNode {
			sources = value.Content
		}
		for _, source := range sources {
			merged = append(merged, node{source}.fields()...)
		}
	}
	return append(own, merged...)
}

// The same test the decoder applies to a `<<` key
func (n node) merge() bool {
	return n.Kind == yaml.ScalarNode && n.Value == "<<" && n.ShortTag() == "!!merge"
}

func (n node) knownKeys(keys []string) error {
	for _, f := range n.fields() {
		if !slices.Contains(keys, f.key.Value) {
			return fmt.Errorf("%w: line %d: %q", ErrKeyUnknown, f.key.Line, f.key.Value)
		}
	}
	return nil
}

// An empty node when the mapping lacks the key
func (n node) value(key string) node {
	for _, f := range n.fields() {
		if f.key.Value == key {
			return f.value
		}
	}
	return node{&yaml.Node{}}
}

// Whether the value or anything inside it defines an anchor
func (n node) anchored() bool {
	if n.Anchor != "" {
		return true
	}
	for _, c := range n.Content {
		if (node{c}).anchored() {
			return true
		}
	}
	return false
}
