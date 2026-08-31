//go:build linux

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSystemdUserManagerUninstallOwnedUnit(t *testing.T) {
	root := t.TempDir()
	unitPath, _ := writeOwnedSystemdTestUnit(t, root)
	systemctlPath := filepath.Join(root, "bin", "systemctl")
	runner := &recordingSystemdRunner{}
	manager := NewSystemdUserManager(
		"linux",
		runner,
		func(string) (string, error) { return systemctlPath, nil },
		func() (string, error) { return root, nil },
		nil,
	)

	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall() error = %v, want nil", err)
	}

	assertPathDoesNotExist(t, unitPath)
	assertSystemdCalls(t, runner.calls, systemctlPath,
		[]string{"--user", "show-environment"},
		[]string{"--user", "disable", "--now", SystemdUserUnitName},
		[]string{"--user", "daemon-reload"},
	)
}

func TestSystemdUserManagerUninstallAbsentUnitSkipsSystemctl(t *testing.T) {
	root := t.TempDir()
	lookPathCalls := 0
	runner := &recordingSystemdRunner{}
	manager := NewSystemdUserManager(
		"linux",
		runner,
		func(string) (string, error) {
			lookPathCalls++
			return filepath.Join(root, "systemctl"), nil
		},
		func() (string, error) { return root, nil },
		nil,
	)

	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall() error = %v, want nil", err)
	}
	if lookPathCalls != 0 {
		t.Errorf("LookPath calls = %d, want 0", lookPathCalls)
	}
	if len(runner.calls) != 0 {
		t.Errorf("Run calls = %d, want 0", len(runner.calls))
	}
	assertPathDoesNotExist(t, filepath.Join(root, "systemd"))
}

func TestSystemdUserManagerUninstallPreservesForeignUnit(t *testing.T) {
	root := t.TempDir()
	unitPath := filepath.Join(root, "systemd", "user", SystemdUserUnitName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(): %v", err)
	}
	foreign := []byte("# Foreign unit\n" + systemdUnitMarker + "\n[Unit]\n")
	if err := os.WriteFile(unitPath, foreign, 0o644); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}
	lookPathCalls := 0
	runner := &recordingSystemdRunner{}
	manager := NewSystemdUserManager(
		"linux",
		runner,
		func(string) (string, error) {
			lookPathCalls++
			return filepath.Join(root, "systemctl"), nil
		},
		func() (string, error) { return root, nil },
		nil,
	)

	err := manager.Uninstall(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("Uninstall() error = %v, want foreign unit ownership error", err)
	}
	got, readErr := os.ReadFile(unitPath)
	if readErr != nil {
		t.Fatalf("ReadFile(): %v", readErr)
	}
	if !reflect.DeepEqual(got, foreign) {
		t.Errorf("foreign unit changed to %q, want %q", got, foreign)
	}
	if lookPathCalls != 0 {
		t.Errorf("LookPath calls = %d, want 0", lookPathCalls)
	}
	if len(runner.calls) != 0 {
		t.Errorf("Run calls = %d, want 0", len(runner.calls))
	}
}

func TestSystemdUserManagerUninstallDisableFailureLeavesUnit(t *testing.T) {
	root := t.TempDir()
	unitPath, original := writeOwnedSystemdTestUnit(t, root)
	systemctlPath := filepath.Join(root, "systemctl")
	disableErr := errors.New("disable failed")
	runner := &recordingSystemdRunner{responses: map[string]systemdRunnerResponse{
		"--user\x00disable\x00--now\x00" + SystemdUserUnitName: {
			result: CommandResult{Diagnostic: []byte("disable diagnostic\n"), ExitCode: 1},
			err:    disableErr,
		},
	}}
	manager := newSystemdUninstallTestManager(root, systemctlPath, runner)

	err := manager.Uninstall(context.Background())
	if !errors.Is(err, disableErr) {
		t.Fatalf("Uninstall() error = %v, want wrapped %v", err, disableErr)
	}
	for _, fragment := range []string{"disable", "disable diagnostic"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("Uninstall() error = %q, want %q", err, fragment)
		}
	}
	got, readErr := os.ReadFile(unitPath)
	if readErr != nil {
		t.Fatalf("ReadFile(): %v", readErr)
	}
	if !reflect.DeepEqual(got, original) {
		t.Errorf("unit changed to %q, want %q", got, original)
	}
	assertSystemdCalls(t, runner.calls, systemctlPath,
		[]string{"--user", "show-environment"},
		[]string{"--user", "disable", "--now", SystemdUserUnitName},
	)
}

