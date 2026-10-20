package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func helperStopped(pid int) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return true
	}
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	state, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && state == windows.WAIT_OBJECT_0
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

func cleanupWindowsHelpers(path string) {
	data, err := os.ReadFile(path)
	var pids helperPIDs
	if err == nil && json.Unmarshal(data, &pids) == nil {
		for _, pid := range []int{pids.Parent, pids.Child} {
			if pid > 0 {
				process, err := os.FindProcess(pid)
				if err == nil {
					_ = process.Kill()
					_ = process.Release()
				}
			}
		}
	}
}

func assertNoHelperMarker(t *testing.T, path string) {
	t.Helper()
	for _, marker := range []string{path, path + ".child"} {
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("suspended helper ran: %s: %v", marker, err)
		}
	}
}

func TestCommandRunnerTerminatesProcessTree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parent child PIDs.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer cleanupWindowsHelpers(path)
	treeInterface, err := newProcessTree()
	if err != nil {
		t.Fatal(err)
	}
	tree := treeInterface.(*windowsProcessTree)
	defer tree.Close()
	tree.beforeAssign = func(cmd *exec.Cmd) error {
		// Give an unsuspended process ample time to expose the race.
		time.Sleep(100 * time.Millisecond)
		assertNoHelperMarker(t, path)
		if tree.attached.Load() {
			t.Error("attached before assignment")
		}
		return nil
	}
	nativeResume := tree.resume
	tree.resume = func(thread windows.Handle) (uint32, error) {
		if !tree.attached.Load() {
			t.Error("resuming before job assignment")
		}
		assertNoHelperMarker(t, path)
		return nativeResume(thread)
	}
	runner := newCommandRunner("git", helperTimeout, helperTimeout, 1024,
		func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return helperCommand(ctx, "tree-parent", path)
		})
	runner.newProcessTree = func() (processTree, error) { return tree, nil }
	dir := t.TempDir()
	done := make(chan error, 1)
	go func() { _, err := runner.Run(ctx, Command{Dir: dir}); done <- err }()
	returned := false
	defer func() {
		cancel()
		if !returned {
			select {
			case <-done:
			case <-time.After(helperTimeout):
				t.Error("runner cleanup did not finish")
			}
		}
	}()
	pids := waitForHelperPIDs(t, path)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path + ".child"); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(path + ".child"); err != nil {
		t.Fatal("child did not run after resume")
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
	if tree.attached.Load() {
		t.Error("Close did not clear attached")
	}
}

