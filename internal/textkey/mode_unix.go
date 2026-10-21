//go:build unix

package textkey

import "io/fs"

// Whether a user other than the owner may read or write a file of the mode
func shared(mode fs.FileMode) bool {
	return mode.Perm()&0o077 != 0
}