func TestSystemdUserManagerUninstallRemovalFailureSkipsReload(t *testing.T) {
	root := t.TempDir()
	unitPath, original := writeOwnedSystemdTestUnit(t, root)
	systemctlPath := filepath.Join(root, "systemctl")
	removeErr := errors.New("remove failed")
	runner := &recordingSystemdRunner{}
	manager := newSystemdUninstallTestManager(root, systemctlPath, runner)
	manager.remove = func(path string) error {
		if path != unitPath {
			t.Errorf("remove path = %q, want %q", path, unitPath)
		}
		return removeErr
	}

	err := manager.Uninstall(context.Background())
	if !errors.Is(err, removeErr) {
		t.Fatalf("Uninstall() error = %v, want wrapped %v", err, removeErr)
	}
	if !strings.Contains(err.Error(), "remove") {
		t.Errorf("Uninstall() error = %q, want removal stage", err)
	}
	got, readErr := os.ReadFile(unitPath)
	if readErr != nil {
		t.Fatalf("ReadFile(): %v", readErr)
	}
	if !reflect.DeepEqual(got, original) {
		t.Errorf("unit changed to %q, want %q", got, original)
	}
	assertSystemdCalls(t, runner.calls, systemctlPath,
		[]string{"--user", "show-environment"},
		[]string{"--user", "disable", "--now", SystemdUserUnitName},
	)
}

func TestSystemdUserManagerUninstallDurabilityFailureStillReloads(t *testing.T) {
	tests := []struct {
		name      string
		reloadErr error
	}{
		{name: "sync failure"},
		{name: "sync and reload failure", reloadErr: errors.New("reload failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			unitPath, _ := writeOwnedSystemdTestUnit(t, root)
			systemctlPath := filepath.Join(root, "systemctl")
			syncErr := errors.New("directory sync failed")
			runner := &recordingSystemdRunner{responses: map[string]systemdRunnerResponse{}}
			if tt.reloadErr != nil {
				runner.responses["--user\x00daemon-reload"] = systemdRunnerResponse{
					result: CommandResult{Diagnostic: []byte("reload diagnostic\n"), ExitCode: 1},
					err:    tt.reloadErr,
				}
			}
			manager := newSystemdUninstallTestManager(root, systemctlPath, runner)
			manager.syncDirectory = func(path string) error {
				if path != filepath.Dir(unitPath) {
					t.Errorf("sync directory = %q, want %q", path, filepath.Dir(unitPath))
				}
				return syncErr
			}

			err := manager.Uninstall(context.Background())
			if !errors.Is(err, syncErr) {
				t.Fatalf("Uninstall() error = %v, want wrapped %v", err, syncErr)
			}
			if tt.reloadErr != nil && !errors.Is(err, tt.reloadErr) {
				t.Errorf("Uninstall() error = %v, want wrapped %v", err, tt.reloadErr)
			}
			assertPathDoesNotExist(t, unitPath)
			assertSystemdCalls(t, runner.calls, systemctlPath,
				[]string{"--user", "show-environment"},
				[]string{"--user", "disable", "--now", SystemdUserUnitName},
				[]string{"--user", "daemon-reload"},
			)
		})
	}
}

