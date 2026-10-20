package knowledge_test

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

var (
	personRef  = knowledge.Ref{ID: "git-c", Version: 1}
	nodloopRef = knowledge.Ref{ID: "pr-c", Version: 1}
	// git-c a person approved and pr-c nodloop approved in the For order
	holdItems = knowledge.Set{
		{ID: personRef.ID, Version: personRef.Version, Status: knowledge.StatusApproved, Approver: "ann"},
		{ID: nodloopRef.ID, Version: nodloopRef.Version, Status: knowledge.StatusApproved, Approver: knowledge.ApproverNodloop},
	}
)

// Fixed draws so a change of the hash never moves a recorded session to the other side of the hold
// The fraction of each key is in the name so a reader sees why it lands on its side of each share
func TestItemHoldDraws(t *testing.T) {
	type args struct {
		producer, key string
		ref           knowledge.Ref
	}
	type want struct {
		tenth, fifth bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a session at 0.054", args{"session", "s34", nodloopRef}, want{true, true}},
		{"the next version in that session at 0.321", args{"session", "s34", knowledge.Ref{ID: "pr-c", Version: 2}}, want{false, false}},
		{"the same key of another producer at 0.253", args{"stream-bot", "s34", nodloopRef}, want{false, false}},
		{"a session at 0.137", args{"session", "s20", nodloopRef}, want{false, true}},
		{"a session at 0.160", args{"session", "s13", nodloopRef}, want{false, true}},
		{"a session at 0.932", args{"session", "s1", nodloopRef}, want{false, false}},
		{"another item at 0.001", args{"session", "s16", personRef}, want{true, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := want{
				knowledge.ItemHold(0.1).Holds(tc.args.producer, tc.args.key, tc.args.ref),
				knowledge.ItemHold(0.2).Holds(tc.args.producer, tc.args.key, tc.args.ref),
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestItemHoldHolds(t *testing.T) {
	type args struct {
		share knowledge.ItemHold
		key   string
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"no share holds nothing", args{0, "s34"}, false},
		{"an empty key is never drawn", args{0.999, ""}, false},
		{"a share near 1 holds the version", args{0.999, "s1"}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.share.Holds("session", tc.args.key, nodloopRef))
		})
	}
}

// One draw per session and item so a share of 0.1 holds about a tenth of 1000 keys
func TestItemHoldShare(t *testing.T) {
	held := 0
	for session := range 100 {
		for item := range 10 {
			if knowledge.ItemHold(0.1).Holds("session", fmt.Sprintf("s%d", session), knowledge.Ref{ID: fmt.Sprintf("k-%d", item), Version: 1}) {
				held++
			}
		}
	}
	assert.GreaterOrEqual(t, held, 70)
	assert.LessOrEqual(t, held, 130)
}

func TestItemHoldCheck(t *testing.T) {
	tcs := []struct {
		name string
		args knowledge.ItemHold
		want error
	}{
		{"0 holds nothing and passes", 0, nil},
		{"the start share passes", 0.1, nil},
		{"a share below 1 passes", 0.999, nil},
		{"1 leaves no session that includes the version", 1, knowledge.ErrItemHoldInvalid},
		{"a negative share is refused", -0.1, knowledge.ErrItemHoldInvalid},
		{"no number is refused", knowledge.ItemHold(math.NaN()), knowledge.ErrItemHoldInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, tc.args.Check(), tc.want)
		})
	}
}

// Only a version nodloop approved is held
// A version a person approved is kept even where its draw falls below the share
func TestItemHoldHeld(t *testing.T) {
	require.True(t, knowledge.ItemHold(0.1).Holds("session", "s34", personRef), "git-c draws 0.058 in s34")
	type args struct {
		share knowledge.ItemHold
		key   string
	}
	tcs := []struct {
		name string
		args args
		want []knowledge.Ref
	}{
		{"a drawn session misses the version nodloop approved alone", args{0.1, "s34"}, []knowledge.Ref{nodloopRef}},
		{"a session the draw passes misses nothing", args{0.1, "s1"}, nil},
		{"no share holds nothing", args{0, "s34"}, nil},
		{"no session holds nothing", args{0.1, ""}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.share.Held(holdItems, "session", tc.args.key))
		})
	}
}

func TestItemHoldDrawn(t *testing.T) {
	both := []knowledge.Ref{personRef, nodloopRef}
	type args struct {
		share             knowledge.ItemHold
		key               string
		applied, withheld []knowledge.Ref
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{
			"a share of 0 leaves the input as it was before the hold",
			args{0, "s34", both, nil},
			`{"applied":[{"id":"git-c","version":1},{"id":"pr-c","version":1}]}`,
		},
		{
			"no session leaves the input as it was before the hold",
			args{0.1, "", both, nil},
			`{"applied":[{"id":"git-c","version":1},{"id":"pr-c","version":1}]}`,
		},
		{
			"a drawn session takes the held version out of applied",
			args{0.1, "s34", both, nil},
			`{"applied":[{"id":"git-c","version":1}],` +
				`"eligible":[{"id":"git-c","version":1,"p_include":1},{"id":"pr-c","version":1,"p_include":0.9}],` +
				`"held":[{"id":"pr-c","version":1}]}`,
		},
		{
			"a session the draw passes keeps every version with its chance",
			args{0.1, "s1", both, nil},
			`{"applied":[{"id":"git-c","version":1},{"id":"pr-c","version":1}],` +
				`"eligible":[{"id":"git-c","version":1,"p_include":1},{"id":"pr-c","version":1,"p_include":0.9}]}`,
		},
		{
			"a withheld session includes no version and the hold keeps none from it",
			args{0.1, "s34", nil, both},
			`{"applied":[],"withheld":[{"id":"git-c","version":1},{"id":"pr-c","version":1}],` +
				`"eligible":[{"id":"git-c","version":1,"p_include":0},{"id":"pr-c","version":1,"p_include":0}]}`,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in, err := knowledge.NewRunInput(tc.args.applied, tc.args.withheld, "")
			require.NoError(t, err)

			got, err := json.Marshal(tc.args.share.Drawn(in, holdItems, "session", tc.args.key))

			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}
