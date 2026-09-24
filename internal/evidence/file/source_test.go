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
	"github.com/jeon-jihyeon/nodloop/internal/evidence/evidencetest"
	"github.com/jeon-jihyeon/nodloop/internal/evidence/file"
)

func TestSourceContract(t *testing.T) {
	src, err := file.New("testdata")
	require.NoError(t, err)
	evidencetest.Run(t, src, evidencetest.Seed{
		EventID:        "tq-005",
		MinPoints:      288,
		ChangeContext:  evidence.ContextNoKnownChange,
		ParagraphID:    "data-integrity-hold#Data integrity hold/Coverage#1",
		LabeledEventID: "tq-005",
	})
}

func TestNew(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(regular, nil, 0o600))
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"a directory opens", dir, nil},
		{"missing path fails", filepath.Join(dir, "missing"), os.ErrNotExist},
		{"regular file fails", regular, file.ErrNotDirectory},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := file.New(tc.args)
			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, tc.want == nil, got != nil, "a source opens exactly when there is no error")
		})
	}
}

func TestSourceLabels(t *testing.T) {
	type want struct {
		labels []evidence.Label
		// A fragment of the message naming the file and line
		text string
	}
	tcs := []struct {
		name string
		args map[string]string
		want want
	}{
		{
			name: "labels are read in file order with every field",
			args: map[string]string{"labels.jsonl": `{"event_id":"e2","type":"click_spike","seed":true,` +
				`"anomalies":[{"metric":"click_count","dims":{"source":"a"}}],"paragraph_ids":["p1"],` +
				`"required_checks":["p2"],"knowledge":["k1"],"hold":false}` + "\n\n" +
				`{"event_id":"e1","type":"hold","seed":false,"anomalies":[],"paragraph_ids":[],` +
				`"required_checks":["p3"],"hold":true}` + "\n"},
			want: want{labels: []evidence.Label{
				{
					EventID:        "e2",
					Type:           evidence.TypeClickSpike,
					Seed:           true,
					Anomalies:      []evidence.SeriesRef{{Metric: "click_count", Dims: map[string]string{"source": "a"}}},
					Paragraphs:     []evidence.ParagraphID{"p1"},
					RequiredChecks: []evidence.ParagraphID{"p2"},
					Knowledge:      []string{"k1"},
				},
				{
					EventID:        "e1",
					Type:           evidence.TypeHold,
					Anomalies:      []evidence.SeriesRef{},
					Paragraphs:     []evidence.ParagraphID{},
					RequiredChecks: []evidence.ParagraphID{"p3"},
				},
			}},
		},
		{
			name: "missing labels file reads as empty",
			args: map[string]string{},
			want: want{},
		},
		{
			name: "a corrupt line is named in the error",
			args: map[string]string{"labels.jsonl": "{\"event_id\":\"a\"}\n\n{not json}\n"},
			want: want{text: "labels.jsonl line 3"},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range tc.args {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
			}
			src, err := file.New(dir)
			require.NoError(t, err)
			got, err := src.Labels(ctx)
			assert.Contains(t, fmt.Sprint(err), tc.want.text)
			assert.Equal(t, tc.want.labels, got)
		})
	}
}
