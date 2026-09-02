# Git Integration Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish the approved Git synchronization work with native cross-platform process safety, two-device end-to-end coverage, race and secret-leak verification, reproducible developer commands, branch/PR and release CI gates, and complete synchronized documentation.

**Architecture:** Keep the Plan 1-7 runtime architecture unchanged: the system Git runner remains the only process boundary, `git.Service` remains the repository operation engine, `GitManager` remains the serialized scheduler/status owner, and the existing coordinator remains the lock-order authority. Add hardening at those existing seams, black-box two-device fixtures around the landed service contracts, native runner tests on each supported OS, and CI/documentation gates; do not add another Git implementation, queue, scheduler, credential store, transport, or user-facing capability.

**Tech Stack:** Go 1.26, system Git 2.28+, `os/exec`, `golang.org/x/sys/windows`, SQLite through `modernc.org/sqlite`, Svelte 5, Vitest 4, Vite 8, npm, GNU Make, GitHub Actions, Linux, macOS, and Windows.

---

## Scope And Dependency Freeze

Execute this plan only after Plans 1-7 from `docs/superpowers/specs/2026-09-01-git-synchronization-design.md` have landed and their focused tests pass. The implementation baseline must already contain:

- Optional per-base Git configuration, read-only probe, system-Git runner, safe errors, and persisted status.
- Coordinator-backed worktree transactions and SHA-256 note revisions.
- Idempotent connect, exact-OID manual sync, operation journal, startup recovery, and guarded Git routes.
- Conflict inspection, resolution, complete, abort, restart recovery, and mutation blocking.
- Autosync scheduling, persisted five-failure circuit breaker, pause, and explicit resume.
- Git settings/status frontend, polling, manual sync, stale-editor handling, and conflict workspace.
- Existing focused backend and frontend tests from Plans 1-7.

This plan is hardening only. It does not add stored credentials, another remote, monorepo subdirectories, rebase, force-push, LFS, submodules, arbitrary autosync intervals, system notifications, Git installation, or setup-wizard Git configuration. Existing public API shapes and user flows may be corrected only when a test in this plan proves they violate the approved design.

Before implementation, run:

```bash
npm --prefix web ci
npm --prefix web run build
go test ./...
npm --prefix web test
```

Expected: all commands exit `0`. A failure means a Plan 1-7 dependency is incomplete; repair that dependency under its original plan before starting this plan.

## Final File Map

### Platform Runner Lane

- Modify `internal/git/runner.go`: process-tree-owned start/wait lifecycle, EOF stdin verification for commands without payload, and process-tree cancellation.
- Modify `internal/git/runner_test.go`: noninteractive environment, stdin EOF, cancellation, bounded-output, and redaction regression tests.
- Modify `internal/git/errors.go`: redact complete secret variants before truncation without changing public safe-error contracts.
- Create `internal/git/process_tree_unix.go`: Linux/macOS process-group setup and termination.
- Create `internal/git/process_tree_windows.go`: Windows Job Object setup and termination.
- Create `internal/git/process_tree_unix_test.go`: native Unix parent/child cancellation assertions.
- Create `internal/git/process_tree_windows_test.go`: suspended launch ordering, immediate-child Job Object cancellation, and attach/resume cleanup assertions.
- Create `internal/git/native_matrix_test.go`: Git version, unusual base/remote/path, `.gitignore`, asset, deletion, and literal-path matrix.
- Create `internal/git/redaction_fuzz_test.go`: deterministic redaction corpus and fuzz target.

### E2E Fixture Lane

- Create `internal/git/two_device_fixture_test.go`: reusable bare remote and two-device service fixture.
- Create `internal/git/two_device_e2e_test.go`: non-conflicting two-device propagation and converged-tree scenario.
- Create `internal/git/two_device_conflict_e2e_test.go`: conflict, restart, partial resolution, complete, and no-data-loss scenario.
- Create `internal/service/git_security_race_test.go`: manager concurrency and persisted/API/log secret-leak acceptance tests.

### CI And Build Lane

- Modify `Makefile`: reproducible frontend, backend, native Git, race, vet, and aggregate verification targets.
- Modify `web/package.json`: stable CI and aggregate frontend scripts.
- Create `.github/workflows/ci.yml`: push/PR frontend, native three-OS Git, race, vet, and cross-build gates.
- Modify `.github/workflows/release.yml`: least-privilege verify/build and publish jobs, concurrency, frontend tests, race tests, and artifact handoff.

### Documentation Lane

- Modify `README.md`: current architecture, Git feature summary, prerequisites, and verified build commands.
- Modify `AGENTS.md`: mark Git synchronization complete and document current API, architecture, tests, and release gates.
- Replace `docs/api.md`: exact note revision and Git REST contracts from the approved design.
- Modify `docs/user.md`: configuration, sync, autosync, conflict, stale-buffer, credentials, and disable behavior.
- Modify `docs/developer.md`: Git architecture, process behavior, lock order, test targets, and CI/release model.
- Modify `site/index.md`: accurate launch command and optional Git synchronization feature.
- Replace `site/docs/api.md`: published copy of the API contract with Jekyll front matter.
- Modify `site/docs/user.md`: published user guidance matching `docs/user.md`.
- Modify `site/docs/developer.md`: published developer guidance matching `docs/developer.md`.
- Create `internal/docs/git_docs_test.go`: stale-claim, route, command, and mirrored-document regression tests.

No lane may edit a file owned by another lane. Workers may read all files. Integration fixes that require an owned file return to that file's owner before lane merge.

## Normative Hardening Matrix

| Area | Required proof |
|---|---|
| Optional Git | Existing non-Git tests pass without invoking `git`; native Git tests are explicitly selected in CI. |
| System Git | Real Git 2.28+ runs on Ubuntu, macOS, and Windows runners. |
| No shell | Every command remains executable plus argument slice; unusual names never become shell text. |
| Noninteractive | `GIT_TERMINAL_PROMPT=0`, locale stabilization, and EOF stdin are observed by a child process on all three OS families. |
| Process tree | Context cancellation terminates the Git process and a spawned descendant on Linux, macOS, and Windows; Windows proves the suspended process is assigned to its Job Object before its first instruction can spawn a child. |
| Files | Spaces, Unicode, leading dash, tab/newline where supported, pathspec-looking names, assets, ignored files, non-Markdown files, rename, and deletion retain exact bytes and names. |
| Two devices | One bare remote and two working bases converge after create/edit/rename/delete/asset/non-overlapping changes. |
| Conflict | Conflicting bytes remain recoverable, no push occurs, restart preserves stages, explicit resolution completes, and both devices converge. |
| Race safety | Full Go suite passes `-race`; manager stress covers manual, scheduled, resume, switch/save, conflict, and close interleavings. |
| Secret safety | Unique credentials are absent from safe errors, response JSON, logs, status, operation journal, config, commit messages, and raw metadata DB bytes. |
| CI | Branch pushes and pull requests run frontend, native Git matrix, race, vet, and release-target builds. |
| Release | Tag job runs the same verification, keeps write permission out of build steps, and publishes only verified artifacts. |
| Documentation | API, user, developer, site, README, and AGENTS contain no “Git unavailable/soon” claims and list the exact shipped behavior. |

## Parallel Dispatch Waves

### Wave 0: Serial Baseline Freeze

The coordinator runs the dependency commands above, records the Plan 7 baseline commit with `git rev-parse HEAD`, and confirms these paths exist before dispatch:

```text
internal/git/runner.go
internal/git/service.go
internal/git/sync.go
internal/git/conflict_resolution.go
internal/service/git_manager.go
internal/service/git_scheduler.go
internal/handlers/git_routes.go
web/src/lib/api.js
web/src/lib/settings
.github/workflows/release.yml
```

Expected: every path exists, including the Plan 7 scheduler at exactly `internal/service/git_scheduler.go`. A missing path means the corresponding dependency plan is incomplete; stop and finish that plan rather than substituting a filename in this plan.

### Wave 1: Four Exclusive Lanes

Dispatch all four workers from the exact Wave 0 commit:

| Lane | Tasks | Exclusive ownership |
|---|---|---|
| Platform runner | Tasks 1-2 | `internal/git/runner.go`, `runner_test.go`, `errors.go`, `process_tree_*`, `native_matrix_test.go`, `redaction_fuzz_test.go` |
| E2E fixtures | Tasks 3-4 | `internal/git/two_device_*`, `internal/service/git_security_race_test.go` |
| CI/build | Tasks 5-6 | `Makefile`, `web/package.json`, `.github/workflows/ci.yml`, `.github/workflows/release.yml` |
| Documentation | Tasks 7-9 | `README.md`, `AGENTS.md`, `docs/*.md`, `site/**/*.md`, `internal/docs/git_docs_test.go` |

Each lane runs its focused tests and commits independently. Workers do not merge, rebase, reset, or modify another lane's files.

### Wave 2: Serial Integration

Merge the four lane commits one at a time. After each merge run:

```bash
npm --prefix web run build
go test ./...
```

Expected: exit `0` after every merge. Resolve an integration failure in the lane that owns the failing file; do not make opportunistic cross-lane edits.

### Wave 3: Serial Final Verification

Task 10 is strictly serial after all lane commits are integrated. Do not run final race, fuzz, workflow, documentation, or release-target verification concurrently: the final evidence must correspond to one immutable worktree state.

### Task 1: Harden Noninteractive Process-Tree Execution

**Files:**
- Modify: `internal/git/runner.go`
- Modify: `internal/git/runner_test.go`
- Create: `internal/git/process_tree_unix.go`
- Create: `internal/git/process_tree_windows.go`
- Create: `internal/git/process_tree_unix_test.go`
- Create: `internal/git/process_tree_windows_test.go`

- [ ] **Step 1: Add RED common runner tests**

Add helper modes to the existing `TestGitRunnerHelper` rather than introducing an external shell script. The helper must support:

```go
const (
	runnerHelperEnvironment = "environment"
	runnerHelperSpawnChild  = "spawn-child"
	runnerHelperBlock       = "block"
)
```

Add these exact tests to `internal/git/runner_test.go`:

```go
func TestCommandRunnerSuppliesNoninteractiveEnvironmentAndEOFStdin(t *testing.T)
func TestCommandRunnerCancellationWaitsForProcessTree(t *testing.T)
func TestCommandRunnerKeepsExplicitStdin(t *testing.T)
```

`TestCommandRunnerSuppliesNoninteractiveEnvironmentAndEOFStdin` runs the test binary through the injected command factory with `Command.Stdin == nil`, returns JSON containing `GIT_TERMINAL_PROMPT`, `LC_ALL`, `GIT_OPTIONAL_LOCKS`, and the result of one-byte stdin read, and asserts:

