//go:build linux || darwin

package git

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

type unixProcessTree struct {
	mu         sync.Mutex
	command    *exec.Cmd
	process    *os.Process
	pgid       int
	closed     bool
	launchDone chan struct{}
	launchOnce sync.Once
}

func newProcessTree() (processTree, error) {
	return &unixProcessTree{launchDone: make(chan struct{})}, nil
}

func (tree *unixProcessTree) Start(cmd *exec.Cmd) error {
	tree.mu.Lock()
	if tree.closed || tree.command != nil {
		tree.mu.Unlock()
		return newSafeProcessError("attach Git process tree", errors.New("process tree already used"))
	}
	tree.command = cmd
	tree.mu.Unlock()
	// exec may invoke Cancel before cmd.Start returns. Publish the private
	// group only after creation succeeds, then release the cancellation gate.
	defer tree.launchOnce.Do(func() { close(tree.launchDone) })
	attrs := syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		attrs = *cmd.SysProcAttr
	}
	attrs.Setpgid = true
	attrs.Pgid = 0
	cmd.SysProcAttr = &attrs
	if err := cmd.Start(); err != nil {
		return err
	}
	tree.mu.Lock()
	tree.process = cmd.Process
	tree.pgid = cmd.Process.Pid
	tree.mu.Unlock()
	return nil
}

func (tree *unixProcessTree) Terminate(cmd *exec.Cmd) error {
	tree.mu.Lock()
	owned := !tree.closed && tree.command == cmd
	tree.mu.Unlock()
	if !owned {
		return os.ErrProcessDone
	}
	<-tree.launchDone
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.closed || tree.pgid <= 0 || tree.process == nil || tree.process != cmd.Process {
		return os.ErrProcessDone
	}
	// Use only the group created by Start, never a caller-supplied replacement
	// process/PID. Retire it on the first attempt so neither duplicate cleanup
	// nor a late callback can signal a subsequently reused group number.
	pgid := tree.pgid
	tree.pgid = 0
	err := syscall.Kill(-pgid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func (tree *unixProcessTree) Close() error {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	tree.closed = true
	tree.pgid = 0
	tree.process = nil
	return nil
}
