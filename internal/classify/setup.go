package classify

import (
	"cmp"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"
)

// A place of the loop that asks yes or no questions
type Point string

const (
	PointCritic Point = "critic" // the second reader of a drafted lesson
)

func Points() []Point {
	return []Point{PointCritic}
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

// How the members of a setup answer together
type Mode string

const (
	ModeSingle   Mode = "single"   // one member
	ModeCascade  Mode = "cascade"  // in order until one answers every question at the threshold
	ModeParallel Mode = "parallel" // every member and their answers combined
)

func (m Mode) Valid() bool {
	return slices.Contains([]Mode{ModeSingle, ModeCascade, ModeParallel}, m)
}

// How parallel answers combine
type Combine string

const (
	CombineAll Combine = "all" // yes only when every member says yes
	CombineAny Combine = "any" // yes when one member says yes
)

func (c Combine) Valid() bool {
	return c == CombineAll || c == CombineAny
}

// The member every point has without an endpoint
const Claude = "claude"

// Threshold of a cascade that names none
const defaultThreshold = 0.8

type Setup struct {
	Mode Mode `json:"mode"`
	// Asked in this order
	Members []string `json:"members"`
	// Only for cascade
	Threshold float64 `json:"threshold,omitempty"`
	// Only for parallel
	Combine Combine `json:"combine,omitempty"`
}

// The mode and the members in order and the parameter of the mode
// Tab separated
func (s Setup) String() string {
	text := fmt.Sprintf("%s\t%s", s.Mode, strings.Join(s.Members, ","))
	switch s.Mode {
	case ModeCascade:
		text += fmt.Sprintf("\tthreshold %v", s.Threshold)
	case ModeParallel:
		text += "\tcombine " + string(s.Combine)
	}
	return text
}

// A checked setup with the defaults filled
// 1. no mode is single for one member and cascade for more
// 2. a cascade without a threshold stops at 0.8 and a parallel without a combine is all
func NewSetup(members []string, mode Mode, threshold float64, combine Combine) (Setup, error) {
	if mode == "" {
		mode = ModeCascade
		if len(members) == 1 {
			mode = ModeSingle
		}
	}
	s := Setup{Mode: mode, Members: members, Threshold: threshold, Combine: combine}
	switch mode {
	case ModeCascade:
		s.Threshold = cmp.Or(threshold, defaultThreshold)
	case ModeParallel:
		s.Combine = cmp.Or(combine, CombineAll)
	}
	if err := s.Check(); err != nil {
		return Setup{}, err
	}
	return s, nil
}

// The mode takes the members and exactly the parameter it needs
// A config edited by hand is checked again when it is read
func (s Setup) Check() error {
	if !s.Mode.Valid() {
		return fmt.Errorf("%w: mode %q", ErrSetupInvalid, s.Mode)
	}
	if err := s.checkMembers(); err != nil {
		return err
	}
	return s.checkParameter()
}

// Single takes one member and the other modes two or more
// No member is named twice
func (s Setup) checkMembers() error {
	single := s.Mode == ModeSingle
	switch {
	case single && len(s.Members) != 1:
		return fmt.Errorf("%w: single takes one member, got %d", ErrSetupInvalid, len(s.Members))
	case !single && len(s.Members) < 2:
		return fmt.Errorf("%w: %s takes two members or more, got %d", ErrSetupInvalid, s.Mode, len(s.Members))
	case len(slices.Compact(slices.Sorted(slices.Values(s.Members)))) < len(s.Members):
		return fmt.Errorf("%w: a member is named twice", ErrSetupInvalid)
	}
	return nil
}

// A cascade needs a threshold and a parallel a combine and no other mode takes either
// Without them a cascade would let its first member answer alone and a parallel would keep the first answer
func (s Setup) checkParameter() error {
	cascade, parallel := s.Mode == ModeCascade, s.Mode == ModeParallel
	switch {
	case !cascade && s.Threshold != 0:
		return fmt.Errorf("%w: threshold is for cascade only", ErrSetupInvalid)
	case cascade && (s.Threshold <= 0 || s.Threshold > 1):
		return fmt.Errorf("%w: a cascade threshold lies above 0 and up to 1, got %v", ErrSetupInvalid, s.Threshold)
	case !parallel && s.Combine != "":
		return fmt.Errorf("%w: combine is for parallel only", ErrSetupInvalid)
	case parallel && !s.Combine.Valid():
		return fmt.Errorf("%w: combine %q is neither all nor any", ErrSetupInvalid, s.Combine)
	}
	return nil
}

// The setup of each point a user set up
type Decisions map[Point]Setup

// The points whose setup names the member
func (d Decisions) Using(name string) []Point {
	var points []Point
	for _, p := range slices.Sorted(maps.Keys(d)) {
		if slices.Contains(d[p].Members, name) {
			points = append(points, p)
		}
	}
	return points
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

func (e Endpoint) Check() error {
	u, err := url.Parse(e.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: %q is not an absolute http or https URL", ErrEndpointInvalid, e.URL)
	}
	return nil
}

// The client of the endpoint with the key its env holds now
func (e Endpoint) Classifier(getenv func(string) string, timeout time.Duration) *HTTP {
	key := ""
	if e.KeyEnv != "" {
		key = getenv(e.KeyEnv)
	}
	return NewHTTP(e, key, timeout)
}

// Endpoints by the name a setup uses
type Endpoints map[string]Endpoint

// The endpoints with one added or replaced
// claude names the built in member of every point so no endpoint takes it
func (es Endpoints) With(name string, e Endpoint) (Endpoints, error) {
	switch name {
	case "":
		return nil, fmt.Errorf("%w: a name is required", ErrEndpointInvalid)
	case Claude:
		return nil, fmt.Errorf("%w: the name %q is reserved", ErrEndpointInvalid, name)
	}
	if err := e.Check(); err != nil {
		return nil, err
	}
	out := maps.Clone(es)
	if out == nil {
		out = Endpoints{}
	}
	out[name] = e
	return out, nil
}

// Every member of the setup is claude or an endpoint
func (es Endpoints) Check(s Setup) error {
	for _, name := range s.Members {
		if _, ok := es[name]; !ok && name != Claude {
			return fmt.Errorf("%w: %s. Add it with nodloop classifier add", ErrClassifierUnknown, name)
		}
	}
	return nil
}

// The members of the setup in order
// claude is the built in member of the point and every other name is its endpoint with the key its env holds
func (es Endpoints) Members(s Setup, builtin Classifier, getenv func(string) string, timeout time.Duration) ([]Member, error) {
	if err := es.Check(s); err != nil {
		return nil, err
	}
	members := make([]Member, 0, len(s.Members))
	for _, name := range s.Members {
		if name == Claude {
			members = append(members, Member{Name: name, Classifier: builtin})
			continue
		}
		members = append(members, Member{Name: name, Classifier: es[name].Classifier(getenv, timeout)})
	}
	return members, nil
}
