package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteReproducesTheCommittedDataSet(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, write(dir))
	for _, name := range []string{"events.csv", "contexts.csv", "labels.jsonl", "policy.yaml", "knowledge.jsonl"} {
		want, err := os.ReadFile(filepath.Join("..", "evidence", "file", "testdata", name))
		require.NoError(t, err)
		got, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), name)
	}
}

func TestWriteFails(t *testing.T) {
	type args struct {
		// Output directory under the temp directory
		sub string
		// Output paths taken by a directory
		blocked []string
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"missing output directory fails", args{sub: "missing"}, os.ErrNotExist},
		{"blocked events file fails", args{blocked: []string{"events.csv"}}, syscall.EISDIR},
		{"blocked contexts file fails", args{blocked: []string{"contexts.csv"}}, syscall.EISDIR},
		{"blocked labels file fails", args{blocked: []string{"labels.jsonl"}}, syscall.EISDIR},
		{"blocked policy file fails", args{blocked: []string{"policy.yaml"}}, syscall.EISDIR},
		{"blocked knowledge file fails", args{blocked: []string{"knowledge.jsonl"}}, syscall.EISDIR},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, name := range tc.args.blocked {
				require.NoError(t, os.Mkdir(filepath.Join(dir, name), 0o755))
			}
			assert.ErrorIs(t, write(filepath.Join(dir, tc.args.sub)), tc.want)
		})
	}
}
