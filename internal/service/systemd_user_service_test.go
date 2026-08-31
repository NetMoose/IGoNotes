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

type systemdRunnerCall struct {
	name string
	args []string
}

type systemdRunnerResponse struct {
	result CommandResult
	err    error
}

type recordingSystemdRunner struct {
	calls     []systemdRunnerCall
	responses map[string]systemdRunnerResponse
}

func (r *recordingSystemdRunner) Run(_ context.Context, name string, args ...string) (CommandResult, error) {
	call := systemdRunnerCall{name: name, args: append([]string(nil), args...)}
	r.calls = append(r.calls, call)
	response := r.responses[strings.Join(args, "\x00")]
	return response.result, response.err
}

func TestSystemdUserManagerInstallSuccess(t *testing.T) {
	root := t.TempDir()
	executablePath := makeSystemdTestExecutable(t, root)
	systemctlPath := filepath.Join(root, "bin", "systemctl")
	runner := &recordingSystemdRunner{}
	manager := newSystemdTestManager(root, executablePath, systemctlPath, runner)
	options := SystemdInstallOptions{
		Port:      "9000",
		ConfigDir: filepath.Join(root, "application config"),
		Base:      "work notes",
	}

	result, err := manager.Install(context.Background(), options)
	if err != nil {
		t.Fatalf("Install() error = %v, want nil", err)
	}

	wantUnitPath := filepath.Join(root, "systemd", "user", SystemdUserUnitName)
	if result.UnitPath != wantUnitPath {
		t.Errorf("Install() UnitPath = %q, want %q", result.UnitPath, wantUnitPath)
	}
	if result.URL != "http://127.0.0.1:9000" {
		t.Errorf("Install() URL = %q, want %q", result.URL, "http://127.0.0.1:9000")
	}

	wantContent := systemdUnitMarker + `
[Unit]
Description=IGoNotes local note server

[Service]
Type=simple
ExecStart=:"` + executablePath + `" "--port" "9000" "--config" "` + options.ConfigDir + `" "--base" "work notes" "--no-browser"
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`
	assertSystemdUnit(t, wantUnitPath, wantContent)
	assertNoSystemdTempFiles(t, filepath.Dir(wantUnitPath))
	assertSystemdCalls(t, runner.calls, systemctlPath,
		[]string{"--user", "show-environment"},
		[]string{"--user", "daemon-reload"},
		[]string{"--user", "enable", SystemdUserUnitName},
		[]string{"--user", "restart", SystemdUserUnitName},
	)
}

func TestSystemdUserManagerInstallRepeatsOwnedReplacement(t *testing.T) {
	root := t.TempDir()
	executablePath := makeSystemdTestExecutable(t, root)
	systemctlPath := filepath.Join(root, "systemctl")
	runner := &recordingSystemdRunner{}
	manager := newSystemdTestManager(root, executablePath, systemctlPath, runner)

	if _, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080", Base: "old"}); err != nil {
		t.Fatalf("first Install() error = %v, want nil", err)
	}
	result, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8181", Base: "new"})
	if err != nil {
		t.Fatalf("second Install() error = %v, want nil", err)
	}

	content, err := os.ReadFile(result.UnitPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", result.UnitPath, err)
	}
	if !strings.Contains(string(content), `"--port" "8181"`) || !strings.Contains(string(content), `"--base" "new"`) {
		t.Errorf("replacement unit content = %q, want updated options", content)
	}
	if strings.Contains(string(content), `"--base" "old"`) {
		t.Errorf("replacement unit content = %q, do not want old options", content)
	}
	assertNoSystemdTempFiles(t, filepath.Dir(result.UnitPath))
	if len(runner.calls) != 8 {
		t.Fatalf("Run calls = %d, want 8", len(runner.calls))
	}
	assertSystemdCalls(t, runner.calls[4:], systemctlPath,
		[]string{"--user", "show-environment"},
		[]string{"--user", "daemon-reload"},
		[]string{"--user", "enable", SystemdUserUnitName},
		[]string{"--user", "restart", SystemdUserUnitName},
	)
}

