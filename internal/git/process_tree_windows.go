package git

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcessTree struct {
	mu         sync.Mutex
	job        windows.Handle
	attached   atomic.Bool
	launchDone chan struct{}
	launchOnce sync.Once

	beforeAssign func(*exec.Cmd) error
	assign       func(windows.Handle, windows.Handle) error
	resume       func(windows.Handle) (uint32, error)
}

func newProcessTree() (processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		return nil, errors.Join(err, windows.CloseHandle(job))
	}
	return &windowsProcessTree{
		job: job, launchDone: make(chan struct{}),
		assign: windows.AssignProcessToJobObject, resume: windows.ResumeThread,
	}, nil
}

func (tree *windowsProcessTree) releaseLaunch() {
	tree.launchOnce.Do(func() { close(tree.launchDone) })
}

func (tree *windowsProcessTree) Start(cmd *exec.Cmd) error {
	attrs := syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		attrs = *cmd.SysProcAttr
	}
	attrs.CreationFlags |= windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW
	cmd.SysProcAttr = &attrs
	if err := cmd.Start(); err != nil {
		tree.releaseLaunch()
		return err
	}
	if err := tree.attachAndResume(cmd); err != nil {
		// Kill while the initial thread is still suspended. Release the gate
		// before Wait: exec's cancellation watcher may be blocked in Terminate.
		killErr := tree.terminateNow(cmd)
		tree.releaseLaunch()
		waitErr := cmd.Wait()
		return newSafeProcessError("attach Git process tree", errors.Join(err, killErr, waitErr))
	}
	tree.releaseLaunch()
	return nil
}

func (tree *windowsProcessTree) attachAndResume(cmd *exec.Cmd) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	if tree.beforeAssign != nil {
		if err := tree.beforeAssign(cmd); err != nil {
			return err
		}
	}
	tree.mu.Lock()
	err = tree.assign(tree.job, process)
	if err == nil {
		tree.attached.Store(true)
	}
	tree.mu.Unlock()
	if err != nil {
		return err
	}
	threadID, err := initialThreadID(uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, threadID)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(thread)
	count, err := tree.resume(thread)
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("unexpected initial thread suspend count")
	}
	return nil
}

// CREATE_SUSPENDED prevents runtime initialization and descendant creation.
// Require the one initial thread rather than guessing which thread to resume.
func initialThreadID(pid uint32) (uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	err = windows.Thread32First(snapshot, &entry)
	var threadID uint32
	count := 0
	for err == nil {
		if entry.OwnerProcessID == pid {
			threadID = entry.ThreadID
			count++
		}
		err = windows.Thread32Next(snapshot, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return 0, err
	}
	if count != 1 || threadID == 0 {
		return 0, errors.New("expected exactly one initial process thread")
	}
	return threadID, nil
}

func (tree *windowsProcessTree) Terminate(cmd *exec.Cmd) error {
	<-tree.launchDone
	return tree.terminateNow(cmd)
}

func (tree *windowsProcessTree) terminateNow(cmd *exec.Cmd) error {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.job == 0 || cmd.Process == nil {
		return os.ErrProcessDone
	}
	if tree.attached.Load() {
		return windows.TerminateJobObject(tree.job, 1)
	}
	return cmd.Process.Kill()
}

func (tree *windowsProcessTree) Close() error {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.job == 0 {
		return nil
	}
	err := windows.CloseHandle(tree.job)
	tree.job = 0
	tree.attached.Store(false)
	return err
}
