//go:build linux || darwin

package git

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func helperStopped(pid int) bool {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return true
	}
	// A zombie has stopped executing and cannot retain pipes. Container PID 1
	// may not reap orphaned children promptly; don't mistake that for liveness.
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if errors.Is(err, os.ErrNotExist) {
			return true
		}
		end := strings.LastIndex(string(data), ")")
		return end >= 0 && strings.HasPrefix(string(data)[end+1:], " Z")
	}
	output, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(output)), "Z")
}

func assertHelpersStopped(t *testing.T, pids helperPIDs) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if helperStopped(pids.Parent) && helperStopped(pids.Child) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helpers still executing: parent=%d child=%d", pids.Parent, pids.Child)
}

func TestCommandRunnerTerminatesProcessTree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parent child PIDs.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024,
		func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			cmd := helperCommand(ctx, "tree-parent", path)
			return cmd
		})
	done := make(chan error, 1)
	dir := t.TempDir()
	go func() { _, err := runner.Run(ctx, Command{Dir: dir}); done <- err }()
	returned := false
	defer func() {
		cancel()
		if data, err := os.ReadFile(path); err == nil {
			var pids helperPIDs
			if jsonErr := json.Unmarshal(data, &pids); jsonErr == nil && pids.Child > 0 {
				_ = syscall.Kill(pids.Child, syscall.SIGKILL)
			}
		}
		if !returned {
			select {
			case <-done:
			case <-time.After(helperTimeout):
				t.Error("runner did not finish cleanup")
			}
		}
	}()
	pids := waitForHelperPIDs(t, path)
	parentGroup, err := syscall.Getpgid(pids.Parent)
	if err != nil {
		t.Fatal(err)
	}
	childGroup, err := syscall.Getpgid(pids.Child)
	if err != nil {
		t.Fatal(err)
	}
	if parentGroup != pids.Parent || childGroup != parentGroup {
		t.Errorf("groups parent=%d child=%d, want private group %d", parentGroup, childGroup, pids.Parent)
	}
	cancel()
	select {
	case err := <-done:
		returned = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not return within 5s")
	}
	assertHelpersStopped(t, pids)
}

func TestUnixProcessTreePreservesAttributes(t *testing.T) {
	tree, err := newProcessTree()
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	cmd := helperCommand(context.Background(), "wait")
	attrs := &syscall.SysProcAttr{Noctty: false}
	cmd.SysProcAttr = attrs
	if err := tree.Start(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if cmd.SysProcAttr == attrs || attrs.Setpgid || !cmd.SysProcAttr.Setpgid {
		t.Error("SysProcAttr not copied with private process group")
	}
	if err := tree.Terminate(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Error("killed process exited successfully")
	}
	if err := tree.Terminate(cmd); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Terminate after reap = %v", err)
	}
	if err := tree.Terminate(&exec.Cmd{}); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Terminate without process = %v", err)
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
}
