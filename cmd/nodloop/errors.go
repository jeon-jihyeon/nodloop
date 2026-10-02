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
	errNotCorrected     = errors.New("a proposal from a run needs an edit or a reject on it")
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
	errRequired         = errors.New("is required")
	errUnknownTraceName = errors.New("unknown trace name")
	errNoAction         = fmt.Errorf("an action %w", errRequired)
	// The session names the report file so a separator would leave the record directory
	errSessionPath = errors.New("must not contain a path separator")
)
