package veto

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// YAML shape of a veto file
type document struct {
	Vetoes []Spec `yaml:"vetoes"`
}

// One veto as a file states it
// It becomes a Veto only through its Veto method so the patterns are compiled and checked
type Spec struct {
	ID     string `yaml:"id"`
	Tool   string `yaml:"tool"`
	When   []When `yaml:"when"`
	Reason string `yaml:"reason"`
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
	return New(s.ID, s.Tool, when, s.Reason, s.Enabled == nil || *s.Enabled)
}

// Errors name the entry index and id
func Parse(b []byte) (Vetoes, error) {
	var d document
	if err := yaml.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrYAMLInvalid, err)
	}
	vetoes := make(Vetoes, 0, len(d.Vetoes))
	for i, s := range d.Vetoes {
		v, err := s.Veto()
		if err == nil && vetoes.Has(v.id) {
			err = ErrIDDuplicate
		}
		if err != nil {
			return nil, fmt.Errorf("vetoes[%d] (%s): %w", i, s.ID, err)
		}
		vetoes = append(vetoes, v)
	}
	return vetoes, nil
}
