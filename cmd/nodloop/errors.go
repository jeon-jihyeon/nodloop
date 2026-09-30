package main

import (
	"errors"
	"fmt"
)

var (
	errHomeUnknown      = errors.New("home directory unknown")
	errDataDirUnset     = errors.New(envFileDir + " is not set")
	errUnknownSource    = errors.New("unknown source")
	errNoEvents         = errors.New("no events.csv")
	errPolicyMissing    = errors.New("no policy.yaml")
	errConfigInvalid    = errors.New(configFile + " is not valid JSON")
	errUnexpectedOutput = errors.New("unexpected output")
	errWrongAnswer      = errors.New("wrong answer")
	errVetoExample      = errors.New("veto example is not a JSON object")
	// go deletes a go run build on exit so a hook on it would fail open
	errExecutableTemporary = errors.New("the executable is a temporary go build. Install from a built binary such as one from go install")
)

// A command that cannot run as asked
// fail adds the usage text after the message
// The sentinel comes first or last so the printed message reads as one phrase
var (
	errUnknownAction    = errors.New("unknown action")
	errRequired         = errors.New("is required")
	errUnknownTraceName = errors.New("unknown trace name")
	errNoAction         = fmt.Errorf("an action %w", errRequired)
)
