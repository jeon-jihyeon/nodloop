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
	errConfigInvalid    = errors.New(configFile + " is not valid JSON")
	errUnexpectedOutput = errors.New("unexpected output")
	errWrongAnswer      = errors.New("wrong answer")
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