```go
want := helperEnvironment{
	GitTerminalPrompt: "0",
	Locale:            "C",
	OptionalLocks:     "0",
	StdinEOF:          true,
}
```

The explicit-stdin test passes `strings.NewReader("payload")` through the landed `Command.Stdin` and requires the helper to read exactly `payload`. Keep nil `exec.Cmd.Stdin` nil: Go connects it to `os.DevNull`, which produces immediate EOF without starting an stdin-copy goroutine. Do not replace nil with a pipe or `bytes.Reader`.

- [ ] **Step 2: Run the common tests RED**

Run:

```bash
go test ./internal/git -run 'TestCommandRunner(SuppliesNoninteractive|CancellationWaits|KeepsExplicitStdin)' -count=1 -v
```

Expected: at least `TestCommandRunnerCancellationWaitsForProcessTree` fails because the landed `exec.CommandContext` cancellation kills only the direct process; no test may hang longer than five seconds.

- [ ] **Step 3: Freeze the process-controller contract**

Add this unexported contract to `internal/git/runner.go`:

```go
const processWaitDelay = 5 * time.Second

type processTree interface {
	Start(*exec.Cmd) error
	Terminate(*exec.Cmd) error
	Close() error
}

type processTreeFactory func() (processTree, error)
```

Extend `CommandRunner` with `newProcessTree processTreeFactory`. `NewCommandRunner` and the existing test constructor must default it to `newProcessTree`. Tests that only inspect argv use a no-op controller whose `Start` calls `cmd.Start`; native process-tree tests always inject or use the real platform controller.

`Start` owns the platform launch transition. Its contract is strict:

1. On success, the process is runnable, belongs to its Unix process group or Windows Job Object, and the caller must invoke `cmd.Wait` exactly once.
2. An ordinary `cmd.Start` failure means no process was created and remains eligible for the landed `*exec.Error -> git_unavailable` classification.
3. A failure after `cmd.Start` succeeds must terminate and reap the process inside `Start`, then return a fixed-message `SafeError`; the caller must not invoke `cmd.Wait` again.
4. `Terminate` must be safe while `Start` is in progress because `exec.Cmd` can start its context watcher before `Start` returns. Windows gates that watcher until Job Object assignment succeeds or setup cleanup takes ownership; Unix process-group creation is atomic with process creation and needs no gate.

Do not expose separate `Configure` and `Attach` calls to the runner. That split permits a future caller to launch a runnable Windows process before Job Object assignment and recreates the descendant-escape race this task closes.

- [ ] **Step 4: Replace direct `Run` with process-tree-owned start handling**

Keep all landed canonical-directory, timeout, bounded-output, environment, secret, and classification logic. Replace only process execution with this sequence:

```go
tree, err := r.newProcessTree()
if err != nil {
	return Result{}, newSafeProcessError("prepare Git process tree", err)
}
defer tree.Close()

cmd := r.command(runCtx, r.executable, append([]string(nil), command.Args...)...)
cmd.Dir = canonicalDir
cmd.Env = gitEnvironment(os.Environ(), command.ReadOnly)
cmd.Stdout = &stdout
cmd.Stderr = &stderr
if command.Stdin != nil {
	cmd.Stdin = command.Stdin
}
cmd.Cancel = func() error { return tree.Terminate(cmd) }
cmd.WaitDelay = processWaitDelay

runErr := tree.Start(cmd)
if runErr == nil {
	runErr = cmd.Wait()
}
if runCtx.Err() != nil {
	runErr = runCtx.Err()
}
```

The `runCtx.Err()` check ensures cancellation or timeout that races a Windows attach/resume failure retains the landed `git_canceled` or `git_timeout` result. Otherwise, `newSafeProcessError` returns the existing fixed `git_command_failed` safe message, wraps the cause privately, and never includes a PID, path, command, environment, or diagnostic. Preserve `*exec.Error -> git_unavailable` and `*exec.ExitError` handling in the existing classifier after this block.

Do not change `commandFactory`, argument cloning, `cmd.Env`, stdin, bounded stdout/stderr, secret collection, redaction-before-truncation, or classification around this block. In particular, the Windows controller receives the already-built `*exec.Cmd`; it never composes a shell command or reconstructs argv, environment, or credentials.

Implement it exactly as:

```go
func newSafeProcessError(action string, err error) *SafeError {
	return &SafeError{
		Code:    CodeCommandFailed,
		Message: "Git command failed",
		cause:   fmt.Errorf("%s: %w", action, err),
	}
}
```

Call it only with the fixed actions `prepare Git process tree` and `attach Git process tree`; never pass a command argument, path, URL, PID, or diagnostic as `action`. A `SafeError` returned by `processTree.Start` passes through the landed classifier unchanged.

- [ ] **Step 5: Implement Unix process-group ownership**

Create `internal/git/process_tree_unix.go`:

```go
//go:build linux || darwin

package git

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type nativeProcessTree struct{}

func newProcessTree() (processTree, error) { return nativeProcessTree{}, nil }

func (nativeProcessTree) Start(cmd *exec.Cmd) error {
	attr := &syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		*attr = *cmd.SysProcAttr
	}
	attr.Setpgid = true
	cmd.SysProcAttr = attr
	return cmd.Start()
}

func (nativeProcessTree) Terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func (nativeProcessTree) Close() error { return nil }
```

Do not send a signal to PID `0` or a positive PID. The negative PID targets only the process group created for this command.

- [ ] **Step 6: Implement Windows Job Object ownership**

Create `internal/git/process_tree_windows.go` with build tag `windows`. Use `golang.org/x/sys/windows`, already a direct dependency through `golang.org/x/sys`. Do not attempt to cast or reflect into `cmd.Process`: Go's Windows `os.Process` retains its process handle privately, and `os.StartProcess` closes the primary thread handle before `exec.Cmd.Start` returns. `CREATE_SUSPENDED` in `SysProcAttr.CreationFlags` is therefore insufficient by itself because there is no supported `exec.Cmd` field from which to call `ResumeThread`.

Use this valid `exec.Cmd`-compatible strategy instead: launch suspended through `cmd.Start`, reopen the still-referenced process by PID, assign it to the Job Object, enumerate the process's threads while its primary thread has never run, require exactly one owner thread, open that sole initial/primary thread, and resume it. If another component has injected a second thread, fail closed rather than guessing which thread is primary. The complete implementation is:

```go
//go:build windows

package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type nativeProcessTree struct {
	job          windows.Handle
	attached     atomic.Bool
	launchDone   chan struct{}
	launchOnce   sync.Once
	beforeAssign func(uint32) error
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
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	return &nativeProcessTree{
		job:        job,
		launchDone: make(chan struct{}),
		assign:     windows.AssignProcessToJobObject,
		resume:     windows.ResumeThread,
	}, nil
}

func (p *nativeProcessTree) releaseLaunch() {
	p.launchOnce.Do(func() { close(p.launchDone) })
}

func (p *nativeProcessTree) Start(cmd *exec.Cmd) error {
	attr := &syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		*attr = *cmd.SysProcAttr
	}
	attr.CreationFlags |= windows.CREATE_SUSPENDED |
		windows.CREATE_NEW_PROCESS_GROUP |
		windows.CREATE_NO_WINDOW
	cmd.SysProcAttr = attr

	if err := cmd.Start(); err != nil {
		p.releaseLaunch()
		return err
	}

	fail := func(cause error) error {
		terminateErr := p.terminateNow(cmd)
		p.releaseLaunch()
		if errors.Is(terminateErr, os.ErrProcessDone) {
			terminateErr = nil
		}
		waitErr := cmd.Wait()
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			waitErr = nil
		}
		return newSafeProcessError(
			"attach Git process tree",
			errors.Join(cause, terminateErr, waitErr),
		)
	}

	pid := uint32(cmd.Process.Pid)
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		pid,
	)
	if err != nil {
		return fail(err)
	}
	defer windows.CloseHandle(process)

	if p.beforeAssign != nil {
		if err := p.beforeAssign(pid); err != nil {
			return fail(err)
		}
	}
	if err := p.assign(p.job, process); err != nil {
		return fail(err)
	}
	p.attached.Store(true)

	thread, err := openSoleInitialThread(pid)
	if err != nil {
		return fail(err)
	}
	defer windows.CloseHandle(thread)

	previousSuspendCount, err := p.resume(thread)
	if err != nil {
		return fail(err)
	}
	if previousSuspendCount != 1 {
		return fail(fmt.Errorf("unexpected primary thread suspend count: %d", previousSuspendCount))
	}
	p.releaseLaunch()
	return nil
}

func openSoleInitialThread(pid uint32) (windows.Handle, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	var threadID uint32
	err = windows.Thread32First(snapshot, &entry)
	for err == nil {
		if entry.OwnerProcessID == pid {
			if threadID != 0 {
				return 0, errors.New("suspended process has more than one initial thread")
			}
			threadID = entry.ThreadID
		}
		err = windows.Thread32Next(snapshot, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return 0, err
	}
	if threadID == 0 {
		return 0, errors.New("suspended process has no initial thread")
	}
	return windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, threadID)
}

func (p *nativeProcessTree) Terminate(cmd *exec.Cmd) error {
	<-p.launchDone
	return p.terminateNow(cmd)
}

func (p *nativeProcessTree) terminateNow(cmd *exec.Cmd) error {
	if p.job != 0 && p.attached.Load() {
		if err := windows.TerminateJobObject(p.job, 1); err == nil {
			return nil
		} else if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return err
		}
	}
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}

func (p *nativeProcessTree) Close() error {
	if p.job == 0 {
		return nil
	}
	err := windows.CloseHandle(p.job)
	p.job = 0
	p.attached.Store(false)
	return err
}
```

The Job Object is created before `cmd.Start`. `CREATE_SUSPENDED` guarantees Git cannot execute or spawn before `AssignProcessToJobObject`; only the sole initial thread is resumed, and only after successful assignment. The reopened process and thread handles are always closed, while `exec.Cmd` retains its own private process handle for its normal `Wait` and pipe lifecycle. `attached` is set only after successful assignment. `launchDone` prevents the `exec.CommandContext` watcher from killing the process while `Start` reopens the PID and assigns the job; setup failure uses `terminateNow`, releases the watcher, then calls `cmd.Wait` exactly once, so cancellation cannot deadlock cleanup or create PID-reuse ambiguity. Closing the kill-on-close job after normal `Wait` removes descendants that outlive Git. Do not invoke `taskkill`, PowerShell, `cmd.exe`, WMI, a shell, `unsafe` reflection into `os.Process`, or a second custom `CreateProcess` path that would have to duplicate `os/exec` quoting, environment, pipe, context, and wait semantics.

