//go:build linux || darwin

package git

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type unixProcessTree struct{}

func newProcessTree() (processTree, error) { return &unixProcessTree{}, nil }

func (*unixProcessTree) Start(cmd *exec.Cmd) error {
	attrs := syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		attrs = *cmd.SysProcAttr
	}
	attrs.Setpgid = true
	attrs.Pgid = 0
	cmd.SysProcAttr = &attrs
	return cmd.Start()
}

func (*unixProcessTree) Terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		return os.ErrProcessDone
	}
	// The child is its own group leader; never signal zero (the caller's group).
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func (*unixProcessTree) Close() error { return nil }