func TestSystemdUserManagerUninstallReloadFailureReturnsAfterRemoval(t *testing.T) {
	root := t.TempDir()
	unitPath, _ := writeOwnedSystemdTestUnit(t, root)
	systemctlPath := filepath.Join(root, "systemctl")
	reloadErr := errors.New("reload failed")
	runner := &recordingSystemdRunner{responses: map[string]systemdRunnerResponse{
		"--user\x00daemon-reload": {
			result: CommandResult{Diagnostic: []byte("reload diagnostic\n"), ExitCode: 1},
			err:    reloadErr,
		},
	}}
	manager := newSystemdUninstallTestManager(root, systemctlPath, runner)

	err := manager.Uninstall(context.Background())
	if !errors.Is(err, reloadErr) {
		t.Fatalf("Uninstall() error = %v, want wrapped %v", err, reloadErr)
	}
	for _, fragment := range []string{"reload", "reload diagnostic"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("Uninstall() error = %q, want %q", err, fragment)
		}
	}
	assertPathDoesNotExist(t, unitPath)
	assertSystemdCalls(t, runner.calls, systemctlPath,
		[]string{"--user", "show-environment"},
		[]string{"--user", "disable", "--now", SystemdUserUnitName},
		[]string{"--user", "daemon-reload"},
	)
}

func TestSystemdUserManagerUninstallRepeated(t *testing.T) {
	root := t.TempDir()
	unitPath, _ := writeOwnedSystemdTestUnit(t, root)
	lookPathCalls := 0
	runner := &recordingSystemdRunner{}
	manager := NewSystemdUserManager(
		"linux",
		runner,
		func(string) (string, error) {
			lookPathCalls++
			return filepath.Join(root, "systemctl"), nil
		},
		func() (string, error) { return root, nil },
		nil,
	)

	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("first Uninstall() error = %v, want nil", err)
	}
	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("second Uninstall() error = %v, want nil", err)
	}
	assertPathDoesNotExist(t, unitPath)
	if lookPathCalls != 1 {
		t.Errorf("LookPath calls = %d, want 1", lookPathCalls)
	}
	if len(runner.calls) != 3 {
		t.Errorf("Run calls = %d, want 3 from first uninstall only", len(runner.calls))
	}
}

func TestSystemdUserManagerUninstallValidatesDependenciesAndConfigRoot(t *testing.T) {
	t.Run("nil runner", func(t *testing.T) {
		manager := NewSystemdUserManager("linux", nil, func(string) (string, error) { return "systemctl", nil }, func() (string, error) { return t.TempDir(), nil }, nil)
		if err := manager.Uninstall(context.Background()); err == nil || !strings.Contains(err.Error(), "runner") {
			t.Fatalf("Uninstall() error = %v, want runner error", err)
		}
	})

	t.Run("nil LookPath", func(t *testing.T) {
		manager := NewSystemdUserManager("linux", &recordingSystemdRunner{}, nil, func() (string, error) { return t.TempDir(), nil }, nil)
		if err := manager.Uninstall(context.Background()); err == nil || !strings.Contains(err.Error(), "LookPath") {
			t.Fatalf("Uninstall() error = %v, want LookPath error", err)
		}
	})

	t.Run("nil UserConfigDir", func(t *testing.T) {
		manager := NewSystemdUserManager("linux", &recordingSystemdRunner{}, func(string) (string, error) { return "systemctl", nil }, nil, nil)
		if err := manager.Uninstall(context.Background()); err == nil || !strings.Contains(err.Error(), "UserConfigDir") {
			t.Fatalf("Uninstall() error = %v, want UserConfigDir error", err)
		}
	})

	t.Run("lookup error", func(t *testing.T) {
		lookupErr := errors.New("config lookup failed")
		manager := NewSystemdUserManager("linux", &recordingSystemdRunner{}, func(string) (string, error) { return "systemctl", nil }, func() (string, error) { return "", lookupErr }, nil)
		if err := manager.Uninstall(context.Background()); !errors.Is(err, lookupErr) {
			t.Fatalf("Uninstall() error = %v, want wrapped %v", err, lookupErr)
		}
	})

	t.Run("empty root", func(t *testing.T) {
		manager := NewSystemdUserManager("linux", &recordingSystemdRunner{}, func(string) (string, error) { return "systemctl", nil }, func() (string, error) { return "", nil }, nil)
		if err := manager.Uninstall(context.Background()); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("Uninstall() error = %v, want empty root error", err)
		}
	})
}

