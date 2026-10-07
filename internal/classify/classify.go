// Package classify asks the yes or no questions of a decision point of the classifiers a user set up for it
package classify

//go:generate mockgen -source=classify.go -destination=classifymock/classifier.go -package=classifymock Classifier

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Question instructions by name
// Every question asks for the probability of yes
// That is the noul type of the Jev wire format
type Questions map[string]string

// Names in a fixed order for messages and records
func (qs Questions) names() []string {
	return slices.Sorted(maps.Keys(qs))
}

type Request struct {
	// The trace the judgment is about
	Ref string
	// What the classifier reads
	State     string
	Questions Questions
}

type Answer struct {
	// Probability of yes
	Yes float64 `json:"yes"`
	// Set only by a member that explains its answer
	Reason string `json:"reason,omitempty"`
}

// A yes from one half up
func (a Answer) True() bool {
	return a.Yes >= 0.5
}

// Probability of the answer given
// Computed from Yes so cascade compares one scale whatever an endpoint reports
func (a Answer) confidence() float64 {
	return max(a.Yes, 1-a.Yes)
}

// Answers by question name
type Answers map[string]Answer

// Every question has an answer and every probability lies between 0 and 1
func (as Answers) check(qs Questions) error {
	for _, name := range qs.names() {
		a, ok := as[name]
		if !ok {
			return fmt.Errorf("%w: %s", ErrAnswerMissing, name)
		}
		if a.Yes < 0 || a.Yes > 1 {
			return fmt.Errorf("%w: %s is %v", ErrResponseInvalid, name, a.Yes)
		}
	}
	return nil
}

// Every answer reaches the threshold
func (as Answers) confident(threshold float64) bool {
	for _, a := range as {
		if a.confidence() < threshold {
			return false
		}
	}
	return true
}

// The probabilities as one line such as `holds 0.42, states 0.91`
func (as Answers) String() string {
	parts := make([]string, 0, len(as))
	for _, name := range slices.Sorted(maps.Keys(as)) {
		parts = append(parts, fmt.Sprintf("%s %.2f", name, as[name].Yes))
	}
	return strings.Join(parts, ", ")
}

// Something that answers the questions of a request
// The consumer is Plan
type Classifier interface {
	Classify(ctx context.Context, req Request) (Answers, error)
}