func TestSystemdUserManagerInstallRejectsNilDependencies(t *testing.T) {
	dependencies := []struct {
		name       string
		managerFor func(root string, runner CommandRunner) *SystemdUserManager
	}{
		{
			name: "runner",
			managerFor: func(root string, _ CommandRunner) *SystemdUserManager {
				return NewSystemdUserManager("linux", nil, successfulLookPath(root), func() (string, error) { return root, nil }, successfulExecutable(root))
			},
		},
		{
			name: "lookPath",
			managerFor: func(root string, runner CommandRunner) *SystemdUserManager {
				return NewSystemdUserManager("linux", runner, nil, func() (string, error) { return root, nil }, successfulExecutable(root))
			},
		},
		{
			name: "userConfigDir",
			managerFor: func(root string, runner CommandRunner) *SystemdUserManager {
				return NewSystemdUserManager("linux", runner, successfulLookPath(root), nil, successfulExecutable(root))
			},
		},
		{
			name: "executable",
			managerFor: func(root string, runner CommandRunner) *SystemdUserManager {
				return NewSystemdUserManager("linux", runner, successfulLookPath(root), func() (string, error) { return root, nil }, nil)
			},
		},
	}
	for _, tt := range dependencies {
		t.Run("nil "+tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "must-not-exist")
			runner := &recordingSystemdRunner{}
			manager := tt.managerFor(root, runner)

			_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
			if err == nil || !strings.Contains(err.Error(), "nil") {
				t.Fatalf("Install() error = %v, want nil dependency error", err)
			}
			if len(runner.calls) != 0 {
				t.Errorf("Run calls = %d, want 0", len(runner.calls))
			}
			if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("Stat(%q) error = %v, want not exist", root, statErr)
			}
		})
	}
}

func TestSystemdUserManagerInstallRejectsInvalidPortsBeforeDependencies(t *testing.T) {
	for _, port := range []string{"", "0", "-1", "+8080", " 8080", "8080 ", "\t8080", "abc", "65536"} {
		t.Run(port, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "must-not-exist")
			runner := &recordingSystemdRunner{}
			called := false
			dependency := func() (string, error) {
				called = true
				return root, nil
			}
			manager := NewSystemdUserManager("linux", runner, func(string) (string, error) {
				called = true
				return "systemctl", nil
			}, dependency, dependency)

			_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: port})
			if err == nil || !strings.Contains(err.Error(), "port") {
				t.Fatalf("Install() error = %v, want port validation error", err)
			}
			if called {
				t.Error("Install() called a dependency before rejecting invalid port")
			}
			if len(runner.calls) != 0 {
				t.Errorf("Run calls = %d, want 0", len(runner.calls))
			}
			if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("Stat(%q) error = %v, want not exist", root, statErr)
			}
		})
	}
}

func TestSystemdUserManagerInstallRejectsSystemctlFailuresBeforeDirectories(t *testing.T) {
	t.Run("empty config root", func(t *testing.T) {
		runner := &recordingSystemdRunner{}
		manager := NewSystemdUserManager(
			"linux",
			runner,
			func(string) (string, error) { return "systemctl", nil },
			func() (string, error) { return "", nil },
			func() (string, error) { return "/unused", nil },
		)

		_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
		if err == nil || !strings.Contains(err.Error(), "config") {
			t.Fatalf("Install() error = %v, want empty config root error", err)
		}
		if len(runner.calls) != 0 {
			t.Errorf("Run calls = %d, want 0", len(runner.calls))
		}
	})

	t.Run("missing systemctl", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "must-not-exist")
		lookPathErr := errors.New("systemctl not found")
		runner := &recordingSystemdRunner{}
		manager := NewSystemdUserManager(
			"linux",
			runner,
			func(name string) (string, error) {
				if name != "systemctl" {
					t.Errorf("LookPath name = %q, want systemctl", name)
				}
				return "", lookPathErr
			},
			func() (string, error) { return root, nil },
			func() (string, error) { return "/unused", nil },
		)

		_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
		if !errors.Is(err, lookPathErr) {
			t.Fatalf("Install() error = %v, want wrapped %v", err, lookPathErr)
		}
		if len(runner.calls) != 0 {
			t.Errorf("Run calls = %d, want 0", len(runner.calls))
		}
		assertPathDoesNotExist(t, root)
	})

	t.Run("show environment failure", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "must-not-exist")
		commandErr := errors.New("exit status 1")
		runner := &recordingSystemdRunner{responses: map[string]systemdRunnerResponse{
			"--user\x00show-environment": {
				result: CommandResult{Diagnostic: []byte("Failed to connect to bus\n"), ExitCode: 1},
				err:    commandErr,
			},
		}}
		manager := newSystemdTestManager(root, "/unused", "relative/systemctl", runner)

		_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
		if !errors.Is(err, commandErr) {
			t.Fatalf("Install() error = %v, want wrapped %v", err, commandErr)
		}
		if !strings.Contains(err.Error(), "show-environment") || !strings.Contains(err.Error(), "Failed to connect to bus") {
			t.Errorf("Install() error = %q, want operation and diagnostic", err)
		}
		if len(runner.calls) != 1 {
			t.Fatalf("Run calls = %d, want 1", len(runner.calls))
		}
		if !filepath.IsAbs(runner.calls[0].name) {
			t.Errorf("Run executable = %q, want absolute path", runner.calls[0].name)
		}
		assertPathDoesNotExist(t, root)
	})
}

