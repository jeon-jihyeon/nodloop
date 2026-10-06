package main

import (
	"errors"
	"fmt"
)

var (
	errHomeUnknown = errors.New("home directory unknown")
	// A run label is key=value and an applied item is id:version
	errLabelFlag   = errors.New("bad --label")
	errAppliedFlag = errors.New("bad --applied")
	// A proposal from a run teaches what a person corrected
	errConfigInvalid    = errors.New(configFile + " is not valid JSON")
	errUnexpectedOutput = errors.New("unexpected output")
	errWrongAnswer      = errors.New("wrong answer")
	errVetoExample      = errors.New("veto example is not a JSON object")
	// go deletes a go run build on exit so a hook on it would fail open
	errExecutableTemporary = errors.New("the executable is a temporary go build. Install from a built binary such as one from go install")
	// A relative path names other records and another approved veto file in every working directory
	errRecordDirRelative = errors.New("the record directory must be an absolute path")
)

// A command that cannot run as asked
// fail adds the usage text after the message
// The sentinel comes first or last so the printed message reads as one phrase
var (
	errUnknownAction    = errors.New("unknown action")
	errHoldoutInvalid   = errors.New("invalid holdout")
	errDeferred         = errors.New("deferred to the conversation")
	errCallInput        = errors.New("call input is not a JSON object")
	errKeyExists        = errors.New("a server key of that name exists")
	errKeyUnknown       = errors.New("no server key of that name")
	errTenantInvalid    = errors.New("invalid tenant")
	errRoleInvalid      = errors.New("invalid role")
	errNoKeys           = errors.New("no server key")
	errRequired         = errors.New("is required")
	errUnknownTraceName = errors.New("unknown trace name")
	errNoAction         = fmt.Errorf("an action %w", errRequired)
)
