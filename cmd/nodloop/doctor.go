package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/guard"
	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// doctor reads every record file and names the lines that fail to decode
// --repair moves them aside
func runDoctor(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	fs := newFlagSet("doctor", stderr)
	var records recordFlags
	records.bind(fs)
	repair := fs.Bool("repair", false, "move the corrupt lines to <file>.corrupt")
	if err := fs.Parse(args); err != nil {
		return parseFailed(err)
	}
	a, err := records.app(getenv, now)
	if err != nil {
		return fail(stderr, "doctor", err)
	}
	files, err := a.recordFiles()
	if err != nil {
		return fail(stderr, "doctor", err)
	}
	corrupt := 0
	for _, f := range files {
		left, err := f.check(stdout, *repair)
		if err != nil {
			return fail(stderr, "doctor", err)
		}
		if left {
			corrupt++
		}
	}
	if corrupt > 0 {
		fmt.Fprintf(stderr, "nodloop doctor: %d files hold corrupt lines. Stop Claude Code and every nodloop process, then run nodloop doctor --repair\n", corrupt)
		return 1
	}
	return 0
}

// A record file doctor reads and repairs
type checkedFile interface {
	Name() string
	// How many records the file holds and the error naming its corrupt lines
	Check() (int, error)
	// Moves the corrupt lines aside and returns how many
	Repair() (int, error)
}

// One record file with the error its corrupt lines come back as
type recordFile struct {
	file    checkedFile
	corrupt error
}

// One line of the file with its records and corrupt lines
// repair moves the corrupt lines aside
// Returns whether corrupt lines are left in place
func (f recordFile) check(out io.Writer, repair bool) (bool, error) {
	name := f.file.Name()
	n, err := f.file.Check()
	switch {
	case errors.Is(err, f.corrupt) && repair:
		moved, err := f.file.Repair()
		if err != nil {
			return false, err
		}
		fmt.Fprintf(out, "%s\t%d records\tmoved %d corrupt lines to %s.corrupt\n", name, n, moved, name)
		return false, nil
	case errors.Is(err, f.corrupt):
		fmt.Fprintf(out, "%s\t%d records\t%v\n", name, n, err)
		return true, nil
	case err != nil:
		return false, err
	}
	fmt.Fprintf(out, "%s\t%d records\tok\n", name, n)
	return false, nil
}

// The record files of the record directory and the guard log under home
// A file not written yet reads as zero records
func (a app) recordFiles() ([]recordFile, error) {
	traces, err := a.traces()
	if err != nil {
		return nil, err
	}
	verdicts, err := a.feedback()
	if err != nil {
		return nil, err
	}
	outcomes, err := a.outcomes()
	if err != nil {
		return nil, err
	}
	dir, err := a.makeRecordDir()
	if err != nil {
		return nil, err
	}
	ledger, err := knowledgefile.New(dir)
	if err != nil {
		return nil, err
	}
	files := []recordFile{
		{traces, tracefile.ErrCorrupt},
		{verdicts, feedbackfile.ErrCorrupt},
		{outcomes, feedbackfile.ErrCorrupt},
		{ledger, knowledgefile.ErrCorrupt},
	}
	if a.cfg.home == "" {
		return files, nil
	}
	log, err := jsonl.Open[guard.Entry](a.cfg.home.dir(), decisionLog)
	if errors.Is(err, os.ErrNotExist) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	return append(files, recordFile{log, jsonl.ErrCorrupt}), nil
}
