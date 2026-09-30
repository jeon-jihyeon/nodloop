package file_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func TestNamed(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want bool
	}{
		{"user file", "/Users/me/.claude/nodloop/vetoes.yaml", true},
		{"project file", "/Users/me/repo/.claude/nodloop/vetoes.yaml", true},
		{"approved file", "/Users/me/.claude/nodloop/vetoes.approved.0123456789ab.yaml", true},
		{"relative project file", ".claude/nodloop/vetoes.yaml", true},
		{"unclean path", "/Users/me/repo/x/../.claude/nodloop/vetoes.yaml", true},
		{"lock file", "/Users/me/.claude/nodloop/vetoes.approved.0123456789ab.yaml.lock", false},
		{"veto file name in another directory", "/Users/me/repo/vetoes.yaml", false},
		{"directory that only ends like the veto directory", "/Users/me/x.claude/nodloop/vetoes.yaml", false},
		{"settings file", "/Users/me/.claude/settings.json", false},
		{"empty path", "", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, file.Named(tc.args))
		})
	}
}
