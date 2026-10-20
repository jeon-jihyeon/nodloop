package curate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
)

// The hooks read the mark without its lock and draw nothing from a mark no pass wrote
func TestReadMark(t *testing.T) {
	type args struct {
		// The content of curate.json and none when empty
		content string
		// curate.json is a directory that cannot be read as a file
		unreadable bool
	}
	type want struct {
		mark curate.Mark
		err  error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no mark reads as the zero mark", args{}, want{}},
		{
			"a mark keeps its knobs",
			args{content: `{"knobs":{"activated":true,"item_hold":0.1}}`},
			want{mark: curate.Mark{Knobs: curate.Knobs{Activated: true, ItemHold: 0.1}}},
		},
		{"a torn mark reads as the zero mark", args{content: `{"knobs":{"activ`}, want{}},
		{"a mark that cannot be read fails", args{unreadable: true}, want{err: curate.ErrMark}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, curate.MarkFile)
			if tc.args.content != "" {
				require.NoError(t, os.WriteFile(path, []byte(tc.args.content), 0o600))
			}
			if tc.args.unreadable {
				require.NoError(t, os.Mkdir(path, 0o755))
			}

			got, err := curate.ReadMark(dir)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.mark, got)
		})
	}
}