func TestSystemdUserManagerUninstallSystemctlPreflightFailureLeavesUnit(t *testing.T) {
	root := t.TempDir()
	unitPath, original := writeOwnedSystemdTestUnit(t, root)
	probeErr := errors.New("no user manager")
	runner := &recordingSystemdRunner{responses: map[string]systemdRunnerResponse{
		"--user\x00show-environment": {
			result: CommandResult{Diagnostic: []byte("Failed to connect to bus\n"), ExitCode: 1},
			err:    probeErr,
		},
	}}
	manager := newSystemdUninstallTestManager(root, "relative/systemctl", runner)

	err := manager.Uninstall(context.Background())
	if !errors.Is(err, probeErr) {
		t.Fatalf("Uninstall() error = %v, want wrapped %v", err, probeErr)
	}
	if !strings.Contains(err.Error(), "show-environment") || !strings.Contains(err.Error(), "Failed to connect") {
		t.Errorf("Uninstall() error = %q, want probe stage and diagnostic", err)
	}
	got, readErr := os.ReadFile(unitPath)
	if readErr != nil {
		t.Fatalf("ReadFile(): %v", readErr)
	}
	if !reflect.DeepEqual(got, original) {
		t.Errorf("unit changed to %q, want %q", got, original)
	}
	if len(runner.calls) != 1 || !filepath.IsAbs(runner.calls[0].name) {
		t.Errorf("Run calls = %#v, want one absolute-path probe", runner.calls)
	}
}

func TestSystemdUserManagerRunSystemctlPreservesContextError(t *testing.T) {
	tests := []struct {
		name    string
		context func(*testing.T) context.Context
		wantErr error
	}{
		{
			name: "canceled",
			context: func(_ *testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr: context.Canceled,
		},
		{
			name: "deadline exceeded",
			context: func(t *testing.T) context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
				t.Cleanup(cancel)
				return ctx
			},
			wantErr: context.DeadlineExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runnerErr := errors.New("runner stopped")
			runner := &recordingSystemdRunner{responses: map[string]systemdRunnerResponse{
				"--user\x00show-environment": {
					result: CommandResult{Diagnostic: []byte("context diagnostic\n"), ExitCode: 1},
					err:    runnerErr,
				},
			}}
			manager := newSystemdUninstallTestManager(t.TempDir(), "/systemctl", runner)

			_, err := manager.runSystemctl(tt.context(t), "/systemctl", "probe user manager", "--user", "show-environment")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("runSystemctl() error = %v, want wrapped %v", err, tt.wantErr)
			}
			if !errors.Is(err, runnerErr) {
				t.Errorf("runSystemctl() error = %v, want wrapped runner error %v", err, runnerErr)
			}
			if !strings.Contains(err.Error(), "probe user manager") || !strings.Contains(err.Error(), "context diagnostic") {
				t.Errorf("runSystemctl() error = %q, want operation and diagnostic", err)
			}
		})
	}
}

type systemdInstallUninstallKey struct{}

type serialSystemdRunner struct {
	blockActor string
	blockArgs  []string
	blocked    chan struct{}
	release    chan struct{}
	blockOnce  sync.Once
	mu         sync.Mutex
	calls      []string
}

