package knowledge_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

func TestNewRunInput(t *testing.T) {
	gitC := []knowledge.Ref{{ID: "git-c", Version: 1}}
	type args struct {
		applied, withheld []knowledge.Ref
		plugin            string
	}
	type want struct {
		json string
		err  error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"nothing applied is stored as an empty list", args{nil, nil, ""}, want{`{"applied":[]}`, nil}},
		{"applied items with the plugin", args{gitC, nil, "0.9.0"}, want{`{"applied":[{"id":"git-c","version":1}],"plugin":"0.9.0"}`, nil}},
		{"withheld items leave applied empty", args{nil, gitC, ""}, want{`{"applied":[],"withheld":[{"id":"git-c","version":1}]}`, nil}},
		{"applied and withheld together are refused", args{gitC, gitC, ""}, want{`{"applied":null}`, knowledge.ErrWithheldApplied}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in, err := knowledge.NewRunInput(tc.args.applied, tc.args.withheld, tc.args.plugin)
			got, _ := json.Marshal(in)
			assert.ErrorIs(t, err, tc.want.err)
			assert.JSONEq(t, tc.want.json, string(got))
		})
	}
}

func TestRunInputWithDraw(t *testing.T) {
	gitC := []knowledge.Ref{{ID: "git-c", Version: 1}}
	type args struct {
		applied, withheld []knowledge.Ref
		share             knowledge.Holdout
		drawWithheld      bool
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"an applied draw records the share, the scheme and the unit", args{gitC, nil, 0.1, false}, `{"applied":[{"id":"git-c","version":1}],"drawn":true,"share":0.1,"scheme":1,"unit":"u1"}`},
		{"a withheld draw records the share", args{nil, gitC, 0.1, true}, `{"applied":[],"withheld":[{"id":"git-c","version":1}],"drawn":true,"share":0.1,"scheme":1,"unit":"u1"}`},
		{"a draw without items is drawn", args{nil, nil, 0.1, false}, `{"applied":[],"drawn":true,"share":0.1,"scheme":1,"unit":"u1"}`},
		{"a share of 0 draws nothing", args{gitC, nil, 0, false}, `{"applied":[{"id":"git-c","version":1}]}`},
		{"applied items on a withheld draw stay undrawn", args{gitC, nil, 0.1, true}, `{"applied":[{"id":"git-c","version":1}]}`},
		{"withheld items on an applied draw stay undrawn", args{nil, gitC, 0.1, false}, `{"applied":[],"withheld":[{"id":"git-c","version":1}]}`},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in, err := knowledge.NewRunInput(tc.args.applied, tc.args.withheld, "")
			assert.NoError(t, err)
			got, _ := json.Marshal(in.WithDraw(tc.args.share, tc.args.drawWithheld, "u1"))
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}