func TestSystemdUserManagerInstallRejectsInvalidExecutable(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string) string
	}{
		{
			name: "missing",
			setup: func(_ *testing.T, root string) string {
				return filepath.Join(root, "missing")
			},
		},
		{
			name: "nonregular",
			setup: func(t *testing.T, root string) string {
				path := filepath.Join(root, "executable-directory")
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatalf("Mkdir(%q): %v", path, err)
				}
				return path
			},
		},
		{
			name: "nonexecutable",
			setup: func(t *testing.T, root string) string {
				path := filepath.Join(root, "igonotes")
				if err := os.WriteFile(path, []byte("binary"), 0o644); err != nil {
					t.Fatalf("WriteFile(%q): %v", path, err)
				}
				return path
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			configRoot := filepath.Join(root, "config")
			executablePath := tt.setup(t, root)
			runner := &recordingSystemdRunner{}
			manager := newSystemdTestManager(configRoot, executablePath, filepath.Join(root, "systemctl"), runner)

			_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
			if err == nil || !strings.Contains(err.Error(), "executable") {
				t.Fatalf("Install() error = %v, want executable validation error", err)
			}
			if len(runner.calls) != 1 {
				t.Errorf("Run calls = %d, want only show-environment", len(runner.calls))
			}
			assertPathDoesNotExist(t, configRoot)
		})
	}
}

func TestSystemdUserManagerInstallPreservesForeignUnit(t *testing.T) {
	root := t.TempDir()
	unitPath := filepath.Join(root, "systemd", "user", SystemdUserUnitName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(): %v", err)
	}
	foreign := []byte("# Foreign service\n[Unit]\n" + systemdUnitMarker + "\n")
	if err := os.WriteFile(unitPath, foreign, 0o600); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}
	executableCalled := false
	runner := &recordingSystemdRunner{}
	manager := NewSystemdUserManager(
		"linux",
		runner,
		func(string) (string, error) { return filepath.Join(root, "systemctl"), nil },
		func() (string, error) { return root, nil },
		func() (string, error) {
			executableCalled = true
			return "/unused", nil
		},
	)

	_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
	if err == nil || !strings.Contains(err.Error(), "managed") {
		t.Fatalf("Install() error = %v, want foreign unit ownership error", err)
	}
	got, readErr := os.ReadFile(unitPath)
	if readErr != nil {
		t.Fatalf("ReadFile(): %v", readErr)
	}
	if !reflect.DeepEqual(got, foreign) {
		t.Errorf("foreign unit changed to %q, want %q", got, foreign)
	}
	if executableCalled {
		t.Error("Install() resolved executable after detecting foreign unit")
	}
	if len(runner.calls) != 1 {
		t.Errorf("Run calls = %d, want only show-environment", len(runner.calls))
	}
	assertNoSystemdTempFiles(t, filepath.Dir(unitPath))
}