func TestWindowsProcessTreeSetupFailuresReap(t *testing.T) {
	for _, stage := range []string{"before assign", "assign", "resume", "resume count"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "must not run.json")
			defer cleanupWindowsHelpers(path)
			treeInterface, err := newProcessTree()
			if err != nil {
				t.Fatal(err)
			}
			tree := treeInterface.(*windowsProcessTree)
			defer tree.Close()
			cmd := helperCommand(context.Background(), "tree-parent", path)
			sentinel := errors.New("sentinel https://user:password@example.invalid PID 12345")
			fail := func() error { return fmt.Errorf("PID %d: %w", cmd.Process.Pid, sentinel) }
			switch stage {
			case "before assign":
				tree.beforeAssign = func(*exec.Cmd) error { return fail() }
			case "assign":
				tree.assign = func(windows.Handle, windows.Handle) error { return fail() }
			case "resume":
				tree.resume = func(windows.Handle) (uint32, error) { return 0, fail() }
			case "resume count":
				tree.resume = func(windows.Handle) (uint32, error) { return 0, nil }
			}
			err = tree.Start(cmd)
			if stage != "resume count" {
				assertSafeProcessError(t, err, sentinel)
			} else {
				var safe *SafeError
				if !errors.As(err, &safe) || safe.Code != CodeCommandFailed || safe.Error() != "Git command failed" {
					t.Fatalf("unsafe resume count error: %v", err)
				}
			}
			for _, public := range []string{fmt.Sprintf("%+v", err), func() string { b, _ := json.Marshal(err); return string(b) }()} {
				if strings.Contains(public, strconv.Itoa(cmd.Process.Pid)) {
					t.Fatalf("PID leaked: %s", public)
				}
			}
			assertNoHelperMarker(t, path)
			if cmd.ProcessState == nil {
				t.Fatal("failed Start did not reap process")
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("second Wait succeeded")
			}
			if !helperStopped(cmd.Process.Pid) {
				t.Fatal("failed Start left helper executing")
			}
			wantAttached := stage == "resume" || stage == "resume count"
			if tree.attached.Load() != wantAttached {
				t.Errorf("attached = %t, want %t", tree.attached.Load(), wantAttached)
			}
			if err := tree.Close(); err != nil {
				t.Fatal(err)
			}
			if tree.attached.Load() {
				t.Error("Close did not clear attached")
			}
			if err := tree.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWindowsProcessTreeCancellationWaitsForLaunch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must not run.json")
	treeInterface, err := newProcessTree()
	if err != nil {
		t.Fatal(err)
	}
	tree := treeInterface.(*windowsProcessTree)
	defer tree.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := helperCommand(ctx, "tree-parent", path)
	cmd.Cancel = func() error { return tree.Terminate(cmd) }
	cmd.WaitDelay = processWaitDelay
	entered, release := make(chan struct{}), make(chan struct{})
	sentinel := errors.New("sentinel")
	tree.beforeAssign = func(*exec.Cmd) error { close(entered); <-release; return sentinel }
	done := make(chan error, 1)
	go func() { done <- tree.Start(cmd) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("Start did not reach assignment")
	}
	cancel()
	select {
	case <-done:
		close(release)
		t.Fatal("Start finished before launch gate released")
	case <-time.After(50 * time.Millisecond):
	}
	assertNoHelperMarker(t, path)
	close(release)
	select {
	case err := <-done:
		assertSafeProcessError(t, err, sentinel)
	case <-time.After(5 * time.Second):
		t.Fatal("Start/Wait deadlocked with cancellation")
	}
	if cmd.ProcessState == nil {
		t.Fatal("failed Start did not reap")
	}
}

func TestWindowsProcessTreeCloseKillsDescendantsAfterWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parent child PIDs.json")
	defer cleanupWindowsHelpers(path)
	treeInterface, err := newProcessTree()
	if err != nil {
		t.Fatal(err)
	}
	tree := treeInterface.(*windowsProcessTree)
	defer tree.Close()
	cmd := helperCommand(context.Background(), "tree-parent-exit", path)
	attrs := &syscall.SysProcAttr{HideWindow: true}
	cmd.SysProcAttr = attrs
	if err := tree.Start(cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.SysProcAttr == attrs || attrs.CreationFlags != 0 || !cmd.SysProcAttr.HideWindow {
		t.Error("SysProcAttr was not copied")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	pids := waitForHelperPIDs(t, path)
	if helperStopped(pids.Child) {
		t.Fatal("descendant did not survive parent Wait")
	}
	if !tree.attached.Load() {
		t.Fatal("Wait detached job")
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	assertHelpersStopped(t, pids)
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tree.Terminate(cmd); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Terminate closed tree = %v", err)
	}
}

func TestWindowsProcessTreeStartFailureReleasesGate(t *testing.T) {
	treeInterface, err := newProcessTree()
	if err != nil {
		t.Fatal(err)
	}
	tree := treeInterface.(*windowsProcessTree)
	defer tree.Close()
	cmd := exec.CommandContext(context.Background(), "igonotes-missing-helper-executable")
	var unavailable *exec.Error
	if err := tree.Start(cmd); !errors.As(err, &unavailable) {
		t.Fatalf("Start = %v, want exec.Error", err)
	}
	done := make(chan error, 1)
	go func() { done <- tree.Terminate(cmd) }()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrProcessDone) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("launch gate not released")
	}
}
