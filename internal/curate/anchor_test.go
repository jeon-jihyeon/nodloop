package curate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestAnchorVersionTrace(t *testing.T) {
	refs := []knowledge.Ref{{ID: "k-1", Version: 2}}
	files := []curate.AnchorFile{{Path: "plan.md", HMAC: hmac}, {Path: "CLAUDE.md", HMAC: hmac, MemoryType: "Project"}}
	type want struct {
		input string
		err   error
	}
	tcs := []struct {
		name string
		args curate.AnchorVersion
		want want
	}{
		{
			"a version names its refs, its files and its cause",
			curate.AnchorVersion{Version: 2, Refs: refs, Files: files, Cause: "a-1"},
			want{`{"version":2,"refs":[{"id":"k-1","version":2}],"files":[{"path":"plan.md","hmac":"` + hmac + `"},` +
				`{"path":"CLAUDE.md","hmac":"` + hmac + `","memory_type":"Project"}],"cause":"a-1"}`, nil},
		},
		{
			"an empty anchor keeps empty lists",
			curate.AnchorVersion{Version: 1, Cause: curate.CauseApproval},
			want{`{"version":1,"refs":[],"files":[],"cause":"approval"}`, nil},
		},
		{"version 0 is refused", curate.AnchorVersion{Cause: "a-1"}, want{err: curate.ErrAnchorInvalid}},
		{"no cause is refused", curate.AnchorVersion{Version: 1}, want{err: curate.ErrAnchorInvalid}},
		{"a ref without its version is refused", curate.AnchorVersion{Version: 1, Cause: "a-1", Refs: []knowledge.Ref{{ID: "k-1"}}}, want{err: curate.ErrAnchorInvalid}},
		{
			"a file without its path is refused",
			curate.AnchorVersion{Version: 1, Cause: "a-1", Files: []curate.AnchorFile{{HMAC: hmac}}},
			want{err: curate.ErrAnchorInvalid},
		},
		{
			"a file whose HMAC is no 64 hex is refused",
			curate.AnchorVersion{Version: 1, Cause: "a-1", Files: []curate.AnchorFile{{Path: "plan.md", HMAC: "plan text"}}},
			want{err: curate.ErrAnchorInvalid},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.Trace(at)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.input, string(got.Input))
		})
	}
}

func TestAnchorVersionTraceFields(t *testing.T) {
	got, err := curate.AnchorVersion{Version: 3, Cause: curate.CauseApproval}.Trace(at)
	require.NoError(t, err)

	assert.Equal(t, []any{trace.NameAnchor, "v3", at}, []any{got.Name, got.Subject, got.Time})
	assert.NoError(t, schema.Validate(string(trace.NameAnchor), got.Record()))
}
