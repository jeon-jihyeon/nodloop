package record

import "time"

// The last prune a store keeps
type Mark struct {
	At time.Time `json:"at"`
	// Zero on a mark of a release that did not record it
	Cutoff time.Time `json:"cutoff,omitzero"`
}
