package replay

import "errors"

var (
	// A judgment with a veto acts through the guard and an item without a run scope reaches no output
	ErrNoScope = errors.New("replay: the version reaches no run output")
	ErrNoCases = errors.New("replay: no recorded output to judge")
	// The judge answered something other than one answer per output
	ErrJudgment = errors.New("replay: the judgment is incomplete")
)
