package knowledge

// What a run trace keeps as its input
// Every writer builds it through NewRunInput and every reader decodes the stored JSON into it
type RunInput struct {
	// The versions the run received
	// Stored as an empty list and never null
	Applied []Ref `json:"applied"`
	// The plugin version a conversation hook ran under so a report can compare releases
	// Empty for a run of any other producer
	Plugin string `json:"plugin,omitempty"`
	// The versions a holdout kept from the run while Applied stays empty
	Withheld []Ref `json:"withheld,omitempty"`
	// Whether the run went through a holdout draw so report effect compares only drawn runs
	// A run recorded without a key or under a share of 0 went through none
	Drawn bool `json:"drawn,omitempty"`
	// The share the draw used so a report can test the arms against it
	Share float64 `json:"share,omitempty"`
	// The rule that turned the key into the draw
	// A later rule gets the next number so a report can tell arms drawn under two rules apart
	Scheme int `json:"scheme,omitempty"`
	// The draw the run belongs to
	// Every run of one holdout key draws the same arm so a report counts them as one unit and never as independent runs
	Unit string `json:"unit,omitempty"`
}

// The rule Withholds follows
const DrawScheme = 1

// The input of a run that went through the draw of the share
// 1. withheld is what the draw decided for the key of the run and unit names that key
// 2. a share of 0 draws nothing
// 3. lists that disagree with the draw such as applied items on a withheld draw leave the run undrawn so it never enters an arm
func (in RunInput) WithDraw(share Holdout, withheld bool, unit string) RunInput {
	if share <= 0 || withheld && len(in.Applied) > 0 || !withheld && len(in.Withheld) > 0 {
		return in
	}
	in.Drawn, in.Share, in.Scheme, in.Unit = true, float64(share), DrawScheme, unit
	return in
}

// The input of one run
// 1. applied and withheld never both hold a version and ErrWithheldApplied refuses both
// 2. a nil applied becomes an empty list
func NewRunInput(applied, withheld []Ref, plugin string) (RunInput, error) {
	if len(applied) > 0 && len(withheld) > 0 {
		return RunInput{}, ErrWithheldApplied
	}
	if applied == nil {
		applied = []Ref{}
	}
	return RunInput{Applied: applied, Plugin: plugin, Withheld: withheld}, nil
}