func TestSystemdUserManagerInstallPreservesForeignUnitCreatedAfterPreflight(t *testing.T) {
	root := t.TempDir()
	executablePath := makeSystemdTestExecutable(t, root)
	unitPath := filepath.Join(root, "systemd", "user", SystemdUserUnitName)
	foreign := []byte("# Created after preflight\n[Unit]\nDescription=Foreign\n")
	runner := &recordingSystemdRunner{}
	manager := NewSystemdUserManager(
		"linux",
		runner,
		func(string) (string, error) { return filepath.Join(root, "systemctl"), nil },
		func() (string, error) { return root, nil },
		func() (string, error) {
			if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
				t.Fatalf("MkdirAll(): %v", err)
			}
			if err := os.WriteFile(unitPath, foreign, 0o600); err != nil {
				t.Fatalf("WriteFile(): %v", err)
			}
			return executablePath, nil
		},
	)

	_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
	if err == nil || !strings.Contains(err.Error(), "replace") {
		t.Fatalf("Install() error = %v, want replacement race error", err)
	}
	got, readErr := os.ReadFile(unitPath)
	if readErr != nil {
		t.Fatalf("ReadFile(): %v", readErr)
	}
	if !reflect.DeepEqual(got, foreign) {
		t.Errorf("racing foreign unit changed to %q, want %q", got, foreign)
	}
	assertSystemdCalls(t, runner.calls, filepath.Join(root, "systemctl"),
		[]string{"--user", "show-environment"},
	)
	assertNoSystemdTempFiles(t, filepath.Dir(unitPath))
}

