package curate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Public passes cannot construct these inconsistent states
// The direct checks protect the ledger boundary from a malformed internal ruling
func TestDelegatedRejectsMalformedRuling(t *testing.T) {
	scope := knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}}}
	k := knowledge.Knowledge{ID: "k1", Version: 1, Run: &scope, Status: knowledge.StatusCandidate}
	noScope := k
	noScope.Run = nil
	good := &Triage{Triage: classify.Triage{Policy: classify.TriagePolicy, Nature: classify.NaturePreference,
		Reversibility: classify.ReversibilityH, Source: classify.SourceComplete, Protected: classify.ProtectionNone,
		Lasting: true, Faithful: true}}
	mixed := *good
	mixed.Mixed = true
	type args struct {
		all    knowledge.Set
		triage *Triage
		grants []Delegation
	}
	grants := []Delegation{{ID: "g1", Scope: scope}}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a missing version", args{nil, good, grants}, knowledge.ErrNotFound},
		{"a missing scope", args{knowledge.Set{noScope}, good, grants}, ErrDecideInvalid},
		{"missing triage", args{knowledge.Set{k}, nil, grants}, ErrDecideInvalid},
		{"ineligible triage", args{knowledge.Set{k}, &mixed, grants}, ErrDecideInvalid},
		{"a removed grant", args{knowledge.Set{k}, good, nil}, ErrDecideInvalid},
		{"a changed grant hash", args{knowledge.Set{k}, good, grants}, ErrDecideInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stores := testkit.Open(t)
			seal := func(text string) string {
				sum := sha256.Sum256([]byte(text))
				return hex.EncodeToString(sum[:])
			}
			p := pass{Curator: &Curator{ledger: stores.Ledger, seal: seal}, all: tc.args.all,
				s: Setting{Delegations: tc.args.grants}}
			d := Decided{ID: "d1", Change: Change{Kind: ChangeAdd, ID: "k1", Version: 1},
				Decision: Decision{RuleCode: RuleDelegated, Triage: tc.args.triage, Delegation: strings.Repeat("f", 64)}}

			err := p.delegated(context.Background(), d)

			assert.ErrorIs(t, err, tc.want)
			all, readErr := stores.Ledger.All(context.Background())
			require.NoError(t, readErr)
			assert.Empty(t, all)
		})
	}
}
