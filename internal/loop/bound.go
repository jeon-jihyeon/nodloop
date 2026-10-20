package loop

import (
	"maps"
	"math"
	"slices"
)

// The Beta prior of the differ rate of a kind and bin
// Its mean is the target of 0.10 with the weight of ten answers
const (
	priorDiffer = 1
	priorSame   = 9
)

// The p below which the judge reads as shifted between two cycles of one configuration
const ShiftThreshold = 0.01

// The natural log of the binomial probability of k in n at p for p strictly between 0 and 1
func logBinomial(k, n int, p float64) float64 {
	return lfact(n) - lfact(k) - lfact(n-k) + float64(k)*math.Log(p) + float64(n-k)*math.Log1p(-p)
}

func lfact(n int) float64 {
	v, _ := math.Lgamma(float64(n) + 1)
	return v
}

// k differ among n answers as the anytime valid bound reads them
type answerCount struct {
	n, k int
}

// The mixture e-value of the answers against the differ rate p0
// 1. E = (1 / p0) ∫₀^p0 p^k (1 − p)^(n − k) dp over p0^k (1 − p0)^(n − k)
// 2. with whole counts the integral is a binomial tail so E = P(Bin(n + 1, p0) ≥ k + 1) / ((n + 1) p0 P(Bin(n, p0) = k))
// 3. each term is summed in its ratio to the denominator so no factorial overflows before the ratio does
func (a answerCount) e(p0 float64) float64 {
	base := logBinomial(a.k, a.n, p0) + math.Log(float64(a.n+1)*p0)
	sum := 0.0
	for j := a.k + 1; j <= a.n+1; j++ {
		sum += math.Exp(logBinomial(j, a.n+1, p0) - base)
	}
	return min(sum, math.MaxFloat64)
}

// Bisections of the bound to a width far below the three decimals a report prints
const bisections = 60

// The smallest differ rate whose e-value reaches 1 / alpha so the rate of the answers stays below it at any time it is read
// 1 when no rate below 1 is reached
// So no answer and answers that all differ bound nothing
func (a answerCount) bound(alpha float64) float64 {
	low, high := 0.0, 1.0
	for range bisections {
		mid := (low + high) / 2
		if a.e(mid) >= 1/alpha {
			high = mid
		} else {
			low = mid
		}
	}
	return high
}

// The mean of the Beta(1 + k, 9 + n − k) posterior of the differ rate
func (a answerCount) mean() float64 {
	return float64(priorDiffer+a.k) / float64(priorDiffer+priorSame+a.n)
}

// The probability the posterior puts above the rate t
// With whole parameters it is P(Bin(n + 9, t) ≤ k)
func (a answerCount) above(t float64) float64 {
	sum := 0.0
	for j := 0; j <= a.k+priorDiffer-1; j++ {
		sum += math.Exp(logBinomial(j, a.n+priorDiffer+priorSame-1, t))
	}
	return min(1, sum)
}

// The upper tail of the chi squared distribution of df degrees of freedom at x for df of 1 or more
// 1. an even df is a Poisson sum
// 2. an odd df is the normal tail and a sum over half integers
func chiSquaredP(x float64, df int) float64 {
	if x <= 0 {
		return 1
	}
	half := x / 2
	if df%2 == 0 {
		term, sum := 1.0, 1.0
		for i := 1; i < df/2; i++ {
			term *= half / float64(i)
			sum += term
		}
		return min(1, math.Exp(-half)*sum)
	}
	term, sum := math.Sqrt(half)/math.Gamma(1.5), 0.0
	for i := 1; i <= (df-1)/2; i++ {
		sum += term
		term *= half / (float64(i) + 0.5)
	}
	return min(1, math.Erfc(math.Sqrt(half))+math.Exp(-half)*sum)
}

// Counts of one sample by category
type categories map[string]int

func (c categories) total() int {
	n := 0
	for _, v := range c {
		n += v
	}
	return n
}

// The chi squared p of the two samples sharing one distribution over their categories
// With fewer than two categories both samples read the same
func (c categories) homogeneity(other categories) float64 {
	keys := slices.Sorted(maps.Keys(c))
	for k := range other {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	n, m := float64(c.total()), float64(other.total())
	if len(keys) < 2 || n == 0 || m == 0 {
		return 1
	}
	chi := 0.0
	for _, k := range keys {
		col := float64(c[k] + other[k])
		for _, cell := range [][2]float64{{float64(c[k]), n}, {float64(other[k]), m}} {
			expected := cell[1] * col / (n + m)
			chi += (cell[0] - expected) * (cell[0] - expected) / expected
		}
	}
	return chiSquaredP(chi, len(keys)-1)
}

// Units of a Horvitz Thompson sample weighed by one over the probability each was drawn with
type sample struct {
	answered, differ, noAnswer int
	// Sums over the answered units of the weights, the weights of the differ and the squared weights
	w, wd, w2 float64
	// The weights of the units that were drawn and never answered
	missing float64
}

func (s *sample) add(p float64, answered, differ bool) {
	w := 1 / p
	switch {
	case !answered:
		s.noAnswer++
		s.missing += w
		return
	case differ:
		s.differ++
		s.wd += w
	}
	s.answered++
	s.w += w
	s.w2 += w * w
}

// The Hájek ratio with its 95 percent Wilson interval at the Kish effective size and nils without an answered unit
func (s sample) hajek() (interval, *float64) {
	if s.answered == 0 {
		return interval{}, nil
	}
	kish := s.w * s.w / s.w2
	rate, low, high := effective{x: s.wd / s.w * kish, n: kish}.wilson()
	return interval{rate, low, high}, ptr(kish)
}

// The Hájek ratio with every unit drawn and never answered counted as differ
// A sample holds one unit at least since a unit makes it
func (s sample) worst() *float64 {
	return ptr((s.wd + s.missing) / (s.w + s.missing))
}
