package main

// The keys of `~/.nodloop/config.json` the server reads
// The nodloop CLI writes the same file so every other key is kept as it is
type fileConfig struct {
	RecordDir string `json:"record_dir,omitempty"`
	// The share of knowledge_for calls with a holdout key whose items are withheld
	Holdout float64 `json:"holdout,omitempty"`
	// Whole days of unjudged and uncited runs each tenant keeps and 0 for every run
	Retention int          `json:"retention,omitempty"`
	Server    serverConfig `json:"server,omitzero"`
}
