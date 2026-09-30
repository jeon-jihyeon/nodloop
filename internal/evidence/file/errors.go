package file

import "errors"

var (
	ErrNotDirectory     = errors.New("evidence file source: not a directory")
	ErrRunbooksFolder   = errors.New("evidence file source: the runbooks folder is no longer read, rename it to procedures")
	ErrNoProcedures     = errors.New("evidence file source: no procedure .md file under procedures")
	ErrProcedureSkipped = errors.New("evidence file source: procedures must be .md files directly under procedures")
)
