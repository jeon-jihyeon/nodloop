package file

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// Any input splits without a panic into unique ids and drops only the lines of a top level heading
func FuzzSplitParagraphs(f *testing.F) {
	seeds := []string{
		"# DB failover\n\n## Recovery\n\n### Replica lag\n\nPromote a replica.\n\n```\nkubectl get pods\n\nstill fenced\n```\n\nReset alerts.\n",
		"no heading\n\n## Skipped level\n\ntext\n#notaheading\n####### seven\n",
		"# T\n\n## Notes\n\nfirst\n\n## Notes\n\nsecond\n\n## A/B\n\nslash\n",
		"~~~bash\n# restart the job\n\n```\n~~~\n",
		"    # restart the job\n\n\t# tab indented\n",
		"1. Run:\n\n    ```bash\n    systemctl restart job\n\n    journalctl -u job\n    ```\n\n2. Then wait.\n",
		"# Title #\n\n## Use C#\n\ncode\n\n## Decide ##\n",
		"Title\n=====\n\nCheck\n-----\n\n```\ncode\n```\n---\n\n- item\n---\n",
		"| a | b |\n|---|---|\n| 1 | 2 |\n---\n\nIntro:\n- a\n---\n",
		"<!--\n# hidden\n\nnote\n-->\n\n##\n\n## ##\n",
		"> ## quoted\n> text\n\n- step\n\n  ## inside\n",
		"[docs]: https://example.com\nTitle  with  spaces\n=====\n",
		"Title\r\n=====\r\n\r\n## Check ##\r\n\r\nx\r\n",
		"Foo *bar\nbaz*\n====\n",
		"- foo\n-----\n",
		"<div>\n*hello*\n\n</div>\n",
		"```\naaa\n\n",
		"#\tFoo\n  ## foo\n    # foo\n",
		"\\## foo\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, content string) {
		got := splitParagraphs("f.md", content)
		ids := map[evidence.ParagraphID]bool{}
		for _, p := range got {
			require.False(t, ids[p.ID], "id %s names two paragraphs", p.ID)
			ids[p.ID] = true
		}
		src := newSource(content)
		taken := map[int]bool{}
		for first, h := range src.headings(src.parse()) {
			for i := first; i <= h.last; i++ {
				taken[i] = true
			}
		}
		var texts strings.Builder
		for _, p := range got {
			texts.WriteString(p.Text + "\n")
		}
		for i, line := range strings.Split(content, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || taken[i] {
				continue
			}
			assert.Contains(t, texts.String(), trimmed, "line %d is in no paragraph", i)
		}
	})
}