func (r *serialSystemdRunner) Run(ctx context.Context, _ string, args ...string) (CommandResult, error) {
	actor, _ := ctx.Value(systemdInstallUninstallKey{}).(string)
	command := actor + ": " + strings.Join(args, " ")
	r.mu.Lock()
	r.calls = append(r.calls, command)
	r.mu.Unlock()

	if actor == r.blockActor && reflect.DeepEqual(args, r.blockArgs) {
		r.blockOnce.Do(func() { close(r.blocked) })
		<-r.release
	}
	return CommandResult{}, nil
}

func TestSystemdUserManagerUninstallSerializesWithInstallActivation(t *testing.T) {
	root := t.TempDir()
	executablePath := makeSystemdTestExecutable(t, root)
	unitPath := filepath.Join(root, "systemd", "user", SystemdUserUnitName)
	runner := &serialSystemdRunner{
		blockActor: "install",
		blockArgs:  []string{"--user", "daemon-reload"},
		blocked:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	manager := newSystemdTestManager(root, executablePath, filepath.Join(root, "systemctl"), runner)
	onLockContention, uninstallWaiting := newSystemdLockContentionBarrier()
	manager.onLockContention = onLockContention
	installDone := make(chan error, 1)
	go func() {
		_, err := manager.Install(
			context.WithValue(context.Background(), systemdInstallUninstallKey{}, "install"),
			SystemdInstallOptions{Port: "8301"},
		)
		installDone <- err
	}()
	<-runner.blocked

	uninstallDone := make(chan error, 1)
	go func() {
		uninstallDone <- manager.Uninstall(context.WithValue(context.Background(), systemdInstallUninstallKey{}, "uninstall"))
	}()

	<-uninstallWaiting
	select {
	case err := <-uninstallDone:
		t.Fatalf("Uninstall() completed while Install() activation held lock: %v", err)
	default:
	}
	close(runner.release)
	if err := <-installDone; err != nil {
		t.Fatalf("Install() error = %v, want nil", err)
	}
	if err := <-uninstallDone; err != nil {
		t.Fatalf("Uninstall() error = %v, want nil", err)
	}

	runner.mu.Lock()
	calls := append([]string(nil), runner.calls...)
	runner.mu.Unlock()
	want := []string{
		"install: --user show-environment",
		"install: --user daemon-reload",
		"install: --user enable " + SystemdUserUnitName,
		"install: --user restart " + SystemdUserUnitName,
		"uninstall: --user show-environment",
		"uninstall: --user disable --now " + SystemdUserUnitName,
		"uninstall: --user daemon-reload",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("systemctl sequence = %#v, want %#v", calls, want)
	}
	assertPathDoesNotExist(t, unitPath)
}

func TestSystemdUserManagerInstallWaitsForUninstallAndInstallsAbsentTarget(t *testing.T) {
	root := t.TempDir()
	unitPath, _ := writeOwnedSystemdTestUnit(t, root)
	executablePath := makeSystemdTestExecutable(t, root)
	systemctlPath := filepath.Join(root, "systemctl")
	runner := &serialSystemdRunner{
		blockActor: "uninstall",
		blockArgs:  []string{"--user", "disable", "--now", SystemdUserUnitName},
		blocked:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	uninstallManager := newSystemdUninstallTestManager(root, systemctlPath, runner)
	installManager := newSystemdTestManager(root, executablePath, systemctlPath, runner)
	onLockContention, installWaiting := newSystemdLockContentionBarrier()
	installManager.onLockContention = onLockContention

	uninstallDone := make(chan error, 1)
	go func() {
		uninstallDone <- uninstallManager.Uninstall(context.WithValue(context.Background(), systemdInstallUninstallKey{}, "uninstall"))
	}()
	<-runner.blocked

	type installOutcome struct {
		result SystemdInstallResult
		err    error
	}
	installDone := make(chan installOutcome, 1)
	go func() {
		result, err := installManager.Install(
			context.WithValue(context.Background(), systemdInstallUninstallKey{}, "install"),
			SystemdInstallOptions{Port: "8401"},
		)
		installDone <- installOutcome{result: result, err: err}
	}()
	<-installWaiting
	select {
	case outcome := <-installDone:
		t.Fatalf("Install() completed while Uninstall() held lock: result=%+v error=%v", outcome.result, outcome.err)
	default:
	}
	close(runner.release)

	if err := <-uninstallDone; err != nil {
		t.Fatalf("Uninstall() error = %v, want nil", err)
	}
	outcome := <-installDone
	if outcome.err != nil {
		t.Fatalf("Install() error = %v, want nil", outcome.err)
	}
	if outcome.result.UnitPath != unitPath {
		t.Errorf("Install() UnitPath = %q, want %q", outcome.result.UnitPath, unitPath)
	}

	runner.mu.Lock()
	calls := append([]string(nil), runner.calls...)
	runner.mu.Unlock()
	want := []string{
		"uninstall: --user show-environment",
		"uninstall: --user disable --now " + SystemdUserUnitName,
		"install: --user show-environment",
		"uninstall: --user daemon-reload",
		"install: --user daemon-reload",
		"install: --user enable " + SystemdUserUnitName,
		"install: --user restart " + SystemdUserUnitName,
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("systemctl sequence = %#v, want %#v", calls, want)
	}
	content, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("ReadFile(): %v", err)
	}
	if !strings.Contains(string(content), `"--port" "8401"`) {
		t.Errorf("final unit = %q, want installed port", content)
	}
}

func TestSystemdUserManagerUninstallPreservesForeignUnitReplacedDuringDisable(t *testing.T) {
	root := t.TempDir()
	unitPath, _ := writeOwnedSystemdTestUnit(t, root)
	systemctlPath := filepath.Join(root, "systemctl")
	foreign := []byte("# Foreign replacement\n[Unit]\nDescription=Foreign\n")
	runner := &serialSystemdRunner{
		blockActor: "uninstall",
		blockArgs:  []string{"--user", "disable", "--now", SystemdUserUnitName},
		blocked:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	manager := newSystemdUninstallTestManager(root, systemctlPath, runner)
	done := make(chan error, 1)
	go func() {
		done <- manager.Uninstall(context.WithValue(context.Background(), systemdInstallUninstallKey{}, "uninstall"))
	}()
	<-runner.blocked
	temporaryPath := filepath.Join(filepath.Dir(unitPath), "foreign.service")
	if err := os.WriteFile(temporaryPath, foreign, 0o644); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}
	if err := os.Rename(temporaryPath, unitPath); err != nil {
		t.Fatalf("Rename(): %v", err)
	}
	close(runner.release)

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("Uninstall() error = %v, want ownership error", err)
	}
	content, readErr := os.ReadFile(unitPath)
	if readErr != nil {
		t.Fatalf("ReadFile(): %v", readErr)
	}
	if !reflect.DeepEqual(content, foreign) {
		t.Errorf("unit changed to %q, want foreign replacement %q", content, foreign)
	}
	runner.mu.Lock()
	calls := append([]string(nil), runner.calls...)
	runner.mu.Unlock()
	want := []string{
		"uninstall: --user show-environment",
		"uninstall: --user disable --now " + SystemdUserUnitName,
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("systemctl sequence = %#v, want %#v", calls, want)
	}
}