- [ ] **Step 7: Add native parent/child and Windows launch-failure tests**

Both platform test files use the current test executable as parent and child. The parent starts the child with argument slices, writes both PIDs to a JSON file under a directory containing spaces, waits until the file is readable, and then blocks. The test cancels context, waits for `runner.Run`, and polls for at most five seconds until neither PID is alive.

Use these exact test names:

```go
func TestCommandRunnerCancellationTerminatesUnixProcessGroup(t *testing.T)
func TestWindowsProcessTreeAssignsBeforeImmediateChildAndCancelsJob(t *testing.T)
func TestWindowsProcessTreeAssignmentFailureKillsAndReapsSuspendedProcess(t *testing.T)
func TestWindowsProcessTreeResumeFailureTerminatesJobAndReapsProcess(t *testing.T)
```

`TestWindowsProcessTreeAssignsBeforeImmediateChildAndCancelsJob` is deterministic and Windows-only. Inject a real `*nativeProcessTree` whose `beforeAssign` callback checks that the helper's `started` marker does not exist while `cmd.Start` has returned but the process is still suspended. The helper's first mode action creates that marker, immediately starts a child copy of the test executable with an argument slice, atomically writes parent/child PIDs to JSON, and blocks. After `Start` assigns and resumes, require both the marker and PID file, cancel the runner context, wait for `Run`, and require both PIDs stopped. This is not a timing contest: the callback observes the exact post-create/pre-assign point, and successful PID publication proves the child was created immediately after resume inside the already-owned job.

The two error tests inject only `nativeProcessTree.assign` or `nativeProcessTree.resume` with a fixed sentinel error; every other Windows API remains real. In each case capture the `*exec.Cmd` from the command factory and assert:

1. `Run` returns `git_command_failed`, with no sentinel, path, PID, argv, environment, or diagnostic in `Error()`, JSON, `%v`, or `%+v`.
2. The helper's first-action marker never appears, proving no suspended command executed on either failure path.
3. `cmd.ProcessState != nil`, a second `cmd.Wait()` reports it was already called, and the direct PID is no longer active.
4. Assignment failure leaves `attached == false` and uses direct-process kill; resume failure has `attached == true` and uses Job Object termination.
5. `Close` clears the job handle and is idempotent; all callbacks and test goroutines finish under a five-second deadline.

Unix liveness uses `syscall.Kill(pid, 0)` and treats `ESRCH` as stopped. Windows liveness opens with `windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE`, calls `windows.WaitForSingleObject(handle, 0)`, and treats `uint32(windows.WAIT_TIMEOUT)` as active and `windows.WAIT_OBJECT_0` as stopped. Register `t.Cleanup` before starting each helper so a failed assertion terminates the process/job and reaps the direct process. Never leave a helper running after failure.

- [ ] **Step 8: Run native and cross-compile verification**

Run on the current native platform:

```bash
go test ./internal/git -run 'Test(CommandRunner(CancellationTerminates|SuppliesNoninteractive|CancellationWaits|KeepsExplicitStdin)|WindowsProcessTree)' -count=10 -v
```

Then run on Linux:

```bash
GOOS=windows GOARCH=amd64 go test -c ./internal/git -o /tmp/igonotes-git-windows.test.exe
GOOS=darwin GOARCH=amd64 go test -c ./internal/git -o /tmp/igonotes-git-darwin.test
```

Expected: native tests pass ten times; both foreign test binaries compile. The foreign binaries are not executed on Linux.

- [ ] **Step 9: Commit process-tree hardening**

```bash
git add internal/git/runner.go internal/git/runner_test.go internal/git/process_tree_unix.go internal/git/process_tree_windows.go internal/git/process_tree_unix_test.go internal/git/process_tree_windows_test.go
git commit -m "fix: terminate git process trees safely"
```

### Task 2: Verify Native Git, Unusual Paths, And Redaction

**Files:**
- Modify: `internal/git/errors.go`
- Modify: `internal/git/runner_test.go`
- Create: `internal/git/native_matrix_test.go`
- Create: `internal/git/redaction_fuzz_test.go`

- [ ] **Step 1: Write RED redaction-boundary tests**

Add these tests:

```go
func TestCommandRunnerRedactsSecretVariantsBeforeBounding(t *testing.T)
func TestSafeErrorNeverFormatsDiagnosticOrCause(t *testing.T)
func TestRedactGitDiagnosticCorpus(t *testing.T)
```

Use this exact corpus, with a unique marker generated per subtest and substituted for `secret`:

```text
https://user:secret@example.invalid/notes.git
fatal: Authentication failed for 'https://user:secret@example.invalid/notes.git/'
remote: token=secret
Authorization: Basic secret
git@example.invalid:secret/repo.git
```

Pass the complete remote, userinfo, password/token, and authorization value through `Command.Secrets`. Place one occurrence across the diagnostic capture limit. Assert none of the complete values or any trailing prefix of at least four bytes appears in `Result.Stderr`, `SafeError.Error()`, `SafeError.Diagnostic()`, `%v`, `%+v`, or the JSON form of the public API error.

- [ ] **Step 2: Run redaction tests RED**

Run:

```bash
go test ./internal/git -run 'Test(CommandRunnerRedactsSecretVariants|SafeErrorNeverFormats|RedactGitDiagnosticCorpus)' -count=1 -v
```

Expected: the normalized URL or boundary case fails against the Plan 1 exact-string-only sanitizer.

- [ ] **Step 3: Derive and redact bounded secret variants**

In `internal/git/errors.go`, retain HTTP userinfo stripping and add:

```go
func secretVariants(secrets []string) []string {
	set := make(map[string]struct{}, len(secrets)*4)
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		set[secret] = struct{}{}
		if parsed, err := url.Parse(secret); err == nil && parsed.User != nil {
			set[parsed.User.String()] = struct{}{}
			set[parsed.User.Username()] = struct{}{}
			if password, ok := parsed.User.Password(); ok {
				set[password] = struct{}{}
			}
		}
	}
	variants := make([]string, 0, len(set))
	for value := range set {
		if len(value) >= 4 {
			variants = append(variants, value)
		}
	}
	sort.Slice(variants, func(i, j int) bool {
		if len(variants[i]) == len(variants[j]) {
			return variants[i] < variants[j]
		}
		return len(variants[i]) > len(variants[j])
	})
	return variants
}
```

Redact longest variants first, then redact HTTP(S) userinfo, then truncate. Do not persist variants, expose them through `SafeError`, or redact arbitrary short words. Candidate HTTP(S) URLs with userinfo/query/fragment remain rejected by the landed validator before process execution.

- [ ] **Step 4: Add native Git requirement and unusual-path matrix**

Create `internal/git/native_matrix_test.go` with:

```go
func requireNativeGit(t *testing.T) string
func nativeGit(t *testing.T, dir string, stdin io.Reader, args ...string) []byte
func supportsFilename(t *testing.T, root, name string) bool
func TestNativeGitVersionIsSupported(t *testing.T)
func TestNativeGitRunnerPreservesUnusualPaths(t *testing.T)
func TestNativeGitRunnerHonorsIgnoreAssetsAndDeletes(t *testing.T)
```

`requireNativeGit` executes `git --version`, parses it through the landed parser, and behaves as follows:

```go
if err != nil || !version.Supported() {
	if os.Getenv("IGONOTES_REQUIRE_GIT_INTEGRATION") == "1" {
		t.Fatalf("native Git 2.28+ is required: version=%v error=%v", version, err)
	}
	t.Skip("native Git 2.28+ is unavailable")
}
```

Use a bare remote path and working root containing `IGoNotes native matrix`, Cyrillic text, `#`, `%`, and square brackets. Test this exact relative-name set after capability probing each name with `os.WriteFile` and `os.Remove`:

```go
names := []string{
	"space name.md",
	"unicode-пример.md",
	"-leading-dash.md",
	"dollar-$(not-a-command).md",
	"semi;colon & ampersand.md",
	"single'quote.md",
	":(glob)*.md",
	"brackets/[draft] #1%.md",
	"tab\tname.md",
	"line\nbreak.md",
	"assets/images/image #1.bin",
	"data/settings.json",
}
```

Every supported name must round-trip through `git add --all -- .`, commit, push, clone, and `git ls-files -z` byte-for-byte. A capability probe may skip only a filename that `os.WriteFile` rejects on that native filesystem; it must log only the escaped relative name and OS error class, never an absolute path. `.gitignore` contains `ignored-secret.txt`; assert that file remains local and absent from the index and clone. Delete one Markdown file and the binary asset, commit through `add --all -- .`, push, and assert both disappear in a fresh clone. All commands go through the landed `CommandRunner`; the test may use `os/exec` only for the initial `git --version` availability probe.

- [ ] **Step 5: Add deterministic fuzz coverage**

Create `internal/git/redaction_fuzz_test.go`:

```go
func FuzzRedactGitDiagnostic(f *testing.F) {
	seeds := []struct {
		diagnostic string
		secret     string
	}{
		{"fatal: https://user:token@example.invalid/repo.git", "token"},
		{"prefix Authorization: Basic abcdef suffix", "abcdef"},
		{"git@example.invalid:path/secret.git", "secret"},
	}
	for _, seed := range seeds {
		f.Add(seed.diagnostic, seed.secret)
	}
	f.Fuzz(func(t *testing.T, diagnostic, secret string) {
		if len(secret) < 4 || len(secret) > 256 || len(diagnostic) > 64*1024 {
			return
		}
		redacted := redact(diagnostic, []string{secret})
		if strings.Contains(redacted, secret) {
			t.Fatalf("redacted diagnostic retained secret of length %d", len(secret))
		}
	})
}
```

Never print the fuzz inputs on failure.

- [ ] **Step 6: Run platform hardening GREEN**

Run:

```bash
gofmt -w internal/git/errors.go internal/git/runner_test.go internal/git/native_matrix_test.go internal/git/redaction_fuzz_test.go
IGONOTES_REQUIRE_GIT_INTEGRATION=1 go test ./internal/git -run 'Test(NativeGit|CommandRunnerRedactsSecretVariants|SafeErrorNeverFormats|RedactGitDiagnosticCorpus)' -count=1 -v
go test ./internal/git -run '^$' -fuzz=FuzzRedactGitDiagnostic -fuzztime=10s
```

Expected: native Git version and path tests pass, ignored bytes never enter Git, deletion propagates, no secret is printed, and fuzzing exits without a panic or leak assertion.

