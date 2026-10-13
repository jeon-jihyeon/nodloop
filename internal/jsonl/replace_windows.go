//go:build windows

package jsonl

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// Tries the rename while a reader holds the file open
// Go opens a file on Windows without sharing its deletion so a rename over a file a reader holds fails until the reader closes it
// Readers hold the file only for one read so a short wait is enough and the file stays as it was when the wait runs out
const (
	renameTries = 100
	renamePause = 20 * time.Millisecond
)

func rename(from, to string) error {
	var err error
	for range renameTries {
		err = os.Rename(from, to)
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return err
		}
		time.Sleep(renamePause)
	}
	return err
}

// Windows has no directory sync and MoveFileEx is durable once it returns
func syncDir(string) error {
	return nil
}