func TestSystemdUserManagerUninstallCancelsWhileWaitingForUnitLock(t *testing.T) {
	root := t.TempDir()
	unitPath, original := writeOwnedSystemdTestUnit(t, root)
	lockFile := openSystemdTestLockFile(t, unitPath)
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX); err != nil {
		t.Fatalf("hold systemd unit lock: %v", err)
	}
	configResolved := make(chan struct{})
	lookPathCalls := 0
	runner := &recordingSystemdRunner{}
	manager := NewSystemdUserManager(
		"linux",
		runner,
		func(string) (string, error) {
			lookPathCalls++
			return filepath.Join(root, "systemctl"), nil
		},
		func() (string, error) {
			close(configResolved)
			return root, nil
		},
		nil,
	)
	onLockContention, waiting := newSystemdLockContentionBarrier()
	manager.onLockContention = onLockContention
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Uninstall(ctx) }()
	<-configResolved
	<-waiting
	cancel()

	uninstallErr := <-done
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_UN); err != nil {
		t.Fatalf("release systemd unit lock: %v", err)
	}
	if err := lockFile.Close(); err != nil {
		t.Fatalf("close systemd unit lock: %v", err)
	}
	if !errors.Is(uninstallErr, context.Canceled) {
		t.Fatalf("Uninstall() error = %v, want context.Canceled", uninstallErr)
	}
	got, readErr := os.ReadFile(unitPath)
	if readErr != nil {
		t.Fatalf("ReadFile(): %v", readErr)
	}
	if !reflect.DeepEqual(got, original) {
		t.Errorf("unit changed to %q, want %q", got, original)
	}
	if lookPathCalls != 0 {
		t.Errorf("LookPath calls = %d, want 0", lookPathCalls)
	}
	if len(runner.calls) != 0 {
		t.Errorf("Run calls = %d, want 0", len(runner.calls))
	}
}