- [ ] **Step 7: Commit native matrix and redaction**

```bash
git add internal/git/errors.go internal/git/runner_test.go internal/git/native_matrix_test.go internal/git/redaction_fuzz_test.go
git commit -m "test: harden native git path and redaction matrix"
```

### Task 3: Add The Two-Device Convergence Fixture And Scenario

**Files:**
- Create: `internal/git/two_device_fixture_test.go`
- Create: `internal/git/two_device_e2e_test.go`

- [ ] **Step 1: Write the RED scenario against an undefined fixture**

Create `internal/git/two_device_e2e_test.go` with this exact test outline:

```go
func TestTwoDeviceEndToEndConvergesCreateEditRenameDeleteAndAssets(t *testing.T) {
	fixture := newTwoDeviceFixture(t)
	fixture.connectFirstDevice(map[string][]byte{
		"shared.md":      []byte("shared v1\n"),
		"delete-me.md":   []byte("delete me\n"),
		"folder/local.md": []byte("local v1\n"),
	})
	fixture.connectSecondDevice()

	fixture.write("one", "created on one.md", []byte("device one\n"))
	fixture.write("one", "assets/images/pixel #1.bin", []byte{0, 1, 2, 3, 255})
	fixture.write("one", "data.json", []byte("{\"device\":1}\n"))
	fixture.sync("one")
	fixture.sync("two")

	fixture.rename("two", "created on one.md", "renamed [two].md")
	fixture.remove("two", "delete-me.md")
	fixture.write("two", "folder/remote.md", []byte("device two\n"))
	fixture.write("one", "folder/local.md", []byte("local v2\n"))

	fixture.sync("one")
	fixture.sync("two")
	fixture.sync("one")

	fixture.assertDevicesAndRemoteConverged()
	fixture.assertFile("one", "renamed [two].md", []byte("device one\n"))
	fixture.assertFile("one", "folder/local.md", []byte("local v2\n"))
	fixture.assertFile("one", "folder/remote.md", []byte("device two\n"))
	fixture.assertFile("one", "assets/images/pixel #1.bin", []byte{0, 1, 2, 3, 255})
	fixture.assertAbsent("one", "delete-me.md")
}
```

- [ ] **Step 2: Run the scenario RED**

Run:

```bash
go test ./internal/git -run '^TestTwoDeviceEndToEndConverges' -count=1 -v
```

Expected: compile failure containing `undefined: newTwoDeviceFixture`.

- [ ] **Step 3: Implement the reusable fixture over landed service contracts**

Create `internal/git/two_device_fixture_test.go` in package `git`. Define:

```go
type twoDeviceFixture struct {
	t       *testing.T
	remote  string
	service *Service
	devices map[string]*twoDevice
	nextID  uint64
}

type twoDevice struct {
	name          string
	path          string
	snapshot      ConfiguredBase
	lastRemoteOID string
}

func newTwoDeviceFixture(*testing.T) *twoDeviceFixture
func (f *twoDeviceFixture) connectFirstDevice(map[string][]byte)
func (f *twoDeviceFixture) connectSecondDevice()
func (f *twoDeviceFixture) write(string, string, []byte)
func (f *twoDeviceFixture) rename(string, string, string)
func (f *twoDeviceFixture) remove(string, string)
func (f *twoDeviceFixture) sync(string) OperationResult
func (f *twoDeviceFixture) syncAttempt(string) (Operation, OperationResult, error)
func (f *twoDeviceFixture) assertDevicesAndRemoteConverged()
func (f *twoDeviceFixture) assertFile(string, string, []byte)
func (f *twoDeviceFixture) assertAbsent(string, string)
```

Fixture rules:

1. Call `requireNativeGit`; create a bare repository whose path contains spaces and Unicode.
2. Set a test-local `HOME`, `USERPROFILE`, and `XDG_CONFIG_HOME`; write `.gitconfig` with only `user.name=IGoNotes E2E`, `user.email=e2e@example.invalid`, `commit.gpgsign=false`, and `init.defaultBranch=main`.
3. Construct exactly one landed `CommandRunner`, `Client`, and `Service`; do not invoke a shell.
4. Use branch `main`, a local-path remote, and commit template `IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)`.
5. Generate operation IDs as 32 lowercase hexadecimal characters with `fmt.Sprintf("%032x", atomic.AddUint64(&f.nextID, 1))`.
6. Use `WorktreeTransaction` that verifies the callback path equals the device canonical path and invokes the callback directly.
7. `connectFirstDevice` writes initial bytes and calls `Service.Initialize` with a probe containing `HasRepository:false`, `RepositoryRootMatches:true`, `EmptyRemote:true`, `CanConfigure:true`, and required mutations `CreateRepository`, `AddOrigin`, and `CreateBranch`. Pass all four confirmations as true, then store the successful `OperationResult.RemoteOID`.
8. `connectSecondDevice` starts empty and calls `Service.Initialize` with a probe containing `HasRepository:false`, `RepositoryRootMatches:true`, `RemoteBranches:[]string{"main"}`, `EmptyRemote:false`, `CanConfigure:true`, and required mutations `CreateRepository` and `AddOrigin`. Store its successful remote OID and require its checked-out bytes to equal device one's initial tree before continuing.
9. `syncAttempt` creates the complete `Operation`, calls `Service.Sync` with the device's last trusted OID, and applies every `Checkpoint` field to its returned operation in the progress callback. It updates the trusted OID only from a nonempty successful result. `sync` wraps `syncAttempt` and fails the test on error.
10. File helpers use `os.Root` with slash-clean local paths; they reject absolute paths and `.git` components.
11. Convergence compares `git rev-parse HEAD^{commit}`, `git ls-tree -r --full-tree -z HEAD`, and every blob OID for both devices and a fresh verification clone. It also requires `git status --porcelain=v1 -z` to be empty on both devices.

Build test snapshots with the exact Plan 3 length-prefixed fingerprint rule rather than importing `internal/service` and creating a package cycle:

```go
func e2eFingerprint(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_ = binary.Write(hash, binary.BigEndian, uint64(len(value)))
		_, _ = io.WriteString(hash, value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func e2eConfiguredBase(name, root, remote string) ConfiguredBase {
	base := ConfiguredBase{
		Name:           name,
		Path:           root,
		URL:            remote,
		Branch:         "main",
		CommitTemplate: "IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)",
	}
	base.Fingerprint = e2eFingerprint(
		base.Name, base.Path, base.URL, base.Branch,
		strconv.FormatBool(base.AutoSync), strconv.Itoa(base.IntervalMinutes), base.CommitTemplate,
	)
	base.RemoteFingerprint = e2eFingerprint(base.Path, base.URL, base.Branch)
	return base
}
```

Define one fixture-local `e2eNULFields([]byte) ([]string, error)` parser that requires a final NUL, rejects empty interior fields, and preserves every non-NUL byte; use it for `ls-files`, `ls-tree`, `status`, and changed-path output. Test diagnostics may print escaped relative paths and OIDs, but never remote URLs, absolute test paths, file contents, or environment values.

- [ ] **Step 4: Run the convergence scenario GREEN**

Run:

```bash
gofmt -w internal/git/two_device_fixture_test.go internal/git/two_device_e2e_test.go
IGONOTES_REQUIRE_GIT_INTEGRATION=1 go test ./internal/git -run '^TestTwoDeviceEndToEndConverges' -count=3 -v
```

Expected: three passes; both device heads and the fresh remote clone have identical trees and all asserted bytes.

- [ ] **Step 5: Commit the fixture and convergence scenario**

```bash
git add internal/git/two_device_fixture_test.go internal/git/two_device_e2e_test.go
git commit -m "test: add two device git convergence scenario"
```

### Task 4: Add Conflict Restart, Race, And Secret Acceptance

**Files:**
- Create: `internal/git/two_device_conflict_e2e_test.go`
- Create: `internal/service/git_security_race_test.go`

- [ ] **Step 1: Write the conflict/restart E2E test**

Add this exact test name and phases:

```go
func TestTwoDeviceEndToEndConflictSurvivesRestartAndLosesNoData(t *testing.T)
```

Phases:

1. Connect both devices with `conflict.md` containing `base\n` and `partial.md` containing `partial base\n`.
2. Device one writes `from one\n` and `partial one\n`, then syncs successfully.
3. Device two writes `from two\n` and `partial two\n`, then calls `syncAttempt`; declare `var conflictErr *ConflictError`, require `errors.As(err, &conflictErr)`, require the exact sorted paths `conflict.md` and `partial.md`, require `MERGE_HEAD`, and require no push.
4. Assert the bare remote still contains exactly `from one\n`; materialize stage 1/2/3 and require `base\n`, `from two\n`, and `from one\n` respectively.
5. Call `Service.Conflicts`, locate `partial.md` by exact path, and call `Service.ResolveConflict` with its returned conflict ID, original operation ID, action `manual`, result path `partial.md`, and content `partial one\npartial two\n`. Require `partial.md` absent from the returned unmerged list while `conflict.md` remains.
6. Discard the `Service` instance, construct a fresh runner/client/service, and call `RecoverLocal(ctx, RecoveryOptions{Snapshot: device.snapshot, Operation: &operation})`. Require `RecoveryConflict`, require only `conflict.md` in `ConflictPaths`, and require the stage-0 bytes of `partial.md` still equal `partial one\npartial two\n`.
7. Call the fresh service's `Conflicts`, resolve `conflict.md` with action `manual`, result path `conflict.md`, and content `from one\nfrom two\n`, then require `CanComplete:true` and no conflicts. Call `CompleteConflict` with the original conflict operation, the fixture worktree transaction, and the same checkpoint accumulator used by sync; require an exact two-parent merge commit and exact-OID push.
8. Sync device one again; assert both devices and a fresh clone have identical trees and the exact merged bytes.
9. Scan `git reflog --all`, branch history, and working trees to prove both original side commits remain reachable and neither side's bytes were silently selected or discarded.

Build the conflict `Operation` from the failed sync's operation/options/result and landed `ConflictError`; do not infer OIDs from mutable `HEAD`, `FETCH_HEAD`, or branch names. Use the existing fixture's operation-ID and transaction helpers.

- [ ] **Step 2: Run conflict E2E GREEN**

Run:

```bash
gofmt -w internal/git/two_device_conflict_e2e_test.go
IGONOTES_REQUIRE_GIT_INTEGRATION=1 go test ./internal/git -run '^TestTwoDeviceEndToEndConflict' -count=3 -v
```

Expected: three passes with preserved stages across service reconstruction, exact merge parents, no pre-resolution push, and converged final trees.

- [ ] **Step 3: Add manager race acceptance tests**

