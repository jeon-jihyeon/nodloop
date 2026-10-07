package classify

import "errors"

var (
	// A decision point outside the valid set
	ErrPointUnknown = errors.New("classify: unknown decision point")
	// A point with no endpoint where one is needed
	ErrPointUnset = errors.New("classify: no endpoint set for the point")
	// An endpoint without an absolute http or https URL
	ErrEndpointInvalid = errors.New("classify: invalid endpoint")
	// A non 2xx answer of an endpoint
	ErrStatus = errors.New("classify: endpoint answered an error status")
	// A body that does not decode as the Jev answers or a probability outside 0 to 1
	ErrResponseInvalid = errors.New("classify: endpoint answer is not valid")
	// A question left without an answer
	ErrAnswerMissing = errors.New("classify: question not answered")
)
