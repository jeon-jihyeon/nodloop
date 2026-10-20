package curate

import "github.com/jeon-jihyeon/nodloop/internal/knowledge"

// The knobs of a cycle as curate.json keeps them
type Knobs struct {
	// Whether the person's yes to the activate question ended the shadow so tier 1 carries out what it decides
	// False until then so a mark that does not decode keeps tier 1 in shadow
	Activated bool `json:"activated"`
	// The share of sessions that miss each version nodloop approved
	ItemHold knowledge.ItemHold `json:"item_hold"`
}

// The item hold the hooks draw
// 1. none while tier 1 runs in shadow so nothing a run receives changes before nodloop approves a version
// 2. none for a share Check refuses so a mark edited by hand never holds every version
func (k Knobs) Hold() knowledge.ItemHold {
	if !k.Activated || k.ItemHold.Check() != nil {
		return 0
	}
	return k.ItemHold
}
