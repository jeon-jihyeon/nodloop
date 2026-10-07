package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/guard"
	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// doctor reads every record file and names the lines that fail to decode, and --repair moves them aside
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

// One record file with the checks doctor runs on it
type recordFile struct {
	name string
	// Records read and the error naming the corrupt lines
	read func() (int, error)
	// Lines moved aside
	repair func() (int, error)
}

func newRecordFile[T any](dir, name string) (recordFile, error) {
	f, err := jsonl.Open[T](dir, name)
	if err != nil {
		return recordFile{}, err
	}
	read := func() (int, error) {
		all, err := f.All()
		return len(all), err
	}
	return recordFile{name: name, read: read, repair: f.Repair}, nil
}

// One line of the file with its records and corrupt lines, moving them aside on repair
// Returns whether corrupt lines are left in place
func (f recordFile) check(out io.Writer, repair bool) (bool, error) {
	n, err := f.read()
	switch {
	case errors.Is(err, jsonl.ErrCorrupt) && repair:
		moved, err := f.repair()
		if err != nil {
			return false, err
		}
		fmt.Fprintf(out, "%s\t%d records\tmoved %d corrupt lines to %s.corrupt\n", f.name, n, moved, f.name)
		return false, nil
	case errors.Is(err, jsonl.ErrCorrupt):
		fmt.Fprintf(out, "%s\t%d records\t%v\n", f.name, n, err)
		return true, nil
	case err != nil:
		return false, err
	}
	fmt.Fprintf(out, "%s\t%d records\tok\n", f.name, n)
	return false, nil
}

// The record files of the record directory and the guard log under home
// A file not written yet reads as zero records
func (a app) recordFiles() ([]recordFile, error) {
	dir, err := a.makeRecordDir()
	if err != nil {
		return nil, err
	}
	var files []recordFile
	for _, open := range []func() (recordFile, error){
		func() (recordFile, error) { return newRecordFile[trace.Trace](dir, "traces.jsonl") },
		func() (recordFile, error) { return newRecordFile[feedback.Feedback](dir, "feedback.jsonl") },
		func() (recordFile, error) { return newRecordFile[feedback.Outcome](dir, "outcomes.jsonl") },
		func() (recordFile, error) { return newRecordFile[knowledge.Knowledge](dir, "knowledge.jsonl") },
	} {
		f, err := open()
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	if a.cfg.home == "" {
		return files, nil
	}
	log, err := newRecordFile[guard.Entry](a.cfg.home.dir(), decisionLog)
	if errors.Is(err, os.ErrNotExist) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	return append(files, log), nil
}
