package curate

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The purposes the seeded draws name
const (
	drawAudit  = "audit"
	drawShadow = "shadow"
)

// The audit draw of a decide under the seed of the cycle
// The unit is the id of the decide that curate makes as it decides so no drafter or proposer can pick an id that avoids the audit
func (k KnobEvent) AuditDraw(decide string, p float64) trace.Draw {
	return trace.NewDraw(k.CycleID, k.PolicyVersion, k.Seed, decide, drawAudit, p)
}

// One due shadow decision a stop of the session asks drawn uniformly with the cycle seed
// 1. the first 8 bytes of SHA-256 of the seed, the session and the purpose read as a fraction pick one of the due shadow questions in their order
// 2. the draw records the session as its unit and one over their number as its p_select
// 3. false when no shadow question is due
func (m Mark) ShadowPick(session string, due []Due) (Due, trace.Draw, bool) {
	var shadows []Due
	for _, d := range due {
		if d.Kind == QuestionShadow {
			shadows = append(shadows, d)
		}
	}
	if len(shadows) == 0 {
		return Due{}, trace.Draw{}, false
	}
	sum := sha256.Sum256([]byte(m.Seed + ":" + session + ":" + drawShadow))
	fraction := float64(binary.BigEndian.Uint64(sum[:8])) / math.MaxUint64
	n := len(shadows)
	p := 1 / float64(n)
	draw := trace.Draw{
		CycleID: m.CycleID, PolicyVersion: m.PolicyVersion, Unit: session, Seed: m.Seed, DrawID: hex.EncodeToString(sum[:8]), P: &p, Drawn: true,
	}
	return shadows[min(int(fraction*float64(n)), n-1)], draw, true
}
