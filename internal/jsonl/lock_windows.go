//go:build windows

package jsonl

import "os"

// Windows has no flock
// One process at a time is the rule there and the torn line check still recovers a partial record
func lock(*os.File) error {
	return nil
}
