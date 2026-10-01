package file_test

import (
	"context"
	"encoding/json"
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
			name: "an empty procedures folder fails because no review could cite a paragraph",
			args: args{procedures: map[string]string{}},
			want: want{err: file.ErrNoProcedures},
		},
		{
			name: "a byte order mark before the first heading keeps the heading",
			args: args{procedures: map[string]string{"r.md": "\uFEFF# T\n\nbody\n"}},
			want: want{paragraphs: []evidence.Paragraph{{ID: "r#T#1", File: "r.md", Path: []string{"T"}, Text: "body"}}},
		},
		{
			name: "a repeated sibling heading counts on so every id names one paragraph",
			args: args{procedures: map[string]string{"r.md": "# T\n\n## Notes\n\nfirst\n\nsecond\n\n## Notes\n\nthird\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "r#T/Notes#1", File: "r.md", Path: []string{"T", "Notes"}, Text: "first"},
				{ID: "r#T/Notes#2", File: "r.md", Path: []string{"T", "Notes"}, Text: "second"},
				{ID: "r#T/Notes#3", File: "r.md", Path: []string{"T", "Notes"}, Text: "third"},
			}},
		},
		{
			name: "headings that differ only by a separator count on under one id",
			args: args{procedures: map[string]string{"r.md": "# T\n\n## A/B\n\nslash\n\n## A-B\n\nhyphen\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "r#T/A-B#1", File: "r.md", Path: []string{"T", "A/B"}, Text: "slash"},
				{ID: "r#T/A-B#2", File: "r.md", Path: []string{"T", "A-B"}, Text: "hyphen"},
			}},
		},
		{
			name: "a repeated heading whose first occurrence holds no paragraph starts at one",
			args: args{procedures: map[string]string{"r.md": "# T\n\n## Notes\n\n## Notes\n\ntext\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "r#T/Notes#1", File: "r.md", Path: []string{"T", "Notes"}, Text: "text"},
			}},
		},
		{
			name: "hash lines inside a tilde fence stay code under their section",
			args: args{procedures: map[string]string{"t.md": "# Tilde\n\n## Check the job\n\n" +
				"~~~bash\n# restart the job\nsystemctl restart job\n\n# then tail\n```\n~~~\n\n## Decide\n\nHold.\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{
					ID: "t#Tilde/Check the job#1", File: "t.md", Path: []string{"Tilde", "Check the job"},
					Text: "~~~bash\n# restart the job\nsystemctl restart job\n\n# then tail\n```\n~~~",
				},
				{ID: "t#Tilde/Decide#1", File: "t.md", Path: []string{"Tilde", "Decide"}, Text: "Hold."},
			}},
		},
		{
			name: "a hash line indented four columns is code and never a heading",
			args: args{procedures: map[string]string{"i.md": "# Indent\n\n## Check the job\n\n" +
				"    # restart the job\n    systemctl restart job\n\n\t# tab indented\n\n## Decide\n\nHold.\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{
					ID: "i#Indent/Check the job#1", File: "i.md", Path: []string{"Indent", "Check the job"},
					Text: "# restart the job\n    systemctl restart job",
				},
				{ID: "i#Indent/Check the job#2", File: "i.md", Path: []string{"Indent", "Check the job"}, Text: "# tab indented"},
				{ID: "i#Indent/Decide#1", File: "i.md", Path: []string{"Indent", "Decide"}, Text: "Hold."},
			}},
		},
		{
			name: "a fence indented under a list item holds its blank line until the list ends",
			args: args{procedures: map[string]string{"l.md": "# List\n\n## Check the job\n\n" +
				"1. Run:\n\n    ```bash\n    systemctl restart job\n\n    journalctl -u job\n    ```\n\n2. Then wait.\n\n" +
				"Plain text.\n\n    ```\n    code\n\n    more\n\n## Decide\n\nHold.\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "l#List/Check the job#1", File: "l.md", Path: []string{"List", "Check the job"}, Text: "1. Run:"},
				{
					ID: "l#List/Check the job#2", File: "l.md", Path: []string{"List", "Check the job"},
					Text: "```bash\n    systemctl restart job\n\n    journalctl -u job\n    ```",
				},
				{ID: "l#List/Check the job#3", File: "l.md", Path: []string{"List", "Check the job"}, Text: "2. Then wait."},
				{ID: "l#List/Check the job#4", File: "l.md", Path: []string{"List", "Check the job"}, Text: "Plain text."},
				{ID: "l#List/Check the job#5", File: "l.md", Path: []string{"List", "Check the job"}, Text: "```\n    code"},
				{ID: "l#List/Check the job#6", File: "l.md", Path: []string{"List", "Check the job"}, Text: "more"},
				{ID: "l#List/Decide#1", File: "l.md", Path: []string{"List", "Decide"}, Text: "Hold."},
			}},
		},
		{
			name: "closing hashes are not part of the title while a hash inside a word stays",
			args: args{procedures: map[string]string{"h.md": "# Title #\n\n## Use C#\n\ncode\n\n## Decide ##\n\nHold.\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "h#Title/Use C-#1", File: "h.md", Path: []string{"Title", "Use C#"}, Text: "code"},
				{ID: "h#Title/Decide#1", File: "h.md", Path: []string{"Title", "Decide"}, Text: "Hold."},
			}},
		},
		{
			name: "setext underlines make headings only under plain paragraph text",
			args: args{procedures: map[string]string{"s.md": "Title\n=====\n\nCheck\n-----\n\nLook.\n\n---\n\n" +
				"```\ncode\n```\n---\n\n- item\n---\n\nDecide\n---\n\nHold.\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "s#Title/Check#1", File: "s.md", Path: []string{"Title", "Check"}, Text: "Look."},
				{ID: "s#Title/Check#2", File: "s.md", Path: []string{"Title", "Check"}, Text: "---"},
				{ID: "s#Title/Check#3", File: "s.md", Path: []string{"Title", "Check"}, Text: "```\ncode\n```\n---"},
				{ID: "s#Title/Check#4", File: "s.md", Path: []string{"Title", "Check"}, Text: "- item\n---"},
				{ID: "s#Title/Decide#1", File: "s.md", Path: []string{"Title", "Decide"}, Text: "Hold."},
			}},
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
		{
			name: "a rule right under a table or a list is a thematic break and never an underline",
			args: args{procedures: map[string]string{"c.md": "# C\n\n## Causes\n\n" +
				"| source | cause |\n|---|---|\n| source-a | cache eviction |\n---\n\n" +
				"## Escalate\n\nPage the on call.\n- restart the worker\n---\n\n## Decide\n\nHold.\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{
					ID: "c#C/Causes#1", File: "c.md", Path: []string{"C", "Causes"},
					Text: "| source | cause |\n|---|---|\n| source-a | cache eviction |\n---",
				},
				{
					ID: "c#C/Escalate#1", File: "c.md", Path: []string{"C", "Escalate"},
					Text: "Page the on call.\n- restart the worker\n---",
				},
				{ID: "c#C/Decide#1", File: "c.md", Path: []string{"C", "Decide"}, Text: "Hold."},
			}},
		},
		{
			name: "a hash line and a blank line inside an HTML comment stay in one paragraph",
			args: args{procedures: map[string]string{"h.md": "# T\n\n## Check\n\n<!--\n# hidden\n\nnote\n-->\n\n## Decide\n\nHold.\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "h#T/Check#1", File: "h.md", Path: []string{"T", "Check"}, Text: "<!--\n# hidden\n\nnote\n-->"},
				{ID: "h#T/Decide#1", File: "h.md", Path: []string{"T", "Decide"}, Text: "Hold."},
			}},
		},
		{
			name: "an ATX heading without a title stays text so no path part is empty",
			args: args{procedures: map[string]string{"e.md": "# T\n\n##\n\nfirst\n\n## ##\n\nsecond\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "e#T#1", File: "e.md", Path: []string{"T"}, Text: "##"},
				{ID: "e#T#2", File: "e.md", Path: []string{"T"}, Text: "first"},
				{ID: "e#T#3", File: "e.md", Path: []string{"T"}, Text: "## ##"},
				{ID: "e#T#4", File: "e.md", Path: []string{"T"}, Text: "second"},
			}},
		},
		{
			name: "a heading inside a quote or a list item stays text under the section",
			args: args{procedures: map[string]string{"q.md": "# T\n\n## Check\n\n> ## quoted\n> text\n\n" +
				"- step\n\n  ## inside\n\n  more\n\n## Decide\n\nHold.\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "q#T/Check#1", File: "q.md", Path: []string{"T", "Check"}, Text: "> ## quoted\n> text"},
				{ID: "q#T/Check#2", File: "q.md", Path: []string{"T", "Check"}, Text: "- step"},
				{ID: "q#T/Check#3", File: "q.md", Path: []string{"T", "Check"}, Text: "## inside"},
				{ID: "q#T/Check#4", File: "q.md", Path: []string{"T", "Check"}, Text: "more"},
				{ID: "q#T/Decide#1", File: "q.md", Path: []string{"T", "Decide"}, Text: "Hold."},
			}},
		},
		{
			name: "a link reference definition above a setext heading stays out of the title",
			args: args{procedures: map[string]string{"l.md": "[docs]: https://example.com\nTitle  with  spaces\n=====\n\nbody\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "l##1", File: "l.md", Text: "[docs]: https://example.com"},
				{ID: "l#Title with spaces#1", File: "l.md", Path: []string{"Title with spaces"}, Text: "body"},
			}},
		},
		{
			name: "CRLF line ends keep the headings and the titles",
			args: args{procedures: map[string]string{"w.md": "Title\r\n=====\r\n\r\n## Check ##\r\n\r\nx\r\ny\r\n"}},
			want: want{paragraphs: []evidence.Paragraph{
				{ID: "w#Title/Check#1", File: "w.md", Path: []string{"Title", "Check"}, Text: "x\r\ny"},
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
			src, err := file.New(dir, evidence.DefaultContexts())
			require.NoError(t, err)
			got, err := src.Procedures(ctx)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.paragraphs, got.Paragraphs())
		})
	}
}

