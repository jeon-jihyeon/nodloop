package analysis

import "errors"

var (
	ErrMalformedPolicy    = errors.New("analysis: policy is not valid yaml")
	ErrUnknownRule        = errors.New("analysis: unknown rule")
	ErrMissingVersion     = errors.New("analysis: policy version is required")
	ErrIncompleteAnalyzer = errors.New("analysis: metrics, baseline and window are required")
	ErrMissingGroupBy     = errors.New("analysis: group_by is required")
	ErrProportionMetrics  = errors.New("analysis: proportion_control needs exactly a numerator and a denominator metric")
	ErrPolicyUnobserved   = errors.New("analysis: policy names a metric or dimension no event carries")
	ErrLimitsSection      = errors.New("analysis: the limits section is no longer read because the review caps are internal now so remove it")
	ErrUnknownContextDecl = errors.New("analysis: invalid change context declaration")
	ErrIncompleteCommand  = errors.New("analysis: a command analyzer needs a name and a command")
	ErrCommandName        = errors.New("analysis: two command analyzers share one name")
	ErrCommandRunner      = errors.New("analysis: a command analyzer needs a runner")
	ErrCommandFailed      = errors.New("analysis: command analyzer failed")
	ErrCommandTimeout     = errors.New("analysis: command analyzer timed out")
	ErrCommandOutput      = errors.New("analysis: command analyzer output is invalid")
)
