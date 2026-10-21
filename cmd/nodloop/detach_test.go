package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A detached pass reads the model of config.json whatever model the env of the hook names
func TestPassEnv(t *testing.T) {
	tcs := []struct {
		name string
		args []string
		want []string
	}{
		{
			"the model is dropped and the rest is kept",
			[]string{"HOME=/home/ann", envLLMModel + "=haiku", envRecordDir + "=/records", envClaudeBin + "=/bin/claude"},
			[]string{"HOME=/home/ann", envRecordDir + "=/records", envClaudeBin + "=/bin/claude"},
		},
		{"an empty model is dropped too", []string{envLLMModel + "=", "PATH=/bin"}, []string{"PATH=/bin"}},
		{"a name that only starts like it is kept", []string{envLLMModel + "_X=haiku"}, []string{envLLMModel + "_X=haiku"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, passEnv(tc.args))
		})
	}
}
