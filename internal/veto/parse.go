package veto

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// YAML shape of a veto file
// The keys are the contract shared with apply and every entry goes through New
type document struct {
	Vetoes []entry `yaml:"vetoes"`
}

type entry struct {
	ID     string      `yaml:"id"`
	Tool   string      `yaml:"tool"`
	When   []condition `yaml:"when"`
	Reason string      `yaml:"reason"`
	// Where the veto came from such as a session id
	// Kept for the reader of the file and never read by code
	Source string `yaml:"source"`
	// nil means true
	Enabled *bool `yaml:"enabled"`
}

type condition struct {
	Field  string `yaml:"field"`
	Match  string `yaml:"match"`
	Unless string `yaml:"unless"`
}

// Errors name the condition index
func (e entry) veto() (Veto, error) {
	when := make([]Condition, 0, len(e.When))
	for j, c := range e.When {
		cond, err := NewCondition(c.Field, c.Match, c.Unless)
		if err != nil {
			return Veto{}, fmt.Errorf("when[%d]: %w", j, err)
		}
		when = append(when, cond)
	}
	return New(e.ID, e.Tool, when, e.Reason, e.Enabled == nil || *e.Enabled)
}

// Errors name the entry index and id
func Parse(b []byte) (Vetoes, error) {
	var d document
	if err := yaml.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrYAMLInvalid, err)
	}
	vetoes := make(Vetoes, 0, len(d.Vetoes))
	for i, e := range d.Vetoes {
		v, err := e.veto()
		if err == nil && vetoes.Has(v.id) {
			err = ErrIDDuplicate
		}
		if err != nil {
			return nil, fmt.Errorf("vetoes[%d] (%s): %w", i, e.ID, err)
		}
		vetoes = append(vetoes, v)
	}
	return vetoes, nil
}
