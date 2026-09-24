package file

import "errors"

// Every OS failure reaches a caller as one of these with the cause wrapped behind it
var (
	ErrRead        = errors.New("settings file: read failed")
	ErrJSONInvalid = errors.New("settings file: not valid JSON")
	ErrBackup      = errors.New("settings file: backup failed")
	ErrWrite       = errors.New("settings file: write failed")
)
