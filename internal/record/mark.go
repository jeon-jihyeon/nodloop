package record

import "time"

// When the last prune or consolidate of a record directory ran or the newest record its OTLP export sent
// Only a prune records Cutoff
type Mark struct {
	At time.Time `json:"at"`
	// Zero on a mark of a release that did not record it
	Cutoff time.Time `json:"cutoff,omitzero"`
}
