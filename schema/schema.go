// Package schema embeds the published JSON Schema of every record kind and version and the index of the kinds
// go generate in cmd/nodloop writes the files from the Go types of the records
package schema

import "embed"

//go:embed *.json
var Files embed.FS
