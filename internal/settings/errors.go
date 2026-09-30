package settings

import "errors"

var (
	ErrHooksInvalid = errors.New("hooks has an unexpected shape")
	ErrHookNarrow   = errors.New("hook matcher leaves tools out")
	ErrHooksOff     = errors.New("disableAllHooks is true")
)
