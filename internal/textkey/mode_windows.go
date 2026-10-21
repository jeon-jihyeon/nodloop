//go:build windows

package textkey

import "io/fs"

// Windows keeps no unix mode bits and Go reports every writable file as 0666
// So the ACL of the record directory decides who reads the key
func shared(fs.FileMode) bool {
	return false
}
