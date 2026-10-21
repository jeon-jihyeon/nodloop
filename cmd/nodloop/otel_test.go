package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// otel set saves the target once and otel prints it without a header value, unset removes it
func TestRunOTel(t *testing.T) {
	type args struct {
		// config.json before the call and none when empty
		saved string
		args  []string
		// The command runs without HOME
		homeless bool
	}
	type want struct {
		code   int
		stdout string
		stderr string
		// config.json after the call and none when empty
		saved string
	}
	broken := "{broken"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"set saves the base URL and prints both endpoints", args{"", []string{"set", "--endpoint", "http://127.0.0.1:4318/"}, false},
			want{0, "otel\thttp/protobuf\thttp://127.0.0.1:4318/v1/traces\thttp://127.0.0.1:4318/v1/logs\n", "",
				"{\n  \"otel\": {\n    \"endpoint\": \"http://127.0.0.1:4318/\"\n  }\n}\n"}},
		{
			"set keeps the name of the header variable and never a value",
			args{`{"approver":"ann"}`, []string{"set", "--endpoint", "https://example.com/api/public/otel", "--headers-env", "ACME_OTLP", "--no-logs", "--content", "--protocol", "http/json"}, false},
			want{0, "otel\thttp/json\thttps://example.com/api/public/otel/v1/traces\theaders $ACME_OTLP\tcontent\n", "",
				"{\n  \"approver\": \"ann\",\n  \"otel\": {\n    \"endpoint\": \"https://example.com/api/public/otel\",\n    \"protocol\": \"http/json\",\n" +
					"    \"headers_env\": \"ACME_OTLP\",\n    \"no_logs\": true,\n    \"content\": true\n  }\n}\n"},
		},
		{"set without an endpoint fails", args{"", []string{"set"}, false}, want{1, "", "--endpoint is required", ""}},
		{"a user in the endpoint fails", args{"", []string{"set", "--endpoint", "https://ann:k1@example.com"}, false}, want{1, "", "credentials in the variable of --headers-env", ""}},
		{"a token in the query of the endpoint fails", args{"", []string{"set", "--endpoint", "https://example.com/otel?token=k1"}, false},
			want{1, "", "credentials in the variable of --headers-env", ""}},
		{"a fragment in the endpoint fails", args{"", []string{"set", "--endpoint", "https://example.com/otel#k1"}, false},
			want{1, "", "credentials in the variable of --headers-env", ""}},
		{"an endpoint that is not http fails", args{"", []string{"set", "--endpoint", "grpc://127.0.0.1:4317"}, false}, want{1, "", "is not an http or https URL", ""}},
		{"an endpoint that does not parse fails", args{"", []string{"set", "--endpoint", "http://[::1"}, false}, want{1, "", errOTelEndpoint.Error(), ""}},
		{"an unknown protocol fails", args{"", []string{"set", "--endpoint", "http://127.0.0.1:4318", "--protocol", "grpc"}, false}, want{1, "", `unknown otel protocol "grpc"`, ""}},
		{"an unknown flag fails", args{"", []string{"set", "--nope"}, false}, want{1, "", "flag provided but not defined: -nope", ""}},
		{"a config that does not decode keeps set from saving", args{broken, []string{"set", "--endpoint", "http://127.0.0.1:4318"}, false},
			want{1, "", "config.json is not valid JSON", broken}},
		{"nothing saved prints none", args{"", nil, false}, want{0, "otel\tnone\n", "", ""}},
		{"the saved target prints", args{`{"otel":{"endpoint":"http://127.0.0.1:4318","no_logs":true}}`, nil, false},
			want{0, "otel\thttp/protobuf\thttp://127.0.0.1:4318/v1/traces\n", "", `{"otel":{"endpoint":"http://127.0.0.1:4318","no_logs":true}}`}},
		{"a config that does not decode prints nothing", args{broken, nil, false}, want{1, "", "config.json is not valid JSON", broken}},
		{"unset removes the key and keeps the others", args{`{"approver":"ann","otel":{"endpoint":"http://127.0.0.1:4318"}}`, []string{"unset"}, false},
			want{0, "otel\tnone\n", "", "{\n  \"approver\": \"ann\"\n}\n"}},
		{"a config that does not decode keeps unset from saving", args{broken, []string{"unset"}, false}, want{1, "", "config.json is not valid JSON", broken}},
		{"an unknown action fails", args{"", []string{"send"}, false}, want{1, "", `unknown action "send"`, ""}},
		{"no home fails", args{"", nil, true}, want{1, "", errHomeUnknown.Error(), ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			hookSaveConfig(t, home, tc.args.saved)
			env := map[string]string{"HOME": home}
			if tc.args.homeless {
				env = nil
			}
			var stdout, stderr bytes.Buffer

			code := runOTel(tc.args.args, envOf(env), atTerminal, &stdout, &stderr)

			// A missing config.json reads as none
			saved, _ := os.ReadFile(homeDir(home).config().ConfigPath())
			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.Equal(t, tc.want.saved, string(saved))
			assert.NotContains(t, stderr.String(), "k1")
		})
	}
}
