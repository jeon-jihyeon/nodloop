package curate

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/markfile"
)

const (
	// The mark of the last curate pass in the record directory
	MarkFile = "curate.json"
	// At most one daily pass per interval whatever stops the conversations make
	Interval = 24 * time.Hour
	// The lock the writes of interventions over the record directory take turns on
	TurnFile = "intervene.lock"
)

// The turn every local writer over the record directory waits for
// The CLI and MCP stdio and the hooks and the capture of one person write the same records from processes of their own
func FileTurn(dir string) func() (func(), error) {
	return func() (func(), error) {
		held, err := markfile.Wait[struct{}](filepath.Join(dir, TurnFile))
		if err != nil {
			return nil, err
		}
		return func() { _ = held.Release() }, nil
	}
}

// The last pass as curate.json keeps it for the hooks
// Every fact a consumer needs beyond the hooks is in a record since a mark is state a writer replaces
type Mark struct {
	// When the last daily pass started
	// A pass without --daily keeps it so the daily work still runs once a day
	At time.Time `json:"at,omitzero"`
	// The cycle the knob event of the pass marks with its policy version and seed
	CycleID       string `json:"cycle_id,omitempty"`
	PolicyVersion string `json:"policy_version,omitempty"`
	Seed          string `json:"seed,omitempty"`
	Knobs         Knobs  `json:"knobs"`
	// The questions due when the pass ended
	Due []Due `json:"due,omitempty"`
}

// Whether a daily pass is due at now
// A mark from the future reads as due so a clock set back never stops the daily pass
func (m Mark) Daily(now time.Time) bool {
	elapsed := now.Sub(m.At)
	return m.At.IsZero() || elapsed < 0 || elapsed >= Interval
}

// A question curate prepared that waits for the person
type Due struct {
	Kind QuestionKind `json:"kind"`
	// The decide the question asks about
	Decide string     `json:"decide"`
	Change ChangeKind `json:"change"`
	// The version the question names
	Ref knowledge.Ref `json:"ref"`
	// The rule that sent the change to the person
	Rule RuleCode `json:"rule,omitempty"`
	// The run the change cites so a stop asks a question of its own session first
	Run string `json:"run"`
	// The runs the version reaches so a stop in another place never asks it
	Place *knowledge.RunScope `json:"place,omitempty"`
	// When curate decided it so the oldest is asked first
	At time.Time `json:"at"`
	// The intervention of an agent a question of a proposal asks the person to carry out
	Proposal string `json:"proposal,omitempty"`
	// The session that submitted the proposal so the stop of its turn asks it
	Session string `json:"session,omitempty"`
}

// The mark of the last pass over one record directory
type Marker struct {
	dir string
}

func NewMarker(dir string) Marker {
	return Marker{dir: dir}
}

// The mark one pass holds until Release
type Held struct {
	held *markfile.Held[Mark]
	// The mark the last pass left and the zero Mark when none decodes
	Mark Mark
}

// Takes the mark without waiting
// Nil without an error when another pass holds it so the caller ends at once and two passes never decide one change twice
func (m Marker) Hold() (*Held, error) {
	held, err := markfile.Hold[Mark](filepath.Join(m.dir, MarkFile))
	if err != nil || held == nil {
		return nil, markErr(err)
	}
	return &Held{held: held, Mark: held.Mark}, nil
}

// Replaces the mark with the one the pass leaves
func (h *Held) Write(m Mark) error {
	err := h.held.Write(m)
	if err == nil {
		h.Mark = m
	}
	return markErr(err)
}

// Lets the next pass take the mark
func (h *Held) Release() error {
	return markErr(h.held.Release())
}

// The mark of the record directory read without its lock as the hooks read it
// A missing mark and one that does not decode read as the zero Mark so a hook draws nothing no pass set
func ReadMark(dir string) (Mark, error) {
	m, err := markfile.Read[Mark](filepath.Join(dir, MarkFile))
	if err != nil {
		return Mark{}, markErr(err)
	}
	return m, nil
}

func markErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrMark, err)
}
