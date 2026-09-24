package file

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The demo data set ships inside the binary so setup can unpack it anywhere
//
//go:embed testdata/events.csv testdata/contexts.csv testdata/labels.jsonl
//go:embed testdata/policy.yaml testdata/knowledge.jsonl testdata/runbooks/*.md
var demo embed.FS

// Writes the demo data set under dir
// Existing files are overwritten so a re-run refreshes them
func WriteDemo(dir string) error {
	return fs.WalkDir(demo, "testdata", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, strings.TrimPrefix(path, "testdata"))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := demo.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}
