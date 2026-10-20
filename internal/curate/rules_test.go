package curate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A scope lies in a temporary place when every dir it names lies below a temp dir or holds a scratchpad segment
func TestTempDirsHold(t *testing.T) {
	temps := curate.TempDirs{"/tmp", "/var/folders/xy/T/", "relative"}
	scoped := func(dirs ...string) knowledge.RunScope {
		return knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}, "dir": dirs}}
	}
	tcs := []struct {
		name string
		args knowledge.RunScope
		want bool
	}{
		{"a dir below the temp dir", scoped("/tmp/acme"), true},
		{"the temp dir itself", scoped("/tmp"), true},
		{"a dir below a temp dir named with a trailing slash", scoped("/var/folders/xy/T/run"), true},
		{"a scratchpad segment anywhere", scoped("/work/scratchpad/notes"), true},
		{"a dir whose name only starts like the temp dir", scoped("/tmpfiles/acme"), false},
		{"a tmp folder inside a repository", scoped("tmp/notes"), false},
		{"a relative dir below a relative entry is no temp dir of the OS", scoped("relative/acme"), false},
		{"one dir of two outside the temp dir", scoped("/tmp/acme", "/work/acme"), false},
		{"a scope with no dir", knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}}}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, temps.Hold(tc.args))
		})
	}
}
