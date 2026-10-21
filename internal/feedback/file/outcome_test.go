package file_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
)

func TestNewOutcomeStore(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "plain"), nil, 0o600))
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"missing directory fails", "missing", file.ErrOpen},
		{"plain file in place of the directory fails", "plain", file.ErrOpen},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := file.NewOutcomeStore(filepath.Join(root, tc.args))
			assert.ErrorIs(t, err, tc.want)
			assert.Nil(t, got)
		})
	}
}

func TestOutcomeStoreAppend(t *testing.T) {
	tcs := []struct {
		name string
		// Created as a directory inside the store directory before the append
		args string
		want error
	}{
		{"append writes the record", "", nil},
		{"directory in place of the file fails", "outcomes.jsonl", file.ErrAppend},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, tc.args), 0o700))
			store, err := file.NewOutcomeStore(dir)
			require.NoError(t, err)
			assert.ErrorIs(t, store.Append(ctx, record.Outcome{TraceID: "a"}), tc.want)
		})
	}
}

func TestOutcomeStoreList(t *testing.T) {
	base := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	refuted := record.Outcome{TraceID: "t1", Time: base, Result: "refuted", Reviewer: "author"}
	tracking := record.Outcome{
		TraceID: "t2", Time: base.Add(time.Minute), Result: "confirmed", ConfirmedCause: "tracking change",
		Reviewer: "author",
	}
	campaign := record.Outcome{
		TraceID: "t1", Time: base.Add(2 * time.Minute), Result: "confirmed", ConfirmedCause: "campaign",
		Note: "checked", Reviewer: "sam",
	}
	type args struct {
		// Created as a directory inside the store directory before any append
		dir      string
		outcomes []record.Outcome
		traceID  string
	}
	type want struct {
		outcomes []record.Outcome
		err      error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"missing file lists nothing", args{}, want{}},
		{
			"empty trace id lists all newest first",
			args{outcomes: []record.Outcome{refuted, tracking, campaign}},
			want{outcomes: []record.Outcome{campaign, tracking, refuted}},
		},
		{
			"trace id keeps that trace newest first",
			args{outcomes: []record.Outcome{refuted, tracking, campaign}, traceID: "t1"},
			want{outcomes: []record.Outcome{campaign, refuted}},
		},
		{"unknown trace id lists nothing", args{outcomes: []record.Outcome{refuted}, traceID: "t9"}, want{}},
		{"directory in place of the file fails", args{dir: "outcomes.jsonl"}, want{err: file.ErrRead}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, tc.args.dir), 0o700))
			store, err := file.NewOutcomeStore(dir)
			require.NoError(t, err)
			for _, o := range tc.args.outcomes {
				require.NoError(t, store.Append(ctx, o))
			}
			got, err := store.List(ctx, tc.args.traceID)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.outcomes, got)
		})
	}
}

// Outcomes read in append order from the position the read before answered
func TestOutcomeStoreAfter(t *testing.T) {
	base := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	outcomes := []record.Outcome{
		{TraceID: "t1", Time: base, Result: "refuted", Reviewer: "author"},
		{TraceID: "t2", Time: base.Add(time.Minute), Result: "confirmed", Reviewer: "author"},
		{TraceID: "t1", Time: base.Add(-time.Minute), Result: "confirmed", Reviewer: "sam"},
	}
	ctx := context.Background()
	store, err := file.NewOutcomeStore(t.TempDir())
	require.NoError(t, err)
	for _, o := range outcomes {
		require.NoError(t, store.Append(ctx, o))
	}

	first, at, err := store.After(ctx, "", 1)
	require.NoError(t, err)
	rest, end, err := store.After(ctx, at, 0)
	require.NoError(t, err)
	none, again, err := store.After(ctx, end, 0)
	require.NoError(t, err)

	assert.Equal(t, outcomes[:1], first)
	assert.Equal(t, outcomes[1:], rest, "an outcome stamped before the others still comes in its place")
	assert.Empty(t, none)
	assert.Equal(t, end, again)
}