Create `internal/service/git_security_race_test.go` with these exact tests:

```go
func TestGitManagerRaceManualScheduledResumeSwitchSaveAndClose(t *testing.T)
func TestGitManagerRaceConflictResolvePollAndRestart(t *testing.T)
func TestGitSecretsNeverReachPublicOrPersistentSinks(t *testing.T)
```

The race fixture uses the landed fake clock/scheduler seam from Plan 7, concrete temporary SQLite repositories, one manager worker, and channels in the blocking fake `git.Runner` to hold fetch, worktree, and push stages. Across 50 iterations it concurrently issues:

```text
manual QueueSync
scheduled interval tick
status polling
resume request
active-base save with expected revision
switch request to the other base
manager Close on the final iteration
```

Required assertions:

- At most one Git operation enters the service at a time across all bases.
- Same-path manual/scheduled requests share one operation ID.
- Save either completes before the worktree transaction or waits and receives `note_changed`; it never silently overwrites incoming bytes.
- Switch waits for coordinator release and observes the rebuilt index.
- Conflict immediately blocks mutations and is not counted as one of five operational failures.
- Close cancels the active network command, starts no queued successor, and leaves a recoverable persisted status.
- No goroutine remains blocked; every spawned goroutine reports through a buffered result channel with a five-second test deadline.

- [ ] **Step 4: Add end-to-end secret sink scanning**

`TestGitSecretsNeverReachPublicOrPersistentSinks` generates `secret := "IGONOTES_SECRET_" + strings.Repeat("7", 32)` and `unsafeRemote := "https://user:" + secret + "@example.invalid/notes.git"`. It verifies two paths:

1. Candidate configuration rejects the URL before queueing or process execution.
2. A fake external Git origin/credential-helper failure emits the raw URL, token, and `"Authorization: Basic " + secret` through stderr while the runner lists all complete values in `Secrets`.

Capture and scan:

```text
SafeError.Error and Diagnostic
handler JSON bodies for probe/config/sync/status/resume/conflicts
application logger bytes
model.GitStatus and git.Operation JSON
config.json bytes
metadata.db bytes after WAL checkpoint
metadata.db-wal and metadata.db-shm bytes when either file exists after checkpoint
all commit subjects and bodies
```

Use one assertion helper that reports only the sink name:

```go
func assertSecretAbsent(t *testing.T, sink string, data []byte, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(data, []byte(secret)) {
			t.Fatalf("secret leaked through %s", sink)
		}
	}
}
```

Do not include the secret in an assertion message, test name, temporary path, environment variable name, golden file, or commit message.

Execute `PRAGMA wal_checkpoint(TRUNCATE)` through the open test database before reading persistence files, close the database, then scan `metadata.db`, `metadata.db-wal`, and `metadata.db-shm` when present. Scan `config.json` before cleanup and inspect commit messages with NUL-delimited `git log --format=%B%x00`; never invoke the Git CLI directly from this service test.

- [ ] **Step 5: Run service tests and race detector**

Run:

```bash
gofmt -w internal/service/git_security_race_test.go
go test ./internal/service -run '^TestGit(ManagerRace|SecretsNever)' -count=1 -v
go test -race ./internal/service -run '^TestGitManagerRace' -count=10 -v
```

Expected: all tests pass; race output contains no `DATA RACE`, deadlock, timeout, secret, raw Git stderr, or leaked goroutine failure.

- [ ] **Step 6: Commit conflict, race, and security acceptance**

```bash
git add internal/git/two_device_conflict_e2e_test.go internal/service/git_security_race_test.go
git commit -m "test: verify git conflict race and secret safety"
```

### Task 5: Add Reproducible Make And Frontend Commands

**Files:**
- Modify: `Makefile`
- Modify: `web/package.json`

- [ ] **Step 1: Demonstrate missing aggregate commands**

Run:

```bash
make verify
npm --prefix web run verify
```

Expected: both fail because `verify` targets/scripts do not exist.

- [ ] **Step 2: Add stable frontend CI scripts**

Set the `scripts` object in `web/package.json` to retain existing commands and add:

```json
{
  "dev": "vite",
  "build": "vite build",
  "preview": "vite preview",
  "test": "vitest run",
  "test:ci": "vitest run --reporter=default",
  "test:watch": "vitest",
  "verify": "npm run test:ci && npm run build"
}
```

Do not change dependencies or `web/package-lock.json`; script-only changes do not alter the dependency graph.

- [ ] **Step 3: Replace the Makefile target graph**

Replace `Makefile` with:

```makefile
.PHONY: all ui ui-deps ui-test go test test-git test-race vet verify clean

APP_NAME := igonotes
BUILD_DIR := builds
GIT_TEST_PACKAGES := ./internal/git ./internal/service ./internal/handlers ./cmd/api

all: go

ui-deps:
	npm --prefix web ci

ui: ui-deps
	npm --prefix web run build

ui-test: ui-deps
	npm --prefix web run test:ci

go: ui
	mkdir -p $(BUILD_DIR)
	go build -trimpath -o $(BUILD_DIR)/$(APP_NAME) ./cmd/api

test: ui
	go test ./... -count=1

test-git: ui
	IGONOTES_REQUIRE_GIT_INTEGRATION=1 go test $(GIT_TEST_PACKAGES) -count=1

test-race: ui
	go test -race ./... -count=1

vet: ui
	go vet ./...

verify: ui-test ui
	go test ./... -count=1
	go test -race ./... -count=1
	go vet ./...

clean:
	rm -rf $(BUILD_DIR) web/dist
```

`all` builds frontend before Go, `npm ci` makes CI/release installs lockfile-reproducible, and `test-git` turns unavailable/old Git from skip into failure. Keep `test-git` separate from `verify` so ordinary non-Git development remains possible without installed Git, matching the optionality requirement.

- [ ] **Step 4: Run all new local targets**

Run:

```bash
npm --prefix web run verify
make test-git
make verify
make all
```

Expected: all commands exit `0`; `web/dist/index.html` and `builds/igonotes` exist. `git status --short web/package-lock.json` has no output.

- [ ] **Step 5: Commit build commands**

```bash
git add Makefile web/package.json
git commit -m "build: add reproducible verification targets"
```

### Task 6: Add Branch CI And Harden Tagged Releases

**Files:**
- Create: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`

- [ ] **Step 1: Add branch and pull-request CI**

Create `.github/workflows/ci.yml`:

```yaml
name: CI

on:
  push:
  pull_request:
    branches: [develop, master]

permissions:
  contents: read

