package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// The variables a detached pass never takes from the env it inherits
// A hook starts the passes and the env of a project settings file an agent can edit reaches every hook
// NODLOOP_LLM_MODEL picks the model that predicts the reactions of the person and drafts and passes their lessons
// So a pass reads the model of config.json
var passDropped = []string{envLLMModel}

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
		cmd.Env = passEnv(os.Environ())
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

// The env of a detached pass without the variables it never takes
func passEnv(environ []string) []string {
	return slices.DeleteFunc(slices.Clone(environ), droppedVar)
}

func droppedVar(pair string) bool {
	name, _, _ := strings.Cut(pair, "=")
	return slices.Contains(passDropped, name)
}