func TestSystemdUserManagerInstallRevalidatesOwnedUnitBeforeReplacement(t *testing.T) {
	root := t.TempDir()
	executablePath := makeSystemdTestExecutable(t, root)
	unitPath := filepath.Join(root, "systemd", "user", SystemdUserUnitName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(): %v", err)
	}
	if err := os.WriteFile(unitPath, []byte(systemdUnitMarker+"\nold managed unit\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}
	foreign := []byte("# Replaced after preflight\n[Unit]\nDescription=Foreign\n")
	runner := &recordingSystemdRunner{}
	manager := NewSystemdUserManager(
		"linux",
		runner,
		func(string) (string, error) { return filepath.Join(root, "systemctl"), nil },
		func() (string, error) { return root, nil },
		func() (string, error) {
			if err := os.WriteFile(unitPath, foreign, 0o600); err != nil {
				t.Fatalf("WriteFile(): %v", err)
			}
			return executablePath, nil
		},
	)

	_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
	if err == nil || !strings.Contains(err.Error(), "managed") {
		t.Fatalf("Install() error = %v, want final ownership error", err)
	}
	got, readErr := os.ReadFile(unitPath)
	if readErr != nil {
		t.Fatalf("ReadFile(): %v", readErr)
	}
	if !reflect.DeepEqual(got, foreign) {
		t.Errorf("racing foreign unit changed to %q, want %q", got, foreign)
	}
	assertSystemdCalls(t, runner.calls, filepath.Join(root, "systemctl"),
		[]string{"--user", "show-environment"},
	)
	assertNoSystemdTempFiles(t, filepath.Dir(unitPath))
}

type concurrentSystemdInstallKey struct{}

type concurrentSystemdActivation struct {
	installer string
	command   string
	unitPort  string
}

type concurrentSystemdRunner struct {
	unitPath      string
	firstReload   chan struct{}
	releaseFirst  chan struct{}
	firstOnce     sync.Once
	mu            sync.Mutex
	activations   []concurrentSystemdActivation
	contentErrors []error
}

func (r *concurrentSystemdRunner) Run(ctx context.Context, _ string, args ...string) (CommandResult, error) {
	if reflect.DeepEqual(args, []string{"--user", "show-environment"}) {
		return CommandResult{}, nil
	}

	installer, _ := ctx.Value(concurrentSystemdInstallKey{}).(string)
	content, err := os.ReadFile(r.unitPath)
	port := ""
	if err == nil {
		for _, candidate := range []string{"8101", "8102"} {
			if strings.Contains(string(content), `"--port" "`+candidate+`"`) {
				port = candidate
				break
			}
		}
	}

	r.mu.Lock()
	r.activations = append(r.activations, concurrentSystemdActivation{
		installer: installer,
		command:   strings.Join(args, " "),
		unitPort:  port,
	})
	if err != nil {
		r.contentErrors = append(r.contentErrors, err)
	}
	r.mu.Unlock()

	if installer == "first" && reflect.DeepEqual(args, []string{"--user", "daemon-reload"}) {
		r.firstOnce.Do(func() { close(r.firstReload) })
		<-r.releaseFirst
	}
	return CommandResult{}, nil
}

func TestSystemdUserManagerInstallSerializesUnitAndActivation(t *testing.T) {
	root := t.TempDir()
	executablePath := makeSystemdTestExecutable(t, root)
	unitPath := filepath.Join(root, "systemd", "user", SystemdUserUnitName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(): %v", err)
	}
	if err := os.WriteFile(unitPath, []byte(systemdUnitMarker+"\nold managed unit\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}

	runner := &concurrentSystemdRunner{
		unitPath:     unitPath,
		firstReload:  make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	firstManager := newSystemdTestManager(root, executablePath, filepath.Join(root, "systemctl"), runner)
	secondExecutableReady := make(chan struct{})
	secondManager := NewSystemdUserManager(
		"linux",
		runner,
		func(string) (string, error) { return filepath.Join(root, "systemctl"), nil },
		func() (string, error) { return root, nil },
		func() (string, error) {
			close(secondExecutableReady)
			return executablePath, nil
		},
	)

	type installOutcome struct {
		result SystemdInstallResult
		err    error
	}
	firstDone := make(chan installOutcome, 1)
	go func() {
		result, err := firstManager.Install(
			context.WithValue(context.Background(), concurrentSystemdInstallKey{}, "first"),
			SystemdInstallOptions{Port: "8101", Base: "first"},
		)
		firstDone <- installOutcome{result: result, err: err}
	}()
	<-runner.firstReload

	lockFile := openSystemdTestLockFile(t, unitPath)
	lockErr := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if lockErr == nil {
		if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_UN); err != nil {
			t.Errorf("unlock unexpected test lock: %v", err)
		}
		t.Error("systemd unit lock was not held during daemon-reload")
	} else if !errors.Is(lockErr, unix.EWOULDBLOCK) && !errors.Is(lockErr, unix.EAGAIN) {
		t.Fatalf("probe systemd unit lock: %v", lockErr)
	}
	if err := lockFile.Close(); err != nil {
		t.Fatalf("close lock probe: %v", err)
	}

	secondDone := make(chan installOutcome, 1)
	go func() {
		result, err := secondManager.Install(
			context.WithValue(context.Background(), concurrentSystemdInstallKey{}, "second"),
			SystemdInstallOptions{Port: "8102", Base: "second"},
		)
		secondDone <- installOutcome{result: result, err: err}
	}()
	<-secondExecutableReady

	contentionTimer := time.NewTimer(50 * time.Millisecond)
	var secondOutcome installOutcome
	secondCompleted := false
	select {
	case secondOutcome = <-secondDone:
		secondCompleted = true
		t.Errorf("second Install() completed before first activation was released: result=%+v error=%v", secondOutcome.result, secondOutcome.err)
	case <-contentionTimer.C:
	}
	if !contentionTimer.Stop() {
		select {
		case <-contentionTimer.C:
		default:
		}
	}
	close(runner.releaseFirst)

	firstOutcome := <-firstDone
	if !secondCompleted {
		secondOutcome = <-secondDone
	}
	if firstOutcome.err != nil {
		t.Fatalf("first Install() error = %v, want nil", firstOutcome.err)
	}
	if secondOutcome.err != nil {
		t.Fatalf("second Install() error = %v, want nil", secondOutcome.err)
	}
	if firstOutcome.result.URL != "http://127.0.0.1:8101" {
		t.Errorf("first Install() URL = %q", firstOutcome.result.URL)
	}
	if secondOutcome.result.URL != "http://127.0.0.1:8102" {
		t.Errorf("second Install() URL = %q", secondOutcome.result.URL)
	}

	runner.mu.Lock()
	activations := append([]concurrentSystemdActivation(nil), runner.activations...)
	contentErrors := append([]error(nil), runner.contentErrors...)
	runner.mu.Unlock()
	if len(contentErrors) != 0 {
		t.Errorf("unit reads during activation failed: %v", contentErrors)
	}
	wantActivations := []concurrentSystemdActivation{
		{installer: "first", command: "--user daemon-reload", unitPort: "8101"},
		{installer: "first", command: "--user enable " + SystemdUserUnitName, unitPort: "8101"},
		{installer: "first", command: "--user restart " + SystemdUserUnitName, unitPort: "8101"},
		{installer: "second", command: "--user daemon-reload", unitPort: "8102"},
		{installer: "second", command: "--user enable " + SystemdUserUnitName, unitPort: "8102"},
		{installer: "second", command: "--user restart " + SystemdUserUnitName, unitPort: "8102"},
	}
	if !reflect.DeepEqual(activations, wantActivations) {
		t.Errorf("activation sequence = %#v, want %#v", activations, wantActivations)
	}
	content, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("ReadFile(): %v", err)
	}
	if !strings.Contains(string(content), `"--port" "8102"`) {
		t.Errorf("final unit = %q, want second install", content)
	}
}

func TestSystemdUserManagerInstallCancelsWhileWaitingForUnitLock(t *testing.T) {
	root := t.TempDir()
	executablePath := makeSystemdTestExecutable(t, root)
	unitPath := filepath.Join(root, "systemd", "user", SystemdUserUnitName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(): %v", err)
	}
	original := []byte(systemdUnitMarker + "\noriginal managed unit\n")
	if err := os.WriteFile(unitPath, original, 0o644); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}

	lockFile := openSystemdTestLockFile(t, unitPath)
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX); err != nil {
		t.Fatalf("hold systemd unit lock: %v", err)
	}
	executableReady := make(chan struct{})
	runner := &recordingSystemdRunner{}
	manager := NewSystemdUserManager(
		"linux",
		runner,
		func(string) (string, error) { return filepath.Join(root, "systemctl"), nil },
		func() (string, error) { return root, nil },
		func() (string, error) {
			close(executableReady)
			return executablePath, nil
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := manager.Install(ctx, SystemdInstallOptions{Port: "8201"})
		done <- err
	}()
	<-executableReady
	cancel()

	cancelTimer := time.NewTimer(250 * time.Millisecond)
	var installErr error
	timedOut := false
	select {
	case installErr = <-done:
	case <-cancelTimer.C:
		timedOut = true
		installErr = errors.New("Install did not observe context cancellation while waiting for lock")
	}
	if !cancelTimer.Stop() {
		select {
		case <-cancelTimer.C:
		default:
		}
	}
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_UN); err != nil {
		t.Fatalf("release systemd unit lock: %v", err)
	}
	if err := lockFile.Close(); err != nil {
		t.Fatalf("close systemd unit lock: %v", err)
	}
	if timedOut {
		<-done
	}
	if !errors.Is(installErr, context.Canceled) {
		t.Fatalf("Install() error = %v, want context.Canceled", installErr)
	}

	content, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("ReadFile(): %v", err)
	}
	if !reflect.DeepEqual(content, original) {
		t.Errorf("unit changed to %q, want %q", content, original)
	}
	assertSystemdCalls(t, runner.calls, filepath.Join(root, "systemctl"),
		[]string{"--user", "show-environment"},
	)
	assertNoSystemdTempFiles(t, filepath.Dir(unitPath))
}

