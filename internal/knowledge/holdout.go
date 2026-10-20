package knowledge

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
)

// The share of runs that receive no item so report effect can compare runs with and without them
// One draw for every producer so the arms of every producer come from the same rule
type Holdout float64

// From 0 up to but not including 1 since a share of 1 leaves no run to apply the items
func (h Holdout) Check() error {
	if h >= 0 && h < 1 {
		return nil
	}
	return fmt.Errorf("%w: %v. Give a share from 0 up to but not including 1, such as 0.1", ErrHoldoutInvalid, float64(h))
}

// Whether the run of the key withholds every item
// 1. the draw bytes of the key read as a fraction of the uint64 range fall below the share
// 2. an empty key or a share of 0 never withholds
// 3. a producer that draws twice for one run passes the same key both times so both draws agree
func (h Holdout) Withholds(key string) bool {
	if h <= 0 || key == "" {
		return false
	}
	return drawFraction(key) < float64(h)
}

// The first 8 bytes of SHA-256 of the key
// SHA-256 because the high bits of FNV barely move between similar keys
func drawBytes(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:8]
}

// The draw bytes of the key read as a fraction of the uint64 range
func drawFraction(key string) float64 {
	return float64(binary.BigEndian.Uint64(drawBytes(key))) / math.MaxUint64
}

// Whether the run of the producer withholds every item
// The key is what the producer keeps for one run such as a window id and an empty one opts out
func (h Holdout) WithholdsRun(producer, key string) bool {
	if key == "" {
		return false
	}
	return h.Withholds(producer + ":" + key)
}

// The input of a run of the producer that names the holdout key it drew with
// 1. the draw is taken again from the key so a producer passes back only the key it already holds
// 2. an empty key went through no draw and the input stays undrawn
// 3. the unit is the hex of the draw bytes so every run of one key shares it and the stored unit never holds the key
func (h Holdout) Drawn(in RunInput, producer, key string) RunInput {
	if key == "" {
		return in
	}
	drawKey := producer + ":" + key
	return in.WithDraw(h, h.Withholds(drawKey), hex.EncodeToString(drawBytes(drawKey)))
}
