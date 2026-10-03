package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// This binary started with the args in a process group of its own, so neither the hook nor Claude Code ending stops it
// Its stdout and stderr are appended to log
func detached(log string) starter {
	return func(args []string) error {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		cmd := exec.Command(exe, args...)
		cmd.Stdout, cmd.Stderr = out, out
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		err = cmd.Start()
		// The child holds its own copy of the file so the parent closes its one at once
		closed := out.Close()
		if err != nil {
			return errors.Join(err, closed)
		}
		return errors.Join(cmd.Process.Release(), closed)
	}
}
