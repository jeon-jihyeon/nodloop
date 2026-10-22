package userconfig

// The curation key of config.json
type Curation struct {
	// Grants a person issued at their terminal and empty by default
	Delegations []Delegation `json:"delegations,omitempty"`
}

// One grant of new answer preferences of a producer in a scope
type Delegation struct {
	// Issued by the terminal command so a renewed scope is another grant
	ID       string              `json:"id"`
	Producer string              `json:"producer"`
	Labels   map[string][]string `json:"labels,omitempty"`
	// The explicit choice of every place of the producer so no empty label set widens by accident
	Everywhere bool `json:"everywhere,omitempty"`
}
