package curate_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
)

func TestEventDecideTriage(t *testing.T) {
	type args struct {
		kind    curate.ChangeKind
		version int
		tier    int
		action  curate.Action
		rule    curate.RuleCode
		hash    string
		triage  *curate.Triage
	}

	good := curate.Triage{Triage: classify.Triage{
		Policy: classify.TriagePolicy, Classify: "c1", Nature: classify.NaturePreference,
		Reversibility: classify.ReversibilityH, Source: classify.SourceComplete, Protected: classify.ProtectionNone,
		Lasting: true, Faithful: true,
	}}
	changed := func(change func(*curate.Triage)) *curate.Triage {
		copy := good
		change(&copy)
		return &copy
	}
	add, apply, hold := curate.ChangeAdd, curate.ActionApply, curate.ActionHold
	delegated, protected := curate.RuleDelegated, curate.RuleTriageProtected
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a held candidate records its labels", args{add, 1, 2, hold, protected, "", &good}, nil},
		{"a held candidate needs no classifier output", args{add, 1, 2, hold, protected, "", nil}, nil},
		{"a hold never belongs to the automatic tier", args{add, 1, 1, hold, protected, "", nil}, curate.ErrDecideInvalid},
		{"a delegated new answer preference", args{add, 1, 1, apply, delegated, hmac, &good}, nil},
		{"a delegated update is refused", args{curate.ChangeUpdate, 1, 1, apply, delegated, hmac, &good},
			curate.ErrDecideInvalid},
		{"a delegated later version is refused", args{add, 2, 1, apply, delegated, hmac, &good}, curate.ErrDecideInvalid},
		{"a delegation needs its cap hash", args{add, 1, 1, apply, delegated, "", &good}, curate.ErrDecideInvalid},
		{"a delegation needs its rule", args{add, 1, 1, apply, protected, hmac, &good}, curate.ErrDecideInvalid},
		{"a delegation needs its labels", args{add, 1, 1, apply, delegated, hmac, nil}, curate.ErrDecideInvalid},
		{"a delegation cannot hold", args{add, 1, 2, hold, delegated, hmac, &good}, curate.ErrDecideInvalid},
		{"a mixed candidate is refused", args{add, 1, 1, apply, delegated, hmac,
			changed(func(v *curate.Triage) { v.Mixed = true })}, curate.ErrDecideInvalid},
		{"a safety candidate is refused", args{add, 1, 1, apply, delegated, hmac,
			changed(func(v *curate.Triage) { v.Nature = classify.NatureSafety })}, curate.ErrDecideInvalid},
		{"a stored artifact is refused", args{add, 1, 1, apply, delegated, hmac,
			changed(func(v *curate.Triage) { v.Reversibility = classify.ReversibilityM })}, curate.ErrDecideInvalid},
		{"an incomplete correction is refused", args{add, 1, 1, apply, delegated, hmac,
			changed(func(v *curate.Triage) { v.Source = classify.SourceIncomplete })}, curate.ErrDecideInvalid},
		{"a protected candidate is refused", args{add, 1, 1, apply, delegated, hmac,
			changed(func(v *curate.Triage) { v.Protected = classify.ProtectionConflict })}, curate.ErrDecideInvalid},
		{"a one time correction is refused", args{add, 1, 1, apply, delegated, hmac,
			changed(func(v *curate.Triage) { v.Lasting = false })}, curate.ErrDecideInvalid},
		{"a stronger lesson is refused", args{add, 1, 1, apply, delegated, hmac,
			changed(func(v *curate.Triage) { v.Faithful = false })}, curate.ErrDecideInvalid},
		{"a conflicting lesson is refused", args{add, 1, 1, apply, delegated, hmac,
			changed(func(v *curate.Triage) { v.Conflict = true })}, curate.ErrDecideInvalid},
		{"a built in critic needs no separate classifier record", args{add, 1, 1, apply, delegated, hmac,
			changed(func(v *curate.Triage) { v.Classify = "" })}, nil},
		{"an unknown policy is refused", args{add, 1, 2, hold, protected, "",
			changed(func(v *curate.Triage) { v.Policy = "other" })}, curate.ErrDecideInvalid},
		{"an unknown nature is refused", args{add, 1, 2, hold, protected, "",
			changed(func(v *curate.Triage) { v.Nature = "other" })}, curate.ErrDecideInvalid},
		{"an unknown reversibility is refused", args{add, 1, 2, hold, protected, "",
			changed(func(v *curate.Triage) { v.Reversibility = "other" })}, curate.ErrDecideInvalid},
		{"an unknown source is refused", args{add, 1, 2, hold, protected, "",
			changed(func(v *curate.Triage) { v.Source = "other" })}, curate.ErrDecideInvalid},
		{"an unknown protection is refused", args{add, 1, 2, hold, protected, "",
			changed(func(v *curate.Triage) { v.Protected = "other" })}, curate.ErrDecideInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := curate.Change{Kind: tc.args.kind, ID: "k1", Version: tc.args.version, Anchor: 1}
			d := curate.Decision{Tier: tc.args.tier, Action: tc.args.action, RuleCode: tc.args.rule,
				Delegation: tc.args.hash, Triage: tc.args.triage}

			got, err := decided("d1", 0).Decide(c, d, nil, at)

			assert.ErrorIs(t, err, tc.want)
			var decoded curate.Decision
			_ = json.Unmarshal(got.Output, &decoded)
			assert.Equal(t, err == nil, decoded.Action == d.Action)
		})
	}
}
