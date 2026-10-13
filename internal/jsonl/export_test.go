package jsonl

// Runs stop where a rewrite renames its temp file until the test ends
func StopBeforeReplace(stop func() error) (restore func()) {
	saved := beforeReplace
	beforeReplace = stop
	return func() { beforeReplace = saved }
}
