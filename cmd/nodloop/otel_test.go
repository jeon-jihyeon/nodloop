package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// otel set saves the target once and otel prints it without a header value, unset removes it
func TestRunOTel(t *testing.T) {
	type args struct {
		// config.json before the call and none when empty
		saved string
		args  []string
	}
	type want struct {
		code   int
		stdout string
		stderr string
		// config.json after the call and unchanged when empty
		saved string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"set saves the base URL and prints both endpoints", args{"", []string{"set", "--endpoint", "http://127.0.0.1:4318/"}},
			want{0, "otel\thttp/protobuf\thttp://127.0.0.1:4318/v1/traces\thttp://127.0.0.1:4318/v1/logs\n", "",
				"{\n  \"otel\": {\n    \"endpoint\": \"http://127.0.0.1:4318/\"\n  }\n}\n"}},
		{
			"set keeps the name of the header variable and never a value",
			args{`{"approver":"ann"}`, []string{"set", "--endpoint", "https://example.com/api/public/otel", "--headers-env", "ACME_OTLP", "--no-logs", "--content", "--protocol", "http/json"}},
			want{0, "otel\thttp/json\thttps://example.com/api/public/otel/v1/traces\theaders $ACME_OTLP\tcontent\n", "",
				"{\n  \"approver\": \"ann\",\n  \"otel\": {\n    \"endpoint\": \"https://example.com/api/public/otel\",\n    \"protocol\": \"http/json\",\n" +
					"    \"headers_env\": \"ACME_OTLP\",\n    \"no_logs\": true,\n    \"content\": true\n  }\n}\n"},
		},
		{"set without an endpoint fails", args{"", []string{"set"}}, want{1, "", "--endpoint is required", ""}},
		{"a user in the endpoint fails", args{"", []string{"set", "--endpoint", "https://ann:k1@example.com"}}, want{1, "", "credentials in the variable of --headers-env", ""}},
		{"an endpoint that is not http fails", args{"", []string{"set", "--endpoint", "grpc://127.0.0.1:4317"}}, want{1, "", "is not an http or https URL", ""}},
		{"an unknown protocol fails", args{"", []string{"set", "--endpoint", "http://127.0.0.1:4318", "--protocol", "grpc"}}, want{1, "", `unknown otel protocol "grpc"`, ""}},
		{"nothing saved prints none", args{"", nil}, want{0, "otel\tnone\n", "", ""}},
		{"the saved target prints", args{`{"otel":{"endpoint":"http://127.0.0.1:4318","no_logs":true}}`, nil},
			want{0, "otel\thttp/protobuf\thttp://127.0.0.1:4318/v1/traces\n", "", ""}},
		{"unset removes the key and keeps the others", args{`{"approver":"ann","otel":{"endpoint":"http://127.0.0.1:4318"}}`, []string{"unset"}},
			want{0, "otel\tnone\n", "", "{\n  \"approver\": \"ann\"\n}\n"}},
		{"an unknown action fails", args{"", []string{"send"}}, want{1, "", `unknown action "send"`, ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			hookSaveConfig(t, home, tc.args.saved)
			getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
			var stdout, stderr bytes.Buffer

			code := runOTel(tc.args.args, getenv, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.NotContains(t, stderr.String(), "k1")
			if tc.want.saved == "" {
				return
			}
			b, err := os.ReadFile(homeDir(home).config().ConfigPath())
			require.NoError(t, err)
			assert.Equal(t, tc.want.saved, string(b))
		})
	}
}
