package main

// The keys of `~/.nodloop/config.json` the server reads
// The nodloop CLI writes the same file so every other key is kept as it is
type fileConfig struct {
	RecordDir string       `json:"record_dir,omitempty"`
	Server    serverConfig `json:"server,omitzero"`
}
