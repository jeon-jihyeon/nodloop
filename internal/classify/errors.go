package classify

import "errors"

var (
	// A decision point outside the valid set
	ErrPointUnknown = errors.New("classify: unknown decision point")
	// A mode, member list, threshold or combine the mode does not take
	ErrSetupInvalid = errors.New("classify: invalid setup")
	// A member that is neither claude nor an added endpoint
	ErrClassifierUnknown = errors.New("classify: unknown classifier")
	// An endpoint without an absolute http URL or one named claude
	ErrEndpointInvalid = errors.New("classify: invalid endpoint")
	// An endpoint still named by the setup of a point
	ErrClassifierInUse = errors.New("classify: classifier in use")
	// A non 2xx answer of an endpoint
	ErrStatus = errors.New("classify: endpoint answered an error status")
	// A body that does not decode as the Jev answers
	ErrResponseInvalid = errors.New("classify: endpoint answer is not valid")
	// A question left without an answer
	ErrAnswerMissing = errors.New("classify: question not answered")
)
