package classify

import "errors"

var (
	// A decision point outside the valid set
	ErrPointUnknown = errors.New("classify: unknown decision point")
	// A point with no endpoint where one is needed
	ErrPointUnset = errors.New("classify: no endpoint set for the point")
	// An endpoint without an absolute http or https URL
	ErrEndpointInvalid = errors.New("classify: invalid endpoint")
	// An endpoint URL that carries a user or a password where only an env variable may hold the key
	ErrEndpointCredentials = errors.New("classify: the endpoint URL carries credentials. Name the env variable of the key with --key-env")
	// A non 2xx answer of an endpoint
	ErrStatus = errors.New("classify: endpoint answered an error status")
	// A body that does not decode as the Jev answers or a probability outside 0 to 1
	ErrResponseInvalid = errors.New("classify: endpoint answer is not valid")
	// A question left without an answer
	ErrAnswerMissing = errors.New("classify: question not answered")
	// A member a panel trace records with the error it failed on
	ErrMemberFailed = errors.New("classify: member gave no answer")
)
