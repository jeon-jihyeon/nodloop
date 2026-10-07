package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunDoctor(t *testing.T) {
	type args struct {
		feedback string
		repair   bool
	}
	type want struct {
		code   int
		stdout string
		stderr string
		// feedback.jsonl after the call
		feedback string
	}
	good := "{\"trace_id\":\"t1\",\"time\":\"2026-10-07T00:00:00Z\",\"verdict\":\"approve\",\"reviewer\":\"ann\",\"audit\":false}\n"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"clean records pass",
			args{good, false},
			want{0, "traces.jsonl\t0 records\tok\nfeedback.jsonl\t1 records\tok\noutcomes.jsonl\t0 records\tok\nknowledge.jsonl\t0 records\tok\n", "", good},
		},
		{
			"a corrupt line is named and fails",
			args{good + "not json\n", false},
			want{1, "feedback.jsonl\t1 records\tfeedback.jsonl has corrupt lines: line 2: invalid", "1 files hold corrupt lines", good + "not json\n"},
		},
		{
			"repair moves the line aside",
			args{good + "not json\n", true},
			want{0, "feedback.jsonl\t1 records\tmoved 1 corrupt lines to feedback.jsonl.corrupt\n", "", good},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(records, "feedback.jsonl"), []byte(tc.args.feedback), 0o600))
			args := []string{"--record-dir", records}
			if tc.args.repair {
				args = append(args, "--repair")
			}
			var stdout, stderr bytes.Buffer

			code := runDoctor(args, func(string) string { return "" }, time.Now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Contains(t, stdout.String(), tc.want.stdout)
			assert.Contains(t, stderr.String(), tc.want.stderr)
			got, err := os.ReadFile(filepath.Join(records, "feedback.jsonl"))
			require.NoError(t, err)
			assert.Equal(t, tc.want.feedback, string(got))
		})
	}
}

// The guard log under home is checked beside the record files
func TestRunDoctorGuardLog(t *testing.T) {
	type want struct {
		code   int
		stdout string
		stderr string
	}
	tcs := []struct {
		name string
		// guard.jsonl under home and nothing written when empty
		args string
		want want
	}{
		{"a missing guard log reads as zero records", "", want{0, "guard.jsonl\t0 records\tok\n", ""}},
		{
			"a corrupt guard log is named and fails",
			"not json\n",
			want{1, "guard.jsonl\t0 records\tguard.jsonl has corrupt lines: line 1: invalid", "1 files hold corrupt lines"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(home, ".nodloop"), 0o700))
			if tc.args != "" {
				require.NoError(t, os.WriteFile(filepath.Join(home, ".nodloop", "guard.jsonl"), []byte(tc.args), 0o600))
			}
			getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
			var stdout, stderr bytes.Buffer

			code := runDoctor([]string{"--record-dir", t.TempDir()}, getenv, time.Now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Contains(t, stdout.String(), tc.want.stdout)
			assert.Contains(t, stderr.String(), tc.want.stderr)
		})
	}
}
