//go:build unix

package jsonl

import (
	"errors"
	"os"
)

func rename(from, to string) error {
	return os.Rename(from, to)
}

// Syncs the entries of the directory so a rename survives a power loss
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
