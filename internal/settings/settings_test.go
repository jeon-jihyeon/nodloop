package settings_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/settings"
)

func TestDocumentInstall(t *testing.T) {
	const exe = "/Users/me/go/bin/nodloop"
	const ours = `{"matcher":"*","hooks":[{"type":"command","command":"/Users/me/go/bin/nodloop guard","timeout":5}]}`
	const other = `{"matcher":"Bash","hooks":[{"type":"command","command":"/usr/local/bin/other-hook","timeout":10}]}`
	type args struct {
		doc string
		exe string
	}
	type want struct {
		changed bool
		err     error
		doc     string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"empty document gets our hook", args{`{}`, exe}, want{true, nil, `{"hooks":{"PreToolUse":[` + ours + `]}}`}},
		{
			"existing hooks gain our hook after other groups and keep every other key",
			args{`{"hooks":{"PreToolUse":[` + other + `],"PostToolUse":[]},"keep":true}`, exe},
			want{true, nil, `{"hooks":{"PreToolUse":[` + other + `,` + ours + `],"PostToolUse":[]},"keep":true}`},
		},
		{
			"null hooks is treated as missing",
			args{`{"hooks":null}`, exe},
			want{true, nil, `{"hooks":{"PreToolUse":[` + ours + `]}}`},
		},
		{
			"null PreToolUse is treated as missing",
			args{`{"hooks":{"PreToolUse":null}}`, exe},
			want{true, nil, `{"hooks":{"PreToolUse":[` + ours + `]}}`},
		},
		{
			"already installed hook is left unchanged",
			args{`{"hooks":{"PreToolUse":[` + ours + `]}}`, exe},
			want{false, nil, `{"hooks":{"PreToolUse":[` + ours + `]}}`},
		},
		{
			"quoted executable is recognized",
			args{`{"hooks":{"PreToolUse":[{"hooks":[{"command":"'/Users/my apps/nodloop' guard"}]}]}}`, exe},
			want{false, nil, `{"hooks":{"PreToolUse":[{"hooks":[{"command":"'/Users/my apps/nodloop' guard"}]}]}}`},
		},
		{
			"release binary name is recognized",
			args{`{"hooks":{"PreToolUse":[{"hooks":[{"command":"/opt/nodloop-darwin-arm64 guard"}]}]}}`, exe},
			want{false, nil, `{"hooks":{"PreToolUse":[{"hooks":[{"command":"/opt/nodloop-darwin-arm64 guard"}]}]}}`},
		},
		{
			"another guard command is not ours",
			args{`{"hooks":{"PreToolUse":[{"hooks":[{"command":"/usr/bin/other guard"}]}]}}`, exe},
			want{true, nil, `{"hooks":{"PreToolUse":[{"hooks":[{"command":"/usr/bin/other guard"}]},` + ours + `]}}`},
		},
		{
			"entries without a command are skipped",
			args{`{"hooks":{"PreToolUse":[{"hooks":["odd",{"type":"command"}]}]}}`, exe},
			want{true, nil, `{"hooks":{"PreToolUse":[{"hooks":["odd",{"type":"command"}]},` + ours + `]}}`},
		},
		{
			"path with spaces is single quoted",
			args{`{}`, "/Users/me/my apps/nodloop"},
			want{true, nil, `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[` +
				`{"type":"command","command":"'/Users/me/my apps/nodloop' guard","timeout":5}]}]}}`},
		},
		{
			"single quote in the path is escaped",
			args{`{}`, "/Users/it's/nodloop"},
			want{true, nil, `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[` +
				`{"type":"command","command":"'/Users/it'\\''s/nodloop' guard","timeout":5}]}]}}`},
		},
		{
			"dollar sign in the path is single quoted",
			args{`{}`, "/opt/$HOME/nodloop"},
			want{true, nil, `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[` +
				`{"type":"command","command":"'/opt/$HOME/nodloop' guard","timeout":5}]}]}}`},
		},
		{
			"hooks that is not an object is left as is",
			args{`{"hooks":"odd"}`, exe},
			want{false, settings.ErrHooksInvalid, `{"hooks":"odd"}`},
		},
		{
			"PreToolUse that is not an array is left as is",
			args{`{"hooks":{"PreToolUse":{}}}`, exe},
			want{false, settings.ErrHooksInvalid, `{"hooks":{"PreToolUse":{}}}`},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := settings.Document{}
			require.NoError(t, json.Unmarshal([]byte(tc.args.doc), &doc))

			changed, err := doc.Install(tc.args.exe)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.changed, changed)
			got, err := json.Marshal(doc)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want.doc, string(got))
		})
	}
}

func TestDocumentUninstall(t *testing.T) {
	type want struct {
		changed bool
		doc     string
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{
			"only our hook is removed and other keys are kept",
			`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"command":"other-hook"}]},` +
				`{"matcher":"*","hooks":[{"command":"nodloop guard"}]}],"PostToolUse":[]},"keep":true}`,
			want{true, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"command":"other-hook"}]}],"PostToolUse":[]},` +
				`"keep":true}`},
		},
		{
			"other entries of a shared group are kept",
			`{"hooks":{"PreToolUse":[{"hooks":[{"command":"nodloop guard"},{"command":"other-hook"}]}]}}`,
			want{true, `{"hooks":{"PreToolUse":[{"hooks":[{"command":"other-hook"}]}]}}`},
		},
		{
			"non object group is kept",
			`{"hooks":{"PreToolUse":["odd",{"hooks":[{"command":"nodloop guard"}]}]}}`,
			want{true, `{"hooks":{"PreToolUse":["odd"]}}`},
		},
		{
			"hooks key is dropped when only our hook was left",
			`{"hooks":{"PreToolUse":[{"hooks":[{"command":"nodloop guard"}]}]},"keep":true}`,
			want{true, `{"keep":true}`},
		},
		{
			"quoted executable of a current install is removed",
			`{"hooks":{"PreToolUse":[{"hooks":[{"command":"'/Users/my apps/nodloop' guard"}]}]}}`,
			want{true, `{}`},
		},
		{
			"unquoted executable with spaces of an older install is removed",
			`{"hooks":{"PreToolUse":[{"hooks":[{"command":"/Users/my apps/nodloop guard"}]}]}}`,
			want{true, `{}`},
		},
		{
			"executable with a space in its name is not ours",
			`{"hooks":{"PreToolUse":[{"hooks":[{"command":"/Users/me/nodloop tool guard"}]}]}}`,
			want{false, `{"hooks":{"PreToolUse":[{"hooks":[{"command":"/Users/me/nodloop tool guard"}]}]}}`},
		},
		{"settings without our hook are left unchanged", `{"keep":true}`, want{false, `{"keep":true}`}},
		{"hooks that is not an object is left as is", `{"hooks":"odd"}`, want{false, `{"hooks":"odd"}`}},
		{
			"PreToolUse that is not an array is left as is",
			`{"hooks":{"PreToolUse":{}}}`,
			want{false, `{"hooks":{"PreToolUse":{}}}`},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := settings.Document{}
			require.NoError(t, json.Unmarshal([]byte(tc.args), &doc))

			changed := doc.Uninstall()
			assert.Equal(t, tc.want.changed, changed)
			got, err := json.Marshal(doc)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want.doc, string(got))
		})
	}
}
