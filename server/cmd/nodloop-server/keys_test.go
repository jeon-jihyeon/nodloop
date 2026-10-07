package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/userconfig"
)

func TestRunKey(t *testing.T) {
	type args struct {
		setup [][]string
		args  []string
	}
	type want struct {
		code int
		// stdout with {key} for a printed key
		stdout string
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a new key is printed once", args{nil, []string{"key", "add", "ann", "--tenant", "acme", "--role", "approver"}}, want{0, "{key}\n", ""}},
		{"keys list without their secret", args{[][]string{{"key", "add", "ann", "--tenant", "acme", "--role", "approver"}}, []string{"key", "list"}},
			want{0, "ann\tacme\tapprover\n", ""}},
		{"a removed key leaves the list", args{[][]string{{"key", "add", "ann", "--tenant", "acme", "--role", "approver"}, {"key", "remove", "ann"}}, []string{"key", "list"}},
			want{0, "", ""}},
		{"a name is used once", args{[][]string{{"key", "add", "ann", "--tenant", "acme", "--role", "approver"}}, []string{"key", "add", "ann", "--tenant", "acme", "--role", "producer"}},
			want{1, "", "nodloop-server key: a server key of that name exists: ann"}},
		{"a tenant is one path element", args{nil, []string{"key", "add", "ann", "--tenant", "../etc", "--role", "approver"}}, want{1, "", "nodloop-server key: invalid tenant"}},
		{"an unknown role is refused", args{nil, []string{"key", "add", "ann", "--tenant", "acme", "--role", "admin"}}, want{1, "", "nodloop-server key: invalid role"}},
		{"an unknown key is not removed", args{nil, []string{"key", "remove", "ann"}}, want{1, "", "nodloop-server key: no server key of that name: ann"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
			for _, args := range tc.args.setup {
				var stderr bytes.Buffer
				require.Equal(t, 0, run(args, getenv, time.Now, &bytes.Buffer{}, &stderr), stderr.String())
			}
			var stdout, stderr bytes.Buffer

			code := run(tc.args.args, getenv, time.Now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code)
			key := strings.TrimSpace(stdout.String())
			assert.Equal(t, strings.ReplaceAll(tc.want.stdout, "{key}", key), stdout.String())
			assert.True(t, strings.HasPrefix(stderr.String(), tc.want.stderr), stderr.String())
		})
	}
}

// config.json keeps the hash of a new key and never the key it printed
func TestServerKeySavedAsHash(t *testing.T) {
	home := userconfig.Home(t.TempDir())
	getenv := func(k string) string { return map[string]string{"HOME": string(home)}[k] }
	var stdout bytes.Buffer
	require.Equal(t, 0, run([]string{"key", "add", "ann", "--tenant", "acme", "--role", "approver"}, getenv, time.Now, &stdout, &bytes.Buffer{}))
	key := strings.TrimSpace(stdout.String())

	var uc fileConfig
	err := home.Read(&uc)

	require.NoError(t, err)
	sum := sha256.Sum256([]byte(key))
	assert.True(t, strings.HasPrefix(key, "nl_"))
	assert.Equal(t, []serverKey{{Name: "ann", Tenant: "acme", Role: mcp.RoleApprover, SHA256: hex.EncodeToString(sum[:])}}, uc.Server.Keys)
	saved, err := os.ReadFile(home.ConfigPath())
	require.NoError(t, err)
	assert.NotContains(t, string(saved), key)
}

// Keys added at once all land since each add reads and writes under the config lock
func TestRunKeyAddConcurrent(t *testing.T) {
	home := t.TempDir()
	getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			assert.Equal(t, 0, run([]string{"key", "add", fmt.Sprintf("k%d", i), "--tenant", "acme", "--role", "producer"}, getenv, time.Now, io.Discard, io.Discard))
		})
	}
	wg.Wait()
	var stdout bytes.Buffer

	require.Equal(t, 0, run([]string{"key", "list"}, getenv, time.Now, &stdout, io.Discard))

	assert.Len(t, strings.Split(strings.TrimSpace(stdout.String()), "\n"), 10)
}
