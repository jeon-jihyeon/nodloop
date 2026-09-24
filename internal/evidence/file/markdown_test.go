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

func TestSourceParagraphs(t *testing.T) {
	type args struct {
		// Data directory name under the temp directory
		dir      string
		runbooks map[string]string
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
			args: args{runbooks: map[string]string{"db-failover.md": "# DB failover\n\n## Recovery\n\n### Replica lag\n\n" +
				"Promote a replica.\n\n```\nkubectl get pods\n\nstill fenced\n```\n\nReset alerts.\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: lag + "1", File: "db-failover.md", Path: deep, Text: "Promote a replica."},
				{ID: lag + "2", File: "db-failover.md", Path: deep, Text: "```\nkubectl get pods\n\nstill fenced\n```"},
				{ID: lag + "3", File: "db-failover.md", Path: deep, Text: "Reset alerts."},
			}},
		},
		{
			name: "a heading drops deeper levels and restarts the index",
			args: args{runbooks: map[string]string{"r.md": "# T\n\n## A\n\nfirst\n\nsecond\n\n## B\n\nthird\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "r#T/A#1", File: "r.md", Path: []string{"T", "A"}, Text: "first"},
				{ID: "r#T/A#2", File: "r.md", Path: []string{"T", "A"}, Text: "second"},
				{ID: "r#T/B#1", File: "r.md", Path: []string{"T", "B"}, Text: "third"},
			}},
		},
		{
			name: "text before a heading and a skipped level keep empty path parts",
			args: args{runbooks: map[string]string{
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
			args: args{runbooks: map[string]string{
				"b.md": "# B\n\nbee\n", "a.md": "# A\n\nay\n", "notes.txt": "# N\n\nskipped\n",
			}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "a#A#1", File: "a.md", Path: []string{"A"}, Text: "ay"},
				{ID: "b#B#1", File: "b.md", Path: []string{"B"}, Text: "bee"},
			}},
		},
		{
			name: "no runbooks give no paragraphs",
			args: args{runbooks: map[string]string{}},
			want: want{},
		},
		{
			name: "data directory with glob characters still reads its runbooks",
			args: args{dir: "data[1]*?", runbooks: map[string]string{"r.md": "# T\n\nbody\n"}},
			want: want{paragraphs: []evidence.Paragraph{{ID: "r#T#1", File: "r.md", Path: []string{"T"}, Text: "body"}}},
		},
		{
			name: "a heading with a separator keeps its paragraph under a hyphenated id",
			args: args{runbooks: map[string]string{"r.md": "# T\n\n## A/B test\n\ntext\n\n## C#1\n\nmore\n"}},
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
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "runbooks"), 0o700))
			for name, content := range tc.args.runbooks {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "runbooks", name), []byte(content), 0o600))
			}
			src, err := file.New(dir)
			require.NoError(t, err)
			got, err := src.Paragraphs(ctx)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.paragraphs, got)
		})
	}
}

func TestSourceParagraphsWithoutRunbooksDirectory(t *testing.T) {
	src, err := file.New(t.TempDir())
	require.NoError(t, err)
	got, err := src.Paragraphs(context.Background())
	assert.NoError(t, err)
	assert.Nil(t, got)
}

func TestSourceParagraphsFailsOnUnreadableRunbooks(t *testing.T) {
	type args struct {
		folders []string
		files   []string
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"folder named like a runbook fails", args{folders: []string{"runbooks/folder.md"}}, syscall.EISDIR},
		{"runbooks path that is a file fails", args{files: []string{"runbooks"}}, syscall.ENOTDIR},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, folder := range tc.args.folders {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, folder), 0o700))
			}
			for _, name := range tc.args.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
			}
			src, err := file.New(dir)
			require.NoError(t, err)
			got, err := src.Paragraphs(context.Background())
			assert.ErrorIs(t, err, tc.want)
			assert.Nil(t, got)
		})
	}
}
