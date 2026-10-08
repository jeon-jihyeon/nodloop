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
	errHeaderFlag  = errors.New("bad --header")
	errServerMoved = errors.New("moved to its own binary in 0.7.0. Install nodloop-server from the release archives or with " +
		"go install github.com/jeon-jihyeon/nodloop/server/cmd/nodloop-server@latest")
	// A classifier setup of a config before 0.7.0 that no single endpoint answers like
	errSetupRetired     = errors.New("classifier setup no longer run")
	errUnexpectedOutput = errors.New("unexpected output")
	errWrongAnswer      = errors.New("wrong answer")
	errVetoExample      = errors.New("veto example is not a JSON object")
	// A replay trace holds a result the replay package wrote so one that does not decode was edited or cut
	errReplayUndecodable = errors.New("replay trace does not decode")
	// go deletes a go run build on exit so a hook on it would fail open
	errExecutableTemporary = errors.New("the executable is a temporary go build. Install from a built binary such as one from go install")
)

// A command that cannot run as asked
// fail adds the usage text after the message
// The sentinel comes first or last so the printed message reads as one phrase
var (
	errUnknownAction      = errors.New("unknown action")
	errHoldoutInvalid     = errors.New("invalid holdout")
	errSessionModeUnknown = errors.New("unknown session mode")
	errDeferred           = errors.New("deferred to the conversation")
	errCallInput          = errors.New("call input is not a JSON object")
	errRequired           = errors.New("is required")
	errScopeFlags         = errors.New("--label and --everywhere exclude each other")
	errUnknownTraceName   = errors.New("unknown trace name")
	errNoAction           = fmt.Errorf("an action %w", errRequired)
)