func TestSystemdUserManagerUninstallUnitRemovedWhileWaitingForLock(t *testing.T) {
	root := t.TempDir()
	unitPath, _ := writeOwnedSystemdTestUnit(t, root)
	lockFile := openSystemdTestLockFile(t, unitPath)
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX); err != nil {
		t.Fatalf("hold systemd unit lock: %v", err)
	}
	configResolved := make(chan struct{})
	lookPathCalls := 0
	runner := &recordingSystemdRunner{}
	manager := NewSystemdUserManager(
		"linux",
		runner,
		func(string) (string, error) {
			lookPathCalls++
			return filepath.Join(root, "systemctl"), nil
		},
		func() (string, error) {
			close(configResolved)
			return root, nil
		},
		nil,
	)
	onLockContention, waiting := newSystemdLockContentionBarrier()
	manager.onLockContention = onLockContention
	done := make(chan error, 1)
	go func() { done <- manager.Uninstall(context.Background()) }()
	<-configResolved
	<-waiting
	if err := os.Remove(unitPath); err != nil {
		t.Fatalf("Remove(): %v", err)
	}
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_UN); err != nil {
		t.Fatalf("release systemd unit lock: %v", err)
	}
	if err := lockFile.Close(); err != nil {
		t.Fatalf("close systemd unit lock: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("Uninstall() error = %v, want nil", err)
	}
	if lookPathCalls != 0 {
		t.Errorf("LookPath calls = %d, want 0", lookPathCalls)
	}
	if len(runner.calls) != 0 {
		t.Errorf("Run calls = %d, want 0", len(runner.calls))
	}
}

func newSystemdLockContentionBarrier() (func(), <-chan struct{}) {
	contended := make(chan struct{})
	var once sync.Once
	return func() {
		once.Do(func() { close(contended) })
	}, contended
}

func writeOwnedSystemdTestUnit(t *testing.T, root string) (string, []byte) {
	t.Helper()
	unitPath := filepath.Join(root, "systemd", "user", SystemdUserUnitName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(): %v", err)
	}
	content := []byte(systemdUnitMarker + "\n[Unit]\nDescription=IGoNotes\n")
	if err := os.WriteFile(unitPath, content, 0o644); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}
	return unitPath, content
}

func newSystemdUninstallTestManager(root, systemctlPath string, runner CommandRunner) *SystemdUserManager {
	return NewSystemdUserManager(
		"linux",
		runner,
		func(string) (string, error) { return systemctlPath, nil },
		func() (string, error) { return root, nil },
		nil,
	)
}
