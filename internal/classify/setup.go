package classify

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
	"time"
)

// A place of the loop that asks yes or no questions
type Point string

const (
	PointCritic   Point = "critic"   // the second reader of a drafted lesson
	PointReaction Point = "reaction" // whether the user's message corrects or approves the previous answer
)

func Points() []Point {
	return []Point{PointCritic, PointReaction}
}

func (p Point) Valid() bool {
	return slices.Contains(Points(), p)
}

// Fails with ErrPointUnknown naming the valid points
func (p Point) Check() error {
	if !p.Valid() {
		return fmt.Errorf("%w: %q. Use one of %v", ErrPointUnknown, p, Points())
	}
	return nil
}

type Endpoint struct {
	// The whole URL a request posts to such as http://localhost:8000/v1/systemone
	URL string `json:"url"`
	// Sent as the model field
	// Empty leaves it out
	Model string `json:"model,omitempty"`
	// The env variable that holds the API key at call time
	// The key itself is never saved
	KeyEnv string `json:"key_env,omitempty"`
}

func (e Endpoint) check() error {
	u, err := url.Parse(e.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: %q is not an absolute http or https URL", ErrEndpointInvalid, e.URL)
	}
	return nil
}

// The host of the URL as records and reports name the endpoint
func (e Endpoint) Name() string {
	u, err := url.Parse(e.URL)
	if err != nil || u.Host == "" {
		return e.URL
	}
	return u.Host
}

// The client of the endpoint with the key its env holds now
func (e Endpoint) Classifier(getenv func(string) string, timeout time.Duration) *HTTP {
	key := ""
	if e.KeyEnv != "" {
		key = getenv(e.KeyEnv)
	}
	return NewHTTP(e, key, timeout)
}

// The endpoint each point asks before its built in member
// A point left out asks the built in member alone
type Endpoints map[Point]Endpoint

// The endpoints with the one of the point added or replaced
func (es Endpoints) With(p Point, e Endpoint) (Endpoints, error) {
	if err := p.Check(); err != nil {
		return nil, err
	}
	if err := e.check(); err != nil {
		return nil, err
	}
	out := maps.Clone(es)
	if out == nil {
		out = Endpoints{}
	}
	out[p] = e
	return out, nil
}

// The endpoints without the one of the point
func (es Endpoints) Without(p Point) (Endpoints, error) {
	if err := p.Check(); err != nil {
		return nil, err
	}
	out := maps.Clone(es)
	delete(out, p)
	return out, nil
}

// Every key is a point and every endpoint a URL so a config edited by hand fails before a call
func (es Endpoints) Check() error {
	for _, p := range slices.Sorted(maps.Keys(es)) {
		if err := p.Check(); err != nil {
			return err
		}
		if err := es[p].check(); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}
