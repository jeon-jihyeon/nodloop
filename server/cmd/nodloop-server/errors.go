package main

import "errors"

var (
	errHomeUnknown   = errors.New("home directory unknown")
	errUnknownAction = errors.New("unknown action")
	errRequired      = errors.New("is required")
	errKeyExists     = errors.New("a server key of that name exists")
	errKeyUnknown    = errors.New("no server key of that name")
	errTenantInvalid = errors.New("invalid tenant")
	errRoleInvalid   = errors.New("invalid role")
	errNoKeys        = errors.New("no server key")
	// A tenant that has no record directory yet so a prune has nothing to read
	errNoRecords = errors.New("no records")
	// A query string a records page cannot read
	errQueryInvalid = errors.New("invalid query")
	// A body POST /v1/interventions cannot read as one payload
	errPayloadInvalid = errors.New("invalid intervention payload")
	// A key add --person that no person typed at a terminal of their own
	errPersonUnproven = errors.New("--person is taken only at an interactive terminal outside an agent session, since the key decides for the person it names")
)