concurrency:
  group: ci-${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  frontend:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - name: Checkout
        uses: actions/checkout@v6
      - name: Setup Node.js
        uses: actions/setup-node@v7
        with:
          node-version: '24'
          cache: npm
          cache-dependency-path: web/package-lock.json
      - name: Install, test, and build frontend
        run: npm --prefix web ci && npm --prefix web run verify
      - name: Audit production dependency graph
        run: npm --prefix web audit --audit-level=high

  native-git:
    name: Native Git (${{ matrix.os }})
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
    runs-on: ${{ matrix.os }}
    timeout-minutes: 20
    env:
      IGONOTES_REQUIRE_GIT_INTEGRATION: '1'
    steps:
      - name: Checkout
        uses: actions/checkout@v6
      - name: Setup Node.js
        uses: actions/setup-node@v7
        with:
          node-version: '24'
          cache: npm
          cache-dependency-path: web/package-lock.json
      - name: Build embedded frontend
        shell: bash
        run: npm --prefix web ci && npm --prefix web run build
      - name: Setup Go
        uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
          cache: true
      - name: Record native Git
        shell: bash
        run: git --version
      - name: Run complete Go suite with native Git required
        shell: bash
        run: go test ./... -count=1
      - name: Repeat process-tree and two-device scenarios
        shell: bash
        run: go test ./internal/git -run 'Test(CommandRunnerCancellationTerminates|WindowsProcessTree|NativeGit|TwoDeviceEndToEnd)' -count=3 -v

  race-vet-build:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    steps:
      - name: Checkout
        uses: actions/checkout@v6
      - name: Setup Node.js
        uses: actions/setup-node@v7
        with:
          node-version: '24'
          cache: npm
          cache-dependency-path: web/package-lock.json
      - name: Setup Go
        uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
          cache: true
      - name: Run race, vet, frontend, and production build
        run: make verify all
      - name: Cross-compile release targets
        shell: bash
        run: |
          set -euo pipefail
          for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64; do
            goos="${target%/*}"
            goarch="${target#*/}"
            suffix=""
            [[ "$goos" == windows ]] && suffix=".exe"
            CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
              go build -trimpath -o "/tmp/igonotes-${goos}-${goarch}${suffix}" ./cmd/api
          done
```

Do not add repository secrets, write permissions, service containers, network remotes, or third-party Git servers. Native tests use only temporary local bare repositories.

- [ ] **Step 2: Split release verification/build from publication**

Rewrite `.github/workflows/release.yml` to retain trigger `push.tags: ['v*']` and prerelease classification, then apply:

```yaml
permissions:
  contents: read

concurrency:
  group: release-${{ github.ref }}
  cancel-in-progress: false
```

The read-only `build` job is exactly:

```yaml
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 45
    env:
      IGONOTES_REQUIRE_GIT_INTEGRATION: '1'
    steps:
      - name: Checkout
        uses: actions/checkout@v6
      - name: Setup Node.js
        uses: actions/setup-node@v7
        with:
          node-version: '24'
          cache: npm
          cache-dependency-path: web/package-lock.json
      - name: Install frontend dependencies
        run: npm --prefix web ci
      - name: Audit frontend dependencies
        run: npm --prefix web audit --audit-level=high
      - name: Test and build frontend
        run: npm --prefix web run verify
      - name: Setup Go
        uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
          cache: true
      - name: Test native Git integration
        run: go test ./... -count=1
      - name: Test with race detector
        run: go test -race ./... -count=1
      - name: Vet
        run: go vet ./...
      - name: Build release archives
        env:
          VERSION: ${{ github.ref_name }}
        run: |
          set -euo pipefail
          mkdir -p release build
          build_tar() {
            local goos="$1"
            local goarch="$2"
            local output="build/${goos}-${goarch}/igonotes"
            mkdir -p "$(dirname "$output")"
            CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
              go build -trimpath -ldflags="-s -w" -o "$output" ./cmd/api
            tar -C "$(dirname "$output")" -czf \
              "release/igonotes-${VERSION}-${goos}-${goarch}.tar.gz" igonotes
          }
          build_zip() {
            local goos="$1"
            local goarch="$2"
            local output="build/${goos}-${goarch}/igonotes.exe"
            mkdir -p "$(dirname "$output")"
            CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
              go build -trimpath -ldflags="-s -w" -o "$output" ./cmd/api
            zip -j "release/igonotes-${VERSION}-${goos}-${goarch}.zip" "$output"
          }
          build_tar linux amd64
          build_tar linux arm64
          build_zip windows amd64
          build_zip windows arm64
          build_tar darwin amd64
          build_tar darwin arm64
          (cd release && sha256sum ./*.tar.gz ./*.zip > checksums.txt)
      - name: Upload verified release artifacts
        uses: actions/upload-artifact@v6
        with:
          name: release-${{ github.ref_name }}
          path: release/
          if-no-files-found: error
          retention-days: 1
```

Add a separate `publish` job:

```yaml
  publish:
    needs: build
    runs-on: ubuntu-latest
    timeout-minutes: 10
    permissions:
      contents: write
    steps:
      - name: Download verified release artifacts
        uses: actions/download-artifact@v7
        with:
          name: release-${{ github.ref_name }}
          path: release
      - name: Publish GitHub release
        env:
          GH_TOKEN: ${{ github.token }}
          PRERELEASE: ${{ contains(github.ref_name, '-') }}
        run: |
          set -euo pipefail
          prerelease_args=()
          if [[ "$PRERELEASE" == "true" ]]; then
            prerelease_args+=(--prerelease)
          fi
          test "$(find release -maxdepth 1 -type f \( -name '*.tar.gz' -o -name '*.zip' \) | wc -l)" -eq 6
          test -s release/checksums.txt
          (cd release && sha256sum --check checksums.txt)
          gh release create "${GITHUB_REF_NAME}" \
            release/*.tar.gz \
            release/*.zip \
            release/checksums.txt \
            --verify-tag \
            --generate-notes \
            --title "${GITHUB_REF_NAME}" \
            "${prerelease_args[@]}"
```

Only `publish` receives `contents: write` and `GH_TOKEN`. It never checks out or executes repository code. The CI push trigger intentionally covers every branch; pull requests target the repository's `develop` and `master` integration branches. Preserve exact archive names and stable/pre-release behavior from the existing workflow.

- [ ] **Step 3: Validate workflow structure locally**

Run:

```bash
git diff --check -- .github/workflows/ci.yml .github/workflows/release.yml
python3 -c 'import pathlib; files=[pathlib.Path(".github/workflows/ci.yml"), pathlib.Path(".github/workflows/release.yml")]; assert all(p.read_text().count("contents: write") == (1 if p.name == "release.yml" else 0) for p in files)'
python3 -c 'from pathlib import Path; p=Path(".github/workflows/release.yml").read_text(); assert "needs: build" in p and "sha256sum --check" in p and "go test -race ./..." in p'
```

Expected: exit `0`; CI has no write permission, release has exactly one write grant, publication depends on build, checksums are verified, and race tests run before artifact publication.

- [ ] **Step 4: Run the commands used by both workflows**

Run:

```bash
npm --prefix web audit --audit-level=high
make verify
IGONOTES_REQUIRE_GIT_INTEGRATION=1 go test ./... -count=1
```

Expected: all commands exit `0` and no secret or raw Git diagnostic appears.

- [ ] **Step 5: Commit CI and release hardening**

```bash
git add .github/workflows/ci.yml .github/workflows/release.yml
git commit -m "ci: verify native git integration on every platform"
```

### Task 7: Replace The API Documentation With Shipped Contracts

**Files:**
- Replace: `docs/api.md`
- Replace: `site/docs/api.md`

- [ ] **Step 1: Record stale API documentation failures**

Run:

```bash
git grep -n -E '/notes/:id|/note\?path=|Git.*200 OK|будет реализовано' -- docs/api.md site/docs/api.md
```

Expected: matches prove both files describe obsolete routes or responses.

- [ ] **Step 2: Replace `docs/api.md` with the exact route table**

The document must use server URL `http://127.0.0.1:8080`, state that every table path is absolute, all API routes use the local-origin guard, and all Git routes require completed setup. It must contain this table:

```markdown
| Метод | Путь | Успех |
|:---|:---|:---|
| GET | `/api/notes` | `200` дерево заметок |
| POST | `/api/notes` | `201` созданный узел |
| GET | `/api/note?id=...` | `200` `{id, content, revision}` |
| DELETE | `/api/note?id=...` | `200` |
| POST | `/api/save` | `200` `{status, revision}` |
| PUT | `/api/rename` | `200` |
| POST | `/api/sync` | `200`, только пересканирование ФС/SQLite |
| GET | `/api/info` | `200` путь активной базы |
| GET | `/api/raw?path=...` | `200` файл базы |
| POST | `/api/assets` | `201` путь изображения |
| GET/PUT | `/api/config` | `200` конфигурация приложения |
| POST | `/api/setup` | `200` завершение первого запуска |
| POST/PUT/DELETE | `/api/bases` | `200/201` управление базами |
| POST | `/api/bases/switch` | `200` активная база |
| POST | `/api/system/select-directory` | `200` путь или `204` отмена |
| POST | `/api/git/probe` | `200` read-only анализ |
| PUT | `/api/git/config?base=...` | `202` сохранение и initialize/connect |
| DELETE | `/api/git/config?base=...` | `200` отключение без удаления `.git` |
| GET | `/api/git/status[?base=...]` | `200` статусы операций |
| POST | `/api/git/sync?base=...` | `202` ручной sync |
| POST | `/api/git/resume?base=...` | `202` явный retry/resume |
| GET | `/api/git/conflicts?base=...` | `200` список конфликтов и stages |
| PUT | `/api/git/conflicts/resolve` | `200` одно решение |
| POST | `/api/git/conflicts/complete?base=...` | `202` merge commit и push |
| POST | `/api/git/conflicts/abort?base=...` | `202` `merge --abort` и pause |
```

Add complete JSON examples for:

- `GET /api/note` revision and `POST /api/save` with `expected_revision`.
- `409 note_changed` and explicit overwrite only after fetching a new revision.
- Probe request/response including required confirmations.
- Git config request with URL, branch, interval `15`, and default template.
- `202` operation response with operation ID/status/deduplication.
- Status values `unconfigured`, `initializing`, `ready`, `syncing`, `error`, `paused`, `conflict`, `needs_reconnect`.
- Conflict list, manual text resolve, complete, and abort.
- Error object `{code,message,field}` and these exact machine codes: `git_unavailable`, `git_version_unsupported`, `auth_failed`, `remote_unreachable`, `identity_missing`, `invalid_branch`, `branch_deleted`, `origin_mismatch`, `repository_root_mismatch`, `repository_locked`, `remote_history_rewritten`, `push_rejected`, `git_conflict_pending`, `note_changed`, `git_repository_in_use`, `git_command_failed`, `git_timeout`, `git_canceled`, `not_a_git_repository`, `git_conflict`, `needs_reconnect`, `git_confirmation_required`, `operation_interrupted`, `backup_mismatch`, `git_conflict_not_found`, `git_conflict_stale`, `git_conflict_unresolved`, `git_conflict_unsupported`, `git_merge_not_in_progress`, `git_recovery_required`, `git_paused`, and `git_not_paused`.

Use these concrete note examples:

```json
{
  "id": "ideas/roadmap.md",
  "content": "# Roadmap\n",
  "revision": "sha256:eae710439b6ab1e8e034479e4785fddf64058bbe4b746003e659498e618ae760"
}
```

```json
{
  "id": "ideas/roadmap.md",
  "content": "# Updated roadmap\n",
  "expected_revision": "sha256:eae710439b6ab1e8e034479e4785fddf64058bbe4b746003e659498e618ae760"
}
```

```json
{
  "code": "note_changed",
  "message": "Note changed on disk",
  "field": "expected_revision"
}
```

State directly below the stale response that overwrite requires a fresh `GET /api/note`, a separate user confirmation, and a second `POST /api/save` carrying the freshly returned revision; omitting `expected_revision` is legacy API compatibility and is never used by the official frontend.

Use this probe request and representative response:

```json
{
  "base": "work",
  "git_url": "git@example.invalid:alice/work-notes.git",
  "git_branch": "main"
}
```

```json
{
  "base": "work",
  "git_version": "2.43.0",
  "has_repository": false,
  "repository_root_matches": true,
  "current_branch": "",
  "detached_head": false,
  "working_tree_clean": false,
  "remote_branches": ["main"],
  "empty_remote": false,
  "identity_configured": true,
  "history_relation": "unknown",
  "can_configure": true,
  "required_mutations": {
    "create_repository": true,
    "add_origin": true,
    "replace_origin": false,
    "create_branch": false,
    "merge_histories": true
  },
  "warnings": [
    "Git commits include every non-ignored file in the base directory.",
    "Connecting may merge existing local and remote histories."
  ]
}
```

Use this configuration request and complete `GitConfigResponse`:

```json
{
  "git_url": "git@example.invalid:alice/work-notes.git",
  "git_branch": "main",
  "auto_sync": true,
  "auto_sync_interval_minutes": 15,
  "git_commit_message_template": "IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)",
  "confirmations": {
    "create_repository": true,
    "replace_origin": false,
    "create_branch": false,
    "merge_histories": true
  }
}
```

```json
{
  "base": {
    "name": "work",
    "path": "/home/alice/Notes/work",
    "git_url": "git@example.invalid:alice/work-notes.git",
    "git_branch": "main",
    "auto_sync": true,
    "auto_sync_interval_minutes": 15,
    "git_commit_message_template": "IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)"
  },
  "status": {
    "base": "work",
    "repository_path": "/home/alice/Notes/work",
    "state": "initializing",
    "operation_id": "0000000000000000000000000000002a",
    "stage": "queued",
    "ahead": 0,
    "behind": 0,
    "consecutive_failures": 0,
    "changed_paths": []
  },
  "operation": {
    "operation_id": "0000000000000000000000000000002a",
    "status": "queued",
    "deduplicated": false
  }
}
```

Document that `POST /api/git/sync`, `POST /api/git/resume`, conflict complete, and conflict abort return only the nested operation object shown above. `PUT /api/git/config` returns the complete wrapper because valid settings remain saved even when later initialize queueing or execution fails.

Use this status object:

```json
{
  "base": "work",
  "repository_path": "/home/alice/Notes/work",
  "state": "syncing",
  "operation_id": "0000000000000000000000000000002a",
  "stage": "fetching",
  "ahead": 1,
  "behind": 2,
  "consecutive_failures": 0,
  "changed_paths": ["ideas/roadmap.md"],
  "remote_oid": "1111111111111111111111111111111111111111"
}
```

Use this conflict list and manual resolution request:

```json
{
  "base": "work",
  "operation_id": "0000000000000000000000000000002b",
  "head_oid": "2222222222222222222222222222222222222222",
  "merge_head_oid": "3333333333333333333333333333333333333333",
  "conflicts": [
    {
      "id": "sha256:cd9a86eb286d5886c3e581a3d87f3e9648e49aad15cfb43f8a001191c4bb37a2",
      "kind": "content",
      "content_kind": "text",
      "path": "ideas/roadmap.md",
      "local": {
        "path": "ideas/roadmap.md",
        "oid": "4444444444444444444444444444444444444444",
        "mode": "100644",
        "size": 6,
        "content": "local\n",
        "preview_truncated": false
      },
      "remote": {
        "path": "ideas/roadmap.md",
        "oid": "5555555555555555555555555555555555555555",
        "mode": "100644",
        "size": 7,
        "content": "remote\n",
        "preview_truncated": false
      },
      "actions": ["local", "remote", "manual"]
    }
  ],
  "can_complete": false
}
```

```json
{
  "base": "work",
  "operation_id": "0000000000000000000000000000002b",
  "conflict_id": "sha256:cd9a86eb286d5886c3e581a3d87f3e9648e49aad15cfb43f8a001191c4bb37a2",
  "path": "ideas/roadmap.md",
  "action": "manual",
  "result_path": "ideas/roadmap.md",
  "content": "local\nremote\n"
}
```

Document complete/abort as bodyless `POST` requests returning the same accepted-operation shape, with complete allowed only after `conflicts` is empty and abort moving the base to persisted `paused` state.

Explicitly state that API payloads never include raw stderr, command lines, credentials, or internal causes; paths are relative and cannot address `.git` or escape the base root.

- [ ] **Step 3: Mirror the API document to the site**

`site/docs/api.md` must contain exactly this front matter followed by the complete `docs/api.md` body:

```yaml
---
layout: default
---
```

Do not summarize or maintain a shorter divergent site API.

- [ ] **Step 4: Verify route coverage and stale text removal**

Run:

```bash
for route in /api/git/probe /api/git/config /api/git/status /api/git/sync /api/git/resume /api/git/conflicts /api/git/conflicts/resolve /api/git/conflicts/complete /api/git/conflicts/abort; do
  git grep -q "$route" -- docs/api.md site/docs/api.md
done
! git grep -n -E '/notes/:id|/note\?path=|Git.*200 OK|будет реализовано' -- docs/api.md site/docs/api.md
```

Expected: exit `0`; every Git route appears in both files and obsolete contracts have no match.

- [ ] **Step 5: Commit API documentation**

```bash
git add docs/api.md site/docs/api.md
git commit -m "docs: document git synchronization api"
```

### Task 8: Document User Workflows In README And Site

**Files:**
- Modify: `README.md`
- Modify: `docs/user.md`
- Modify: `site/index.md`
- Modify: `site/docs/user.md`

- [ ] **Step 1: Update README to current shipped behavior**

Replace obsolete Go 1.21/BoltDB/static-JS claims and add:

```markdown
## Git-синхронизация

Git необязателен для обычных локальных баз. Для синхронизации нужен системный Git 2.28 или новее и заранее настроенная пользовательская авторизация через SSH-agent, credential helper или Git config. IGoNotes не хранит HTTPS-токены и SSH-ключи.

Интеграция настраивается отдельно для каждой базы: `origin`, ветка, ручной режим или интервал 5/15/30/60 минут и шаблон локального commit message. Sync фиксирует все неигнорируемые файлы базы через `git add -A`, выполняет обычный merge и никогда не использует rebase, reset или force-push. Конфликты разрешаются явно в интерфейсе.
```

Use current Go 1.26+, Svelte 5/Vite 8/Tailwind 4/CodeMirror 6, SQLite, `make all`, `make verify`, and `make test-git`. The launch command remains `./builds/igonotes`; do not document `igonotes start`.

- [ ] **Step 2: Add complete user Git guidance**

In `docs/user.md`, replace the disabled-Git paragraph with sections that explain:

1. Prerequisites: Git 2.28+, noninteractive credentials, branch access, commit identity.
2. Four configuration screens: remote probe, branch, schedule/template, mutation review/confirmation.
3. Initial-connect outcomes and backup ref `refs/igonotes/backups/...` without promising automatic cleanup.
4. Manual sync after workspace flush; `/api/sync` remains “Обновить дерево”.
5. Autosync intervals and pause after five operational failures; retry/resume semantics.
6. Status meanings and ahead/behind display.
7. Stale note dialog choices and explicit overwrite confirmation.
8. Text/binary/add-add/modify-delete/rename-delete conflict choices, partial resolution, complete, and defer/abort.
9. Disable/forget/path-change behavior: `.git`, commits, refs, remote, and files remain; moved bases require reconnect.
10. Security: no embedded HTTP userinfo/query tokens, no stored keys/tokens, no prompt in background work, hooks/signing remain active, all nonignored base files may be committed.
11. Explicit exclusions: force-push, rebase, submodules, LFS, multiple remotes, monorepo subdirectory, arbitrary intervals.

Use UI labels from the landed frontend tests. Do not claim system notifications or automatic conflict choice.

- [ ] **Step 3: Update published user content and landing page**

Mirror the complete user guidance into `site/docs/user.md` after its Jekyll front matter. Replace the landing-page launch example with the literal fenced block:

````markdown
```bash
igonotes
```
````

Add “Опциональная двусторонняя Git-синхронизация нескольких устройств” to the features. Remove emoji checkmarks and the unsupported `igonotes start` command.

- [ ] **Step 4: Verify user documentation claims**

Run:

```bash
! git grep -n -E 'Git, скоро|Git-синхронизация.*не реализована|igonotes start|BoltDB|Go \(1\.21' -- README.md docs/user.md site/index.md site/docs/user.md
git grep -q 'Git 2.28' -- README.md docs/user.md site/docs/user.md
git grep -q 'force-push' -- docs/user.md site/docs/user.md
git grep -q 'пять' -- docs/user.md site/docs/user.md
```

Expected: exit `0`; no stale claim remains and prerequisites/safety/pause are present in source and site docs.

- [ ] **Step 5: Commit user documentation**

```bash
git add README.md docs/user.md site/index.md site/docs/user.md
git commit -m "docs: explain git synchronization workflows"
```

### Task 9: Document Architecture And Enforce Documentation Consistency

**Files:**
- Modify: `docs/developer.md`
- Modify: `site/docs/developer.md`
- Modify: `AGENTS.md`
- Create: `internal/docs/git_docs_test.go`

- [ ] **Step 1: Update developer architecture and commands**

Add these exact architecture facts to `docs/developer.md`; leave `site/docs/developer.md` unchanged until the RED mirror test in Step 4:

```text
internal/git is the only system-Git process boundary.
Git commands are argument slices and never shell strings.
Linux/macOS use a dedicated process group; Windows starts suspended, assigns a kill-on-close Job Object, then resumes the sole primary thread.
Nil command stdin is EOF and GIT_TERMINAL_PROMPT=0 is always set.
GitService owns one repository operation; GitManager owns queue/status/schedule.
SettingsService alone mutates application Git config.
Lock order is coordinator -> SettingsService -> NoteService -> repository/SQLite.
Fetch occurs before the note write lock; push occurs after reindex and unlock.
Config and metadata DB remain outside note bases.
```

Document exact commands:

```bash
npm --prefix web run verify
make test
make test-git
make test-race
make vet
make verify
```

Document `IGONOTES_REQUIRE_GIT_INTEGRATION=1`, temporary local bare remotes, the three-OS CI matrix, release six-target cross-build, and read-only build versus write-only publish job. State that `go-git` is not used.

- [ ] **Step 2: Update AGENTS as the current repository contract**

Change Git synchronization to checked/implemented and update the technology/status sections. Extend its REST table with all Git routes and note revision behavior. Add the runner/service/manager/coordinator ownership rules and these required pre-merge commands:

```bash
npm --prefix web run verify
make test-git
make verify
```

Keep the future Git-synchronization sentence removed. Do not change branch names `develop`, `master`, or `pages`.

- [ ] **Step 3: Write RED documentation regression tests**

Create `internal/docs/git_docs_test.go` with the complete implementation:

```go
package docs

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not resolve documentation test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func readDocument(t *testing.T, relative string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), filepath.FromSlash(relative)))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
}

func TestGitDocumentationContainsShippedRoutesAndCommands(t *testing.T) {
	required := map[string][]string{
		"docs/api.md": {
			"/api/git/probe", "/api/git/config", "/api/git/status", "/api/git/sync",
			"/api/git/resume", "/api/git/conflicts", "/api/git/conflicts/resolve",
			"/api/git/conflicts/complete", "/api/git/conflicts/abort", "note_changed",
		},
		"docs/user.md":      {"Git 2.28", "refs/igonotes/backups/", "force-push"},
		"docs/developer.md": {"make test-git", "make verify", "GIT_TERMINAL_PROMPT=0"},
	}
	for relative, tokens := range required {
		contents := string(readDocument(t, relative))
		for _, token := range tokens {
			if !strings.Contains(contents, token) {
				t.Errorf("%s is missing %q", relative, token)
			}
		}
	}
}

func TestGitDocumentationHasNoStaleClaims(t *testing.T) {
	files := []string{
		"README.md", "AGENTS.md", "docs/api.md", "docs/user.md", "docs/developer.md",
		"site/index.md", "site/docs/api.md", "site/docs/user.md", "site/docs/developer.md",
	}
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`Git, скоро`),
		regexp.MustCompile(`Git-синхронизация[^\n]*(не реализована|будущ)`),
		regexp.MustCompile(`igonotes start`),
		regexp.MustCompile(`BoltDB`),
		regexp.MustCompile(`/notes/:id`),
		regexp.MustCompile(`/note\?path=`),
	}
	for _, relative := range files {
		contents := readDocument(t, relative)
		for _, pattern := range forbidden {
			if pattern.Match(contents) {
				t.Errorf("%s contains stale expression %s", relative, pattern)
			}
		}
	}
}

func TestPublishedGitDocumentsMirrorSource(t *testing.T) {
	pairs := map[string]string{
		"docs/api.md":       "site/docs/api.md",
		"docs/user.md":      "site/docs/user.md",
		"docs/developer.md": "site/docs/developer.md",
	}
	const frontMatter = "---\nlayout: default\n---\n\n"
	for source, published := range pairs {
		want := readDocument(t, source)
		got := readDocument(t, published)
		if !bytes.HasPrefix(got, []byte(frontMatter)) {
			t.Errorf("%s does not have the required front matter", published)
			continue
		}
		got = got[len(frontMatter):]
		if !bytes.Equal(got, want) {
			t.Errorf("published document pair differs: %s -> %s", source, published)
		}
	}
}
```

The test resolves the repository root from `runtime.Caller`, never the process working directory, removes only the exact Jekyll front matter, normalizes CRLF to LF, and requires byte equality. This deliberately prevents shorter site copies from drifting.

- [ ] **Step 4: Run docs tests RED, then synchronize the copies**

First run before the site copies are byte-matched:

```bash
go test ./internal/docs -count=1 -v
```

Expected: `TestPublishedGitDocumentsMirrorSource` fails and names only the mismatched relative file pair.

Then make each site document its three-line front matter plus the exact source body and rerun:

```bash
gofmt -w internal/docs/git_docs_test.go
go test ./internal/docs -count=1 -v
```

Expected: all three documentation tests pass.

- [ ] **Step 5: Commit developer docs and consistency test**

```bash
git add AGENTS.md docs/developer.md site/docs/developer.md internal/docs/git_docs_test.go
git commit -m "docs: describe git integration architecture"
```

### Task 10: Run Serial Final Acceptance And Self-Review

**Files:**
- Verify all files from Tasks 1-9.
- Do not create a release tag or modify application behavior during this task.

- [ ] **Step 1: Confirm intended integrated file ownership**

Run:

```bash
git status --short
git diff --check
git log --oneline -12
```

Expected: no unstaged implementation changes, no whitespace errors, and the lane commits are present. Existing unrelated user files may remain untracked, but they must not be staged or modified.

- [ ] **Step 2: Run frontend and documentation verification**

Run:

```bash
npm --prefix web ci
npm --prefix web run verify
npm --prefix web audit --audit-level=high
go test ./internal/docs -count=1 -v
```

Expected: all commands exit `0`, Vitest reports no failed tests, Vite produces `web/dist`, audit has no high/critical finding, and docs contain no stale claim.

- [ ] **Step 3: Run complete backend and native Git verification**

Run:

```bash
go test ./... -count=1
IGONOTES_REQUIRE_GIT_INTEGRATION=1 go test ./internal/git ./internal/service ./internal/handlers ./cmd/api -count=1 -v
IGONOTES_REQUIRE_GIT_INTEGRATION=1 go test ./internal/git -run 'Test(CommandRunnerCancellationTerminates|WindowsProcessTree|NativeGit|TwoDeviceEndToEnd)' -count=10 -v
```

Expected: all packages pass, native Git is not skipped, process-tree tests pass ten times, both two-device scenarios pass ten times, and no raw stderr/secret appears.

- [ ] **Step 4: Run full race and repeated manager stress serially**

Run:

```bash
go test -race ./... -count=1
go test -race ./internal/service -run '^TestGitManagerRace' -count=20 -v
```

Expected: exit `0`; no `DATA RACE`, deadlock, five-second test timeout, leaked goroutine, duplicate operation, or stale overwrite.

- [ ] **Step 5: Run finite fuzz campaigns serially**

Run each command separately:

```bash
go test ./internal/git -run '^$' -fuzz=FuzzRedactGitDiagnostic -fuzztime=15s
go test ./internal/git -run '^$' -fuzz=FuzzConflictParsers -fuzztime=15s
```

Expected: both known Git fuzz campaigns exit `0` without panic, hang, malformed-path escape, or secret assertion.

- [ ] **Step 6: Run static and production build verification**

Run:

```bash
go vet ./...
make clean
make all
test -x builds/igonotes
```

Expected: vet exits `0`, clean rebuild succeeds from `npm ci`, and the production binary exists.

- [ ] **Step 7: Cross-compile all release targets**

Run:

```bash
tmp=$(mktemp -d "/tmp/opencode/igonotes-hardening.XXXXXX")
for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64; do
  goos="${target%/*}"
  goarch="${target#*/}"
  suffix=""
  [[ "$goos" == windows ]] && suffix=".exe"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags="-s -w" -o "$tmp/igonotes-${goos}-${goarch}${suffix}" ./cmd/api
done
test "$(find "$tmp" -maxdepth 1 -type f | wc -l)" -eq 6
```

Expected: all six binaries compile and exactly six files exist in the temporary directory.

- [ ] **Step 8: Validate workflow hardening and changed-file boundary**

Run:

```bash
git diff --check
git grep -n 'contents: write' -- .github/workflows
git grep -n 'IGONOTES_REQUIRE_GIT_INTEGRATION' -- .github/workflows/ci.yml .github/workflows/release.yml
git status --short
```

Expected: exactly one `contents: write` match, in the release `publish` job; native Git is required by CI and release verification; only intentional uncommitted files, if any, are the pre-existing unrelated files excluded from this plan.

- [ ] **Step 9: Execute the final acceptance matrix**

| Acceptance criterion | Evidence | Expected result |
|---|---|---|
| Non-Git base remains optional | `go test ./...` with ordinary tests | No startup Git invocation; non-Git tests pass. |
| Per-base enable/disable | Landed settings, scheduler reconciliation, disable, and forget tests | Only the selected base changes; disabling leaves `.git` and user files intact. |
| Existing local/remote connect | Landed `TestInitialize*Backup*`, unrelated-history, and retry tests | A journaled backup ref protects the local snapshot and retry is idempotent. |
| Git 2.28+ and native platforms | CI `native-git` matrix | Ubuntu/macOS/Windows pass with no skip. |
| Process tree/noninteractive | `TestCommandRunnerCancellationTerminates*`, `TestWindowsProcessTree*`, environment test | Windows cannot execute before Job assignment; immediate descendants terminate; setup failures reap; stdin EOF and prompt-disabled env are observed. |
| Unusual paths and full tree | `TestNativeGitRunner*` | Exact NUL names/bytes, ignore, assets, non-Markdown, deletion pass. |
| Two-device propagation | `TestTwoDeviceEndToEndConverges*` | Identical device/remote trees. |
| Conflict safety/restart | `TestTwoDeviceEndToEndConflict*` | No early push or lost side; restart and explicit completion pass. |
| Timeout/rejected-push safety | Landed push-timeout, exact-OID retry, and rejection tests | No force-push or silent overwrite; journaled commit remains recoverable. |
| Stale editor | landed revision tests plus race fixture | `409 note_changed`; no overwrite. |
| Filesystem/index consistency | Landed `Test*Reindex*` tests plus two-device tree comparison | Reindex completes before unlock and frontend-visible tree matches disk. |
| Scheduler breaker | landed Plan 7 tests plus race fixture | Fifth error pauses; persisted resume semantics pass. |
| Secret redaction | redaction corpus/fuzz and sink scan | No secret in config/API/log/DB/journal/commit. |
| Race safety | `go test -race ./...` and repeated manager tests | No race/deadlock/leak. |
| Frontend | `npm --prefix web run verify` | Vitest and production build pass. |
| Release | six cross-builds and split workflow checks | Six binaries; write permission only in publish. |
| Documentation | `go test ./internal/docs` | Exact routes/commands and mirrored site docs pass. |

- [ ] **Step 10: Self-review against the approved specification**

Read `docs/superpowers/specs/2026-09-01-git-synchronization-design.md` from top to bottom and check each readiness criterion at lines 476-488 against the matrix above. Then run:

```bash
stale_pattern='T''BD|TO''DO|implement la''ter|fill i''n|similar t''o|Git, скоро|Git-синхронизация.*(не реализована|будущ)'
! git grep -n -E "$stale_pattern" -- \
  internal/git/runner.go internal/git/runner_test.go internal/git/errors.go \
  internal/git/process_tree_unix.go internal/git/process_tree_windows.go \
  internal/git/process_tree_unix_test.go internal/git/process_tree_windows_test.go \
  internal/git/native_matrix_test.go internal/git/redaction_fuzz_test.go \
  internal/git/two_device_fixture_test.go internal/git/two_device_e2e_test.go \
  internal/git/two_device_conflict_e2e_test.go internal/service/git_security_race_test.go \
  internal/docs/git_docs_test.go Makefile web/package.json .github/workflows/ci.yml \
  .github/workflows/release.yml README.md AGENTS.md docs/api.md docs/user.md \
  docs/developer.md site/index.md site/docs/api.md site/docs/user.md site/docs/developer.md
git diff --check
```

Expected: both commands exit `0`; the negated `git grep` has no output and `git diff --check` reports no whitespace errors. Confirm all names used by tests match the landed Plan 1-7 types and methods, especially `ConfiguredBase`, `Operation`, `RecoveryResult`, `ConflictSnapshot`, conflict actions, scheduler clock seam, and public status values.

- [ ] **Step 11: Commit only serial verification fixes, when present**

When Steps 1-10 required an owned-lane correction, stage only that correction after repeating its focused and final checks:

```bash
git add \
  internal/git/runner.go internal/git/runner_test.go internal/git/errors.go \
  internal/git/process_tree_unix.go internal/git/process_tree_windows.go \
  internal/git/process_tree_unix_test.go internal/git/process_tree_windows_test.go \
  internal/git/native_matrix_test.go internal/git/redaction_fuzz_test.go \
  internal/git/two_device_fixture_test.go internal/git/two_device_e2e_test.go \
  internal/git/two_device_conflict_e2e_test.go internal/service/git_security_race_test.go \
  internal/docs/git_docs_test.go Makefile web/package.json .github/workflows/ci.yml \
  .github/workflows/release.yml README.md AGENTS.md docs/api.md docs/user.md \
  docs/developer.md site/index.md site/docs/api.md site/docs/user.md site/docs/developer.md
git diff --cached --check
git commit -m "test: complete git integration acceptance"
```

Expected: the commit contains only fixes discovered by final verification. When no correction was required, skip this commit; do not create an empty commit.

## Completion Conditions

Implementation is complete only when:

- Every Task 1-9 lane commit is integrated from the same Plan 7 baseline.
- Task 10's commands have fresh successful output from the integrated worktree.
- Linux, macOS, and Windows native CI jobs pass against real system Git.
- The branch/PR workflow completes successfully on the hardening pull request before merge.
- The release workflow remains tag-triggered and preserves stable/pre-release classification.
- No test, document, or workflow requires a network Git host or stored credential.
- No scope exclusion from the approved design has been implemented indirectly.
- `git status --short` contains no accidental generated `web/dist`, build archive, fuzz corpus, credential, temporary repository, or metadata database.

Do not create a release tag as part of this plan. Release publication remains an explicit maintainer action after the hardening pull request is reviewed and merged.
