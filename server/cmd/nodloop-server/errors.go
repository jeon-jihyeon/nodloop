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
)
