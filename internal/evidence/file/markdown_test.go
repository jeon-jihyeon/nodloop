package file_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/evidence/file"
)

func TestSourceProceduresParagraphs(t *testing.T) {
	type args struct {
		// Data directory name under the temp directory
		dir        string
		procedures map[string]string
	}
	type want struct {
		paragraphs []evidence.Paragraph
		err        error
	}
	deep := []string{"DB failover", "Recovery", "Replica lag"}
	const lag = "db-failover#DB failover/Recovery/Replica lag#"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "fenced code stays in one paragraph under deep headings",
			args: args{procedures: map[string]string{"db-failover.md": "# DB failover\n\n## Recovery\n\n### Replica lag\n\n" +
				"Promote a replica.\n\n```\nkubectl get pods\n\nstill fenced\n```\n\nReset alerts.\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: lag + "1", File: "db-failover.md", Path: deep, Text: "Promote a replica."},
				{ID: lag + "2", File: "db-failover.md", Path: deep, Text: "```\nkubectl get pods\n\nstill fenced\n```"},
				{ID: lag + "3", File: "db-failover.md", Path: deep, Text: "Reset alerts."},
			}},
		},
		{
			name: "a heading drops deeper levels and restarts the index",
			args: args{procedures: map[string]string{"r.md": "# T\n\n## A\n\nfirst\n\nsecond\n\n## B\n\nthird\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "r#T/A#1", File: "r.md", Path: []string{"T", "A"}, Text: "first"},
				{ID: "r#T/A#2", File: "r.md", Path: []string{"T", "A"}, Text: "second"},
				{ID: "r#T/B#1", File: "r.md", Path: []string{"T", "B"}, Text: "third"},
			}},
		},
		{
			name: "text before a heading and a skipped level keep empty path parts",
			args: args{procedures: map[string]string{
				"r.md": "no heading\n\n## Skipped level\n\ntext\n#notaheading\n####### seven\n",
			}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "r##1", File: "r.md", Text: "no heading"},
				{
					ID:   "r#/Skipped level#1",
					File: "r.md",
					Path: []string{"", "Skipped level"},
					Text: "text\n#notaheading\n####### seven",
				},
			}},
		},
		{
			name: "files are read in name order",
			args: args{procedures: map[string]string{
				"b.md": "# B\n\nbee\n", "a.md": "# A\n\nay\n", "notes.txt": "# N\n\nskipped\n",
			}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "a#A#1", File: "a.md", Path: []string{"A"}, Text: "ay"},
				{ID: "b#B#1", File: "b.md", Path: []string{"B"}, Text: "bee"},
			}},
		},
		{
			name: "no procedures give no paragraphs",
			args: args{procedures: map[string]string{}},
			want: want{},
		},
		{
			name: "a byte order mark before the first heading keeps the heading",
			args: args{procedures: map[string]string{"r.md": "\uFEFF# T\n\nbody\n"}},
			want: want{paragraphs: []evidence.Paragraph{{ID: "r#T#1", File: "r.md", Path: []string{"T"}, Text: "body"}}},
		},
		{
			name: "data directory with glob characters still reads its procedures",
			args: args{dir: "data[1]*?", procedures: map[string]string{"r.md": "# T\n\nbody\n"}},
			want: want{paragraphs: []evidence.Paragraph{{ID: "r#T#1", File: "r.md", Path: []string{"T"}, Text: "body"}}},
		},
		{
			name: "a heading with a separator keeps its paragraph under a hyphenated id",
			args: args{procedures: map[string]string{"r.md": "# T\n\n## A/B test\n\ntext\n\n## C#1\n\nmore\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "r#T/A-B test#1", File: "r.md", Path: []string{"T", "A/B test"}, Text: "text"},
				{ID: "r#T/C-1#1", File: "r.md", Path: []string{"T", "C#1"}, Text: "more"},
			}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), tc.args.dir)
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "procedures"), 0o700))
			for name, content := range tc.args.procedures {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "procedures", name), []byte(content), 0o600))
			}
			src, err := file.New(dir)
			require.NoError(t, err)
			got, err := src.Procedures(ctx)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.paragraphs, got.Paragraphs())
		})
	}
}

func TestSourceProceduresFolder(t *testing.T) {
	type args struct {
		folders []string
		files   map[string]string
	}
	type want struct {
		texts []string
		err   error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no folder reads as no procedures", args{}, want{}},
		{
			"the procedures folder is read",
			args{folders: []string{"procedures"}, files: map[string]string{"procedures/r.md": "current"}},
			want{texts: []string{"current"}},
		},
		{
			"a runbooks folder fails with a rename hint",
			args{folders: []string{"runbooks"}, files: map[string]string{"runbooks/r.md": "old"}},
			want{err: file.ErrRunbooksFolder},
		},
		{
			"a runbooks folder beside procedures still fails",
			args{folders: []string{"procedures", "runbooks"}, files: map[string]string{
				"procedures/r.md": "current", "runbooks/r.md": "old",
			}},
			want{err: file.ErrRunbooksFolder},
		},
		{
			"an unreadable procedures folder returns its error",
			args{files: map[string]string{"procedures": "file"}},
			want{err: syscall.ENOTDIR},
		},
		{
			"a folder named like a procedure returns its error",
			args{folders: []string{"procedures/folder.md"}},
			want{err: syscall.EISDIR},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, folder := range tc.args.folders {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, folder), 0o700))
			}
			for name, content := range tc.args.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
			}
			src, err := file.New(dir)
			require.NoError(t, err)
			got, err := src.Procedures(ctx)
			var texts []string
			for _, p := range got.Paragraphs() {
				texts = append(texts, p.Text)
			}
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.texts, texts)
		})
	}
}
