package analysis

import "errors"

var (
	ErrMalformedPolicy    = errors.New("analysis: policy is not valid yaml")
	ErrUnknownRule        = errors.New("analysis: unknown rule")
	ErrMissingVersion     = errors.New("analysis: policy version is required")
	ErrIncompleteAnalyzer = errors.New("analysis: metrics, baseline and window are required")
	ErrMissingGroupBy     = errors.New("analysis: group_by is required")
	ErrMissingDenominator = errors.New("analysis: proportion_control needs a numerator and denominator metric")
)