func openSystemdTestLockFile(t *testing.T, unitPath string) *os.File {
	t.Helper()
	lockPath := filepath.Join(filepath.Dir(unitPath), "."+filepath.Base(unitPath)+".lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", lockPath, err)
	}
	return lockFile
}

func TestSystemdUserManagerInstallActivationFailures(t *testing.T) {
	tests := []struct {
		name       string
		failedArgs []string
		wantCalls  [][]string
	}{
		{
			name:       "reload",
			failedArgs: []string{"--user", "daemon-reload"},
			wantCalls: [][]string{
				{"--user", "show-environment"},
				{"--user", "daemon-reload"},
			},
		},
		{
			name:       "enable",
			failedArgs: []string{"--user", "enable", SystemdUserUnitName},
			wantCalls: [][]string{
				{"--user", "show-environment"},
				{"--user", "daemon-reload"},
				{"--user", "enable", SystemdUserUnitName},
			},
		},
		{
			name:       "restart",
			failedArgs: []string{"--user", "restart", SystemdUserUnitName},
			wantCalls: [][]string{
				{"--user", "show-environment"},
				{"--user", "daemon-reload"},
				{"--user", "enable", SystemdUserUnitName},
				{"--user", "restart", SystemdUserUnitName},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			executablePath := makeSystemdTestExecutable(t, root)
			systemctlPath := filepath.Join(root, "systemctl")
			commandErr := errors.New("activation failed")
			runner := &recordingSystemdRunner{responses: map[string]systemdRunnerResponse{
				strings.Join(tt.failedArgs, "\x00"): {
					result: CommandResult{Diagnostic: []byte("manager diagnostic\n"), ExitCode: 1},
					err:    commandErr,
				},
			}}
			manager := newSystemdTestManager(root, executablePath, systemctlPath, runner)

			_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8765"})
			if !errors.Is(err, commandErr) {
				t.Fatalf("Install() error = %v, want wrapped %v", err, commandErr)
			}
			for _, fragment := range []string{
				tt.name,
				"manager diagnostic",
				"systemctl --user status " + SystemdUserUnitName,
				"journalctl --user-unit " + SystemdUserUnitName,
			} {
				if !strings.Contains(err.Error(), fragment) {
					t.Errorf("Install() error = %q, want %q", err, fragment)
				}
			}
			unitPath := filepath.Join(root, "systemd", "user", SystemdUserUnitName)
			content, readErr := os.ReadFile(unitPath)
			if readErr != nil {
				t.Fatalf("ReadFile(%q): %v", unitPath, readErr)
			}
			if !strings.HasPrefix(string(content), systemdUnitMarker+"\n") {
				t.Errorf("retained unit = %q, want managed unit", content)
			}
			assertSystemdCalls(t, runner.calls, systemctlPath, tt.wantCalls...)
			assertNoSystemdTempFiles(t, filepath.Dir(unitPath))
		})
	}
}

