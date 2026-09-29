package file_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/evidence/file"
)

// Every row writes one procedure named r.md
func TestSourceProcedures(t *testing.T) {
	type want struct {
		procedures evidence.Procedures
		err        error
		// A fragment of the message naming the file
		text string
	}
	const body = "# T\n\n## A\n\nfirst\n\n---\n\nsecond\n"
	path := []string{"T", "A"}
	paragraphs := []evidence.Paragraph{
		{ID: "r#T/A#1", File: "r.md", Path: path, Text: "first"},
		{ID: "r#T/A#2", File: "r.md", Path: path, Text: "---"},
		{ID: "r#T/A#3", File: "r.md", Path: path, Text: "second"},
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{
			"a file without front matter has an empty scope and a fence later in the file is text",
			body,
			want{procedures: evidence.Procedures{{Slug: "r", File: "r.md", Paragraphs: paragraphs}}},
		},
		{
			"front matter is parsed and stripped so the paragraph ids equal the file without it",
			"---\nchange_contexts: [planned_operational_change]\nmetrics: [conversion_count, click_count]\n---\n" + body,
			want{procedures: evidence.Procedures{{
				Slug: "r", File: "r.md", Paragraphs: paragraphs,
				Scope: evidence.Scope{
					ChangeContexts: []evidence.Context{evidence.ContextPlannedChange},
					Metrics:        []string{"conversion_count", "click_count"},
				},
			}}},
		},
		{
			"empty front matter is an empty scope",
			"---\n---\n" + body,
			want{procedures: evidence.Procedures{{Slug: "r", File: "r.md", Paragraphs: paragraphs}}},
		},
		{
			"an unknown key fails with the file named",
			"---\ndims: {source: a}\n---\n" + body,
			want{err: evidence.ErrMalformed, text: "r.md"},
		},
		{
			"an unknown change context fails with the file named",
			"---\nchange_contexts: [someday]\n---\n" + body,
			want{err: evidence.ErrUnknownContext, text: "r.md"},
		},
		{
			"an unclosed front matter fails with the file named",
			"---\nmetrics: [click_count]\n" + body,
			want{err: evidence.ErrMalformed, text: "r.md"},
		},
		{
			"front matter that does not decode fails with the file named",
			"---\nmetrics: click_count: 1\n---\n" + body,
			want{err: evidence.ErrMalformed, text: "r.md"},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, "procedures"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "procedures", "r.md"), []byte(tc.args), 0o600))
			src, err := file.New(dir)
			require.NoError(t, err)
			got, err := src.Procedures(ctx)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Contains(t, fmt.Sprint(err), tc.want.text)
			assert.Equal(t, tc.want.procedures, got)
		})
	}
}