// The demo procedures keep every id and text the reviews and labels cite
func TestSourceProceduresDemo(t *testing.T) {
	tcs := []struct {
		name string
		// Data directory relative to this package
		args string
		// Golden file under testdata
		want string
	}{
		{"demo procedures", filepath.Join("..", "..", "..", "examples", "demo"), "demo-paragraphs.json"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			src, err := file.New(tc.args, evidence.DefaultContexts())
			require.NoError(t, err)
			got, err := src.Procedures(context.Background())
			require.NoError(t, err)
			golden, err := os.ReadFile(filepath.Join("testdata", tc.want))
			require.NoError(t, err)
			var want []evidence.Paragraph
			require.NoError(t, json.Unmarshal(golden, &want))
			assert.Equal(t, want, got.Paragraphs())
		})
	}
}

// Takes every permission from a folder and gives them back before the temp directory is removed
// Skips the test where permissions do not stop a read such as for root or on windows
func lock(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.Chmod(dir, 0))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("folder permissions do not stop a read here")
	}
}

func TestSourceProceduresFolder(t *testing.T) {
	type args struct {
		folders []string
		files   map[string]string
		// Folders that cannot be opened during the run
		locked []string
	}
	type want struct {
		texts []string
		// Paths relative to the data directory that the error names
		named []string
		err   error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no folder fails because no review could cite a paragraph", args{}, want{err: file.ErrNoProcedures}},
		{
			"a folder of files that are not procedures fails",
			args{folders: []string{"procedures"}, files: map[string]string{"procedures/notes.txt": "x"}},
			want{err: file.ErrNoProcedures},
		},
		{
			"a folder of Markdown it does not read fails with the entries named",
			args{folders: []string{"procedures/team"}, files: map[string]string{
				"procedures/GAP.MD": "skipped", "procedures/team/a.md": "nested",
			}},
			want{named: []string{"procedures/GAP.MD", "procedures/team"}, err: file.ErrProcedureSkipped},
		},
		{
			"a folder that holds procedures only deeper down fails with the folder named",
			args{folders: []string{"procedures/payments/latency"}, files: map[string]string{
				"procedures/payments/latency/l.md": "nested",
			}},
			want{named: []string{"procedures/payments"}, err: file.ErrProcedureSkipped},
		},
		{
			"Markdown beside a procedure is skipped so an archive never stops the reviews",
			args{folders: []string{"procedures/archive"}, files: map[string]string{
				"procedures/r.md": "current", "procedures/GAP.MD": "skipped", "procedures/archive/a.md": "old",
			}},
			want{texts: []string{"current"}},
		},
		{
			"hidden entries and folders without Markdown are skipped",
			args{folders: []string{"procedures/images", "procedures/.obsidian"}, files: map[string]string{
				"procedures/r.md": "current", "procedures/images/a.png": "png",
				"procedures/.obsidian/n.md": "hidden", "procedures/.draft.markdown": "hidden",
				"procedures/.draft.md": "hidden", "procedures/._r.md": "apple double",
			}},
			want{texts: []string{"current"}},
		},
		{
			"the procedures folder is read",
			args{folders: []string{"procedures"}, files: map[string]string{"procedures/r.md": "current"}},
			want{texts: []string{"current"}},
		},
		{
			"a folder that cannot be opened beside a procedure never stops the reviews",
			args{folders: []string{"procedures/private"}, files: map[string]string{"procedures/r.md": "current"}, locked: []string{"procedures/private"}},
			want{texts: []string{"current"}},
		},
		{
			"a folder that cannot be opened deeper down never stops the reviews",
			args{
				folders: []string{"procedures/archive/private"}, files: map[string]string{"procedures/r.md": "current"},
				locked: []string{"procedures/archive/private"},
			},
			want{texts: []string{"current"}},
		},
		{
			"a folder that cannot be opened without a procedure fails with the folder named",
			args{folders: []string{"procedures/private"}, locked: []string{"procedures/private"}},
			want{named: []string{"procedures/private"}, err: file.ErrProcedureSkipped},
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
			for _, folder := range tc.args.locked {
				lock(t, filepath.Join(dir, folder))
			}
			src, err := file.New(dir, evidence.DefaultContexts())
			require.NoError(t, err)
			got, err := src.Procedures(ctx)
			var texts []string
			for _, p := range got.Paragraphs() {
				texts = append(texts, p.Text)
			}
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.texts, texts)
			for _, rel := range tc.want.named {
				assert.ErrorContains(t, err, filepath.Join(dir, rel))
			}
		})
	}
}