func newSystemdTestManager(root, executablePath, systemctlPath string, runner CommandRunner) *SystemdUserManager {
	return NewSystemdUserManager(
		"linux",
		runner,
		func(name string) (string, error) {
			if name != "systemctl" {
				return "", errors.New("unexpected executable lookup")
			}
			return systemctlPath, nil
		},
		func() (string, error) { return root, nil },
		func() (string, error) { return executablePath, nil },
	)
}

func successfulLookPath(root string) func(string) (string, error) {
	return func(string) (string, error) { return filepath.Join(root, "systemctl"), nil }
}

func successfulExecutable(root string) func() (string, error) {
	return func() (string, error) { return filepath.Join(root, "igonotes"), nil }
}

func makeSystemdTestExecutable(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "bin", "igonotes")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("binary"), 0o751); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	return path
}

func assertSystemdUnit(t *testing.T, path, wantContent string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if string(content) != wantContent {
		t.Errorf("unit content = %q, want %q", content, wantContent)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q): %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("unit mode = %04o, want 0644", got)
	}
}

func assertNoSystemdTempFiles(t *testing.T, directory string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, ".igonotes-service-*.tmp"))
	if err != nil {
		t.Fatalf("Glob(): %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("temporary files = %q, want none", matches)
	}
}

func assertSystemdCalls(t *testing.T, got []systemdRunnerCall, executable string, args ...[]string) {
	t.Helper()
	want := make([]systemdRunnerCall, len(args))
	for i := range args {
		want[i] = systemdRunnerCall{name: executable, args: args[i]}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Run calls = %#v, want %#v", got, want)
	}
}

func assertPathDoesNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat(%q) error = %v, want not exist", path, err)
	}
}