func TestSourceSkipped(t *testing.T) {
	type args struct {
		folders []string
		files   map[string]string
		// Folders that cannot be opened during the run
		locked []string
		// Links made after the files keyed by path with their target
		links map[string]string
	}
	type want struct {
		// Paths relative to the data directory
		skipped []string
		err     error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"Markdown spelled otherwise and a folder that holds Markdown are named in name order",
			args{folders: []string{"procedures/archive", "procedures/images"}, files: map[string]string{
				"procedures/r.md": "current", "procedures/GAP.MD": "x", "procedures/gap.markdown": "x",
				"procedures/archive/a.md": "old", "procedures/images/a.png": "png", "procedures/._r.md": "apple double",
			}},
			want{skipped: []string{"procedures/GAP.MD", "procedures/archive", "procedures/gap.markdown"}},
		},
		{
			"a folder that holds Markdown only deeper down is named",
			args{folders: []string{"procedures/payments/latency", "procedures/images/icons"}, files: map[string]string{
				"procedures/r.md": "current", "procedures/payments/latency/l.md": "nested", "procedures/images/icons/a.png": "png",
			}},
			want{skipped: []string{"procedures/payments"}},
		},
		{
			"Markdown under a hidden folder deeper down is not named",
			args{folders: []string{"procedures/archive/.git"}, files: map[string]string{
				"procedures/r.md": "current", "procedures/archive/.git/x.md": "hidden", "procedures/archive/.x.md": "hidden",
			}},
			want{},
		},
		{
			"a folder that cannot be opened is named because nothing proves it holds no Markdown",
			args{folders: []string{"procedures/private"}, files: map[string]string{"procedures/r.md": "current"}, locked: []string{"procedures/private"}},
			want{skipped: []string{"procedures/private"}},
		},
		{
			"a folder that cannot be opened deeper down names the folder under procedures",
			args{
				folders: []string{"procedures/archive/private"}, files: map[string]string{"procedures/r.md": "current"},
				locked: []string{"procedures/archive/private"},
			},
			want{skipped: []string{"procedures/archive"}},
		},
		{
			"a link to a folder that holds Markdown is named",
			args{
				folders: []string{"procedures", "shared/team"}, files: map[string]string{"procedures/r.md": "current", "shared/team/t.md": "team"},
				links: map[string]string{"procedures/team": "shared/team"},
			},
			want{skipped: []string{"procedures/team"}},
		},
		{
			"a link to a folder without Markdown and a dangling link are not named",
			args{
				folders: []string{"procedures", "shared/images"}, files: map[string]string{"procedures/r.md": "current", "shared/images/a.png": "png"},
				links: map[string]string{"procedures/images": "shared/images", "procedures/gone": "shared/none"},
			},
			want{},
		},
		{
			"procedures alone name nothing",
			args{folders: []string{"procedures"}, files: map[string]string{"procedures/r.md": "current"}},
			want{},
		},
		{"no folder fails", args{}, want{err: file.ErrNoProcedures}},
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
			for _, folder := range tc.args.locked {
				lock(t, filepath.Join(dir, folder))
			}
			for link, target := range tc.args.links {
				require.NoError(t, os.Symlink(filepath.Join(dir, target), filepath.Join(dir, link)))
			}
			src, err := file.New(dir, evidence.DefaultContexts())
			require.NoError(t, err)
			got, err := src.Skipped(ctx)
			var skipped []string
			for _, p := range got {
				rel, relErr := filepath.Rel(dir, p)
				require.NoError(t, relErr)
				skipped = append(skipped, filepath.ToSlash(rel))
			}
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.skipped, skipped)
		})
	}
}
