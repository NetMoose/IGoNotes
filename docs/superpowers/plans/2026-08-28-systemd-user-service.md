# Systemd User Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `igonotes service install|uninstall` for a generated Linux user-systemd unit and make the local HTTP server shut down cleanly on SIGINT/SIGTERM.

**Architecture:** Keep systemd unit rendering and lifecycle management in focused `internal/service` files, using the existing shell-free `CommandRunner`. Add a small top-level CLI dispatcher before ordinary flag parsing, then move the HTTP listener lifecycle into a testable helper driven by a signal context; server resource construction remains in `cmd/api/main.go` so existing wiring stays recognizable.

**Tech Stack:** Go 1.26, standard `flag`, `net/http`, `os/signal`, user-systemd via `systemctl --user`, existing SQLite and embedded Svelte frontend.

**Design:** `docs/superpowers/specs/2026-08-28-systemd-user-service-design.md`

**Systemd references:** `systemd.service(5)` for direct `ExecStart` execution and the `:` prefix that disables environment substitution; `systemd.syntax(7)` for quoting/C escapes and `%%`; `systemctl(1)` for `daemon-reload`, `enable`, `restart`, and `disable --now`.

**Practical threat model:** The exact marker rejects pre-existing/static foreign units, initially absent creation uses no-replace, and a stable advisory lock serializes cooperating IGoNotes processes. Marker checks and best-effort revalidation are collision safeguards, not a security boundary. Concurrent replacement or editing by another same-user process, direct user action, or user-systemd operation is unsupported because such actors can ignore the advisory lock.

---

## File Structure

- Create `internal/service/systemd_unit.go`: pure install options, constants, systemd argument quoting, and deterministic unit rendering.
- Create `internal/service/systemd_unit_test.go`: byte-exact renderer and escaping tests.
- Create `internal/service/systemd_user_service.go`: platform preflight, marker collision checks, cooperative locking, atomic unit writes, and ordered `systemctl --user` operations.
- Create `internal/service/systemd_user_service_test.go`: fake command runner plus install/uninstall filesystem and failure tests.
- Create `cmd/api/service_command.go`: top-level command dispatch, service subcommand parsing, relative config normalization, and user output.
- Create `cmd/api/service_command_test.go`: dispatch and parser/output contract tests.
- Create `cmd/api/server.go`: loopback endpoint, browser opening, and graceful `http.Server` lifecycle.
- Create `cmd/api/signals_unix.go`: SIGINT/SIGTERM set for Linux and macOS builds.
- Create `cmd/api/signals_windows.go`: portable SIGINT set for Windows builds.
- Modify `cmd/api/main.go`: ordinary server option parsing, resource cleanup, service manager wiring, and signal context.
- Modify `cmd/api/server_address_test.go`: adapt existing listener tests and add graceful/forced shutdown tests.
- Modify `cmd/api/system_routes_test.go`: update the production-wiring assertion for the new server call without weakening route coverage.
- Modify `docs/user.md`: full user-service setup, diagnostics, PWA-port, binary-path, and shutdown documentation.
- Modify `site/docs/user.md`: concise published mirror of the same behavior.

### Task 1: Render Safe Systemd Units

**Files:**
- Create: `internal/service/systemd_unit.go`
- Create: `internal/service/systemd_unit_test.go`

- [ ] **Step 1: Write failing byte-exact renderer tests**

Create `internal/service/systemd_unit_test.go` with tests that establish the exact unit contract and escaping rules:

```go
package service

import (
	"strings"
	"testing"
)

func TestRenderSystemdUserUnit(t *testing.T) {
	got, err := renderSystemdUserUnit(
		`/opt/IGo Notes/igo%notes`,
		SystemdInstallOptions{
			Port:      "8080",
			ConfigDir: `/home/user/My Config/$current`,
			Base:      `work "notes"\draft`,
		},
	)
	if err != nil {
		t.Fatalf("renderSystemdUserUnit() error = %v", err)
	}

	want := `# Managed by IGoNotes
[Unit]
Description=IGoNotes local note server

[Service]
Type=simple
ExecStart=:"/opt/IGo Notes/igo%%notes" "--port" "8080" "--config" "/home/user/My Config/$current" "--base" "work \"notes\"\\draft" "--no-browser"
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`
	if string(got) != want {
		t.Fatalf("unit =\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderSystemdUserUnitOmitsEmptyOptionalArguments(t *testing.T) {
	got, err := renderSystemdUserUnit("/usr/bin/igonotes", SystemdInstallOptions{Port: "8080"})
	if err != nil {
		t.Fatalf("renderSystemdUserUnit() error = %v", err)
	}
	line := `ExecStart=:"/usr/bin/igonotes" "--port" "8080" "--no-browser"`
	if !strings.Contains(string(got), line+"\n") {
		t.Fatalf("unit does not contain %q:\n%s", line, got)
	}
	if strings.Contains(string(got), "--config") || strings.Contains(string(got), "--base") {
		t.Fatalf("unit contains empty optional arguments:\n%s", got)
	}
}

func TestQuoteSystemdExecArgEscapesUnitSyntax(t *testing.T) {
	got, err := quoteSystemdExecArg("space quote\" slash\\ percent% tab\t newline\n")
	if err != nil {
		t.Fatalf("quoteSystemdExecArg() error = %v", err)
	}
	want := `"space quote\" slash\\ percent%% tab\t newline\n"`
	if got != want {
		t.Fatalf("quoteSystemdExecArg() = %q, want %q", got, want)
	}
}

func TestQuoteSystemdExecArgRejectsNUL(t *testing.T) {
	if _, err := quoteSystemdExecArg("bad\x00argument"); err == nil {
		t.Fatal("quoteSystemdExecArg() error = nil, want NUL error")
	}
}
```

- [ ] **Step 2: Run the renderer tests and verify they fail**

Run:

```bash
go test ./internal/service -run '^(TestRenderSystemdUserUnit|TestQuoteSystemdExecArg)' -count=1
```

Expected: build failure because `renderSystemdUserUnit`, `SystemdInstallOptions`, and `quoteSystemdExecArg` do not exist.

- [ ] **Step 3: Implement the pure unit renderer**

Create `internal/service/systemd_unit.go`:

```go
package service

import (
	"fmt"
	"strings"
)

const (
	SystemdUserUnitName = "igonotes.service"
	systemdUnitMarker   = "# Managed by IGoNotes"
)

type SystemdInstallOptions struct {
	Port      string
	ConfigDir string
	Base      string
}

func renderSystemdUserUnit(executable string, options SystemdInstallOptions) ([]byte, error) {
	arguments := []string{executable, "--port", options.Port}
	if options.ConfigDir != "" {
		arguments = append(arguments, "--config", options.ConfigDir)
	}
	if options.Base != "" {
		arguments = append(arguments, "--base", options.Base)
	}
	arguments = append(arguments, "--no-browser")

	quoted := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		value, err := quoteSystemdExecArg(argument)
		if err != nil {
			return nil, err
		}
		quoted = append(quoted, value)
	}

	unit := fmt.Sprintf(`%s
[Unit]
Description=IGoNotes local note server

[Service]
Type=simple
ExecStart=:%s
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`, systemdUnitMarker, strings.Join(quoted, " "))
	return []byte(unit), nil
}

func quoteSystemdExecArg(value string) (string, error) {
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("systemd argument contains NUL")
	}

	var quoted strings.Builder
	quoted.Grow(len(value) + 2)
	quoted.WriteByte('"')
	for _, character := range value {
		switch character {
		case '\\':
			quoted.WriteString(`\\`)
		case '"':
			quoted.WriteString(`\"`)
		case '%':
			quoted.WriteString(`%%`)
		case '\n':
			quoted.WriteString(`\n`)
		case '\r':
			quoted.WriteString(`\r`)
		case '\t':
			quoted.WriteString(`\t`)
		default:
			if character < 0x20 || character == 0x7f {
				quoted.WriteString(fmt.Sprintf(`\x%02x`, character))
				continue
			}
			quoted.WriteRune(character)
		}
	}
	quoted.WriteByte('"')
	return quoted.String(), nil
}
```

The `:` prefix makes `$` literal by disabling systemd environment substitution for this command. Doubling `%` prevents unit specifier expansion. No shell is invoked.

- [ ] **Step 4: Run the renderer tests**

Run:

```bash
gofmt -w internal/service/systemd_unit.go internal/service/systemd_unit_test.go
go test ./internal/service -run '^(TestRenderSystemdUserUnit|TestQuoteSystemdExecArg)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the renderer**

```bash
git add internal/service/systemd_unit.go internal/service/systemd_unit_test.go
git commit -m "feat: render systemd user unit"
```

### Task 2: Install and Update the User Unit

**Files:**
- Create: `internal/service/systemd_user_service.go`
- Create: `internal/service/systemd_user_service_test.go`

- [ ] **Step 1: Write failing manager preflight and install tests**

Create `internal/service/systemd_user_service_test.go`. Use real temporary files but a recording runner so tests never contact systemd:

```go
package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type recordedSystemdCommand struct {
	name string
	args []string
}

type recordingSystemdRunner struct {
	calls  []recordedSystemdCommand
	failAt string
}

func (r *recordingSystemdRunner) Run(_ context.Context, name string, args ...string) (CommandResult, error) {
	r.calls = append(r.calls, recordedSystemdCommand{name: name, args: append([]string(nil), args...)})
	key := strings.Join(args, " ")
	if key == r.failAt {
		return CommandResult{Diagnostic: []byte("fake diagnostic"), ExitCode: 1}, errors.New("fake systemctl failure")
	}
	return CommandResult{ExitCode: 0}, nil
}

func newTestSystemdManager(t *testing.T, runner *recordingSystemdRunner) (*SystemdUserManager, string, string) {
	t.Helper()
	configRoot := t.TempDir()
	executable := filepath.Join(t.TempDir(), "IGo Notes")
	if err := os.WriteFile(executable, []byte("binary"), 0755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	manager := NewSystemdUserManager(
		"linux",
		runner,
		func(string) (string, error) { return "/usr/bin/systemctl", nil },
		func() (string, error) { return configRoot, nil },
		func() (string, error) { return executable, nil },
	)
	return manager, configRoot, executable
}

func TestSystemdUserManagerInstallCreatesAndStartsUnit(t *testing.T) {
	runner := &recordingSystemdRunner{}
	manager, configRoot, executable := newTestSystemdManager(t, runner)

	result, err := manager.Install(context.Background(), SystemdInstallOptions{
		Port:      "9000",
		ConfigDir: "/tmp/notes config",
		Base:      "work-notes",
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	wantPath := filepath.Join(configRoot, "systemd", "user", SystemdUserUnitName)
	if result.UnitPath != wantPath || result.URL != "http://127.0.0.1:9000" {
		t.Fatalf("Install() result = %#v, want path %q and port 9000 URL", result, wantPath)
	}
	content, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	if !strings.Contains(string(content), executable) || !strings.Contains(string(content), `"--no-browser"`) {
		t.Fatalf("unit does not contain executable and --no-browser:\n%s", content)
	}
	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("stat unit: %v", err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("unit mode = %o, want 0644", info.Mode().Perm())
	}
	temporaryFiles, err := filepath.Glob(filepath.Join(filepath.Dir(wantPath), ".igonotes-service-*.tmp"))
	if err != nil {
		t.Fatalf("glob temporary units: %v", err)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("temporary units remain after install: %v", temporaryFiles)
	}

	wantCalls := []recordedSystemdCommand{
		{name: "/usr/bin/systemctl", args: []string{"--user", "show-environment"}},
		{name: "/usr/bin/systemctl", args: []string{"--user", "daemon-reload"}},
		{name: "/usr/bin/systemctl", args: []string{"--user", "enable", SystemdUserUnitName}},
		{name: "/usr/bin/systemctl", args: []string{"--user", "restart", SystemdUserUnitName}},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("systemctl calls = %#v, want %#v", runner.calls, wantCalls)
	}
}

func TestSystemdUserManagerInstallReplacesOwnedUnit(t *testing.T) {
	runner := &recordingSystemdRunner{}
	manager, configRoot, _ := newTestSystemdManager(t, runner)
	if _, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"}); err != nil {
		t.Fatalf("first Install() error = %v", err)
	}
	if _, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "9000", Base: "work"}); err != nil {
		t.Fatalf("second Install() error = %v", err)
	}
	unitPath := filepath.Join(configRoot, "systemd", "user", SystemdUserUnitName)
	content, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read replaced unit: %v", err)
	}
	if !strings.Contains(string(content), `"9000"`) || !strings.Contains(string(content), `"work"`) {
		t.Fatalf("unit was not replaced with new options:\n%s", content)
	}
	if len(runner.calls) != 8 {
		t.Fatalf("systemctl call count = %d, want two complete installations", len(runner.calls))
	}
}

func TestSystemdUserManagerInstallRejectsUnsupportedPlatformBeforeChanges(t *testing.T) {
	runner := &recordingSystemdRunner{}
	manager, configRoot, _ := newTestSystemdManager(t, runner)
	manager.goos = "windows"

	_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
	if err == nil || !strings.Contains(err.Error(), "Linux") {
		t.Fatalf("Install() error = %v, want Linux support error", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("systemctl calls = %#v, want none", runner.calls)
	}
	if _, statErr := os.Stat(filepath.Join(configRoot, "systemd")); !os.IsNotExist(statErr) {
		t.Fatalf("systemd directory stat error = %v, want not exist", statErr)
	}
}

func TestSystemdUserManagerInstallRejectsForeignUnit(t *testing.T) {
	runner := &recordingSystemdRunner{}
	manager, configRoot, _ := newTestSystemdManager(t, runner)
	unitDir := filepath.Join(configRoot, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		t.Fatalf("mkdir unit dir: %v", err)
	}
	unitPath := filepath.Join(unitDir, SystemdUserUnitName)
	want := []byte("[Service]\nExecStart=/custom/server\n# Managed by IGoNotes\n")
	if err := os.WriteFile(unitPath, want, 0644); err != nil {
		t.Fatalf("write foreign unit: %v", err)
	}

	_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
	if err == nil || !strings.Contains(err.Error(), "not managed by IGoNotes") {
		t.Fatalf("Install() error = %v, want marker collision error", err)
	}
	got, readErr := os.ReadFile(unitPath)
	if readErr != nil || string(got) != string(want) {
		t.Fatalf("foreign unit changed: content %q, error %v", got, readErr)
	}
}
```

Add these table-driven failure tests in the same file:

```go
func TestSystemdUserManagerInstallRejectsInvalidPorts(t *testing.T) {
	for _, port := range []string{"", "0", "-1", "abc", "65536"} {
		t.Run(port, func(t *testing.T) {
			runner := &recordingSystemdRunner{}
			manager, configRoot, _ := newTestSystemdManager(t, runner)
			_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: port})
			if err == nil || !strings.Contains(err.Error(), "invalid service port") {
				t.Fatalf("Install() error = %v, want invalid port error", err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("systemctl calls = %#v, want none", runner.calls)
			}
			if _, statErr := os.Stat(filepath.Join(configRoot, "systemd")); !os.IsNotExist(statErr) {
				t.Fatalf("systemd directory stat error = %v, want not exist", statErr)
			}
		})
	}
}

func TestSystemdUserManagerInstallRequiresAvailableUserSystemd(t *testing.T) {
	t.Run("missing executable", func(t *testing.T) {
		runner := &recordingSystemdRunner{}
		manager, configRoot, _ := newTestSystemdManager(t, runner)
		manager.lookPath = func(string) (string, error) { return "", errors.New("not found") }
		_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
		if err == nil || !strings.Contains(err.Error(), "find systemctl") {
			t.Fatalf("Install() error = %v, want lookup error", err)
		}
		if _, statErr := os.Stat(filepath.Join(configRoot, "systemd")); !os.IsNotExist(statErr) {
			t.Fatalf("systemd directory stat error = %v, want not exist", statErr)
		}
	})

	t.Run("user manager unavailable", func(t *testing.T) {
		runner := &recordingSystemdRunner{failAt: "--user show-environment"}
		manager, configRoot, _ := newTestSystemdManager(t, runner)
		_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
		if err == nil || !strings.Contains(err.Error(), "connect to user systemd") {
			t.Fatalf("Install() error = %v, want user manager error", err)
		}
		if _, statErr := os.Stat(filepath.Join(configRoot, "systemd")); !os.IsNotExist(statErr) {
			t.Fatalf("systemd directory stat error = %v, want not exist", statErr)
		}
	})
}

func TestSystemdUserManagerInstallRequiresExecutableFile(t *testing.T) {
	runner := &recordingSystemdRunner{}
	manager, configRoot, _ := newTestSystemdManager(t, runner)
	manager.executable = func() (string, error) { return filepath.Join(t.TempDir(), "missing"), nil }
	_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
	if err == nil || !strings.Contains(err.Error(), "stat current executable") {
		t.Fatalf("Install() error = %v, want executable error", err)
	}
	if _, statErr := os.Stat(filepath.Join(configRoot, "systemd")); !os.IsNotExist(statErr) {
		t.Fatalf("systemd directory stat error = %v, want not exist", statErr)
	}
}

func TestSystemdUserManagerInstallStopsAfterActivationFailure(t *testing.T) {
	tests := []struct {
		name      string
		failAt    string
		callCount int
	}{
		{name: "reload", failAt: "--user daemon-reload", callCount: 2},
		{name: "enable", failAt: "--user enable igonotes.service", callCount: 3},
		{name: "restart", failAt: "--user restart igonotes.service", callCount: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingSystemdRunner{failAt: test.failAt}
			manager, configRoot, _ := newTestSystemdManager(t, runner)
			_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
			if err == nil || !strings.Contains(err.Error(), "systemctl --user status") {
				t.Fatalf("Install() error = %v, want diagnostic error", err)
			}
			if len(runner.calls) != test.callCount {
				t.Fatalf("systemctl call count = %d, want %d", len(runner.calls), test.callCount)
			}
			unitPath := filepath.Join(configRoot, "systemd", "user", SystemdUserUnitName)
			if _, statErr := os.Stat(unitPath); statErr != nil {
				t.Fatalf("new unit was not retained: %v", statErr)
			}
		})
	}
}
```

- [ ] **Step 2: Run install manager tests and verify they fail**

Run:

```bash
go test ./internal/service -run '^TestSystemdUserManagerInstall' -count=1
```

Expected: build failure because `SystemdUserManager`, `NewSystemdUserManager`, and `SystemdInstallResult` do not exist.

- [ ] **Step 3: Implement manager preflight, atomic writing, and install ordering**

Create `internal/service/systemd_user_service.go` with these public types and methods:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type SystemdInstallResult struct {
	UnitPath string
	URL      string
}

type SystemdUserManager struct {
	goos          string
	runner        CommandRunner
	lookPath      func(string) (string, error)
	userConfigDir func() (string, error)
	executable    func() (string, error)
}

func NewSystemdUserManager(
	goos string,
	runner CommandRunner,
	lookPath func(string) (string, error),
	userConfigDir func() (string, error),
	executable func() (string, error),
) *SystemdUserManager {
	return &SystemdUserManager{
		goos:          goos,
		runner:        runner,
		lookPath:      lookPath,
		userConfigDir: userConfigDir,
		executable:    executable,
	}
}

func (m *SystemdUserManager) Install(ctx context.Context, options SystemdInstallOptions) (SystemdInstallResult, error) {
	if err := m.requireLinux(); err != nil {
		return SystemdInstallResult{}, err
	}
	if err := validateSystemdPort(options.Port); err != nil {
		return SystemdInstallResult{}, err
	}
	unitPath, err := m.unitPath()
	if err != nil {
		return SystemdInstallResult{}, err
	}
	systemctl, err := m.availableSystemctl(ctx)
	if err != nil {
		return SystemdInstallResult{}, err
	}
	if err := requireManagedOrAbsent(unitPath); err != nil {
		return SystemdInstallResult{}, err
	}
	executable, err := m.executablePath()
	if err != nil {
		return SystemdInstallResult{}, err
	}
	unit, err := renderSystemdUserUnit(executable, options)
	if err != nil {
		return SystemdInstallResult{}, fmt.Errorf("render systemd user unit: %w", err)
	}
	if err := writeFileAtomically(unitPath, unit, 0644); err != nil {
		return SystemdInstallResult{}, fmt.Errorf("install systemd user unit: %w", err)
	}

	commands := [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", SystemdUserUnitName},
		{"--user", "restart", SystemdUserUnitName},
	}
	for _, arguments := range commands {
		if err := m.runSystemctl(ctx, systemctl, arguments...); err != nil {
			return SystemdInstallResult{}, fmt.Errorf(
				"activate systemd user unit: %w\nDiagnose with: systemctl --user status %s\nLogs: journalctl --user-unit %s",
				err,
				SystemdUserUnitName,
				SystemdUserUnitName,
			)
		}
	}

	return SystemdInstallResult{
		UnitPath: unitPath,
		URL:      "http://" + net.JoinHostPort("127.0.0.1", options.Port),
	}, nil
}

func (m *SystemdUserManager) requireLinux() error {
	if m.goos != "linux" {
		return fmt.Errorf("systemd user services are supported only on Linux, current OS is %s", m.goos)
	}
	if m.runner == nil || m.lookPath == nil || m.userConfigDir == nil || m.executable == nil {
		return errors.New("systemd user service dependencies are not configured")
	}
	return nil
}

func validateSystemdPort(port string) error {
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return fmt.Errorf("invalid service port %q: expected 1..65535", port)
	}
	return nil
}

func (m *SystemdUserManager) unitPath() (string, error) {
	configDir, err := m.userConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	if configDir == "" {
		return "", errors.New("resolve user config directory: empty path")
	}
	return filepath.Join(configDir, "systemd", "user", SystemdUserUnitName), nil
}

func (m *SystemdUserManager) availableSystemctl(ctx context.Context) (string, error) {
	systemctl, err := m.lookPath("systemctl")
	if err != nil {
		return "", fmt.Errorf("find systemctl: %w", err)
	}
	systemctl, err = filepath.Abs(systemctl)
	if err != nil {
		return "", fmt.Errorf("resolve systemctl path: %w", err)
	}
	if err := m.runSystemctl(ctx, systemctl, "--user", "show-environment"); err != nil {
		return "", fmt.Errorf("connect to user systemd: %w", err)
	}
	return systemctl, nil
}

func (m *SystemdUserManager) executablePath() (string, error) {
	executable, err := m.executable()
	if err != nil {
		return "", fmt.Errorf("resolve current executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("resolve absolute executable path: %w", err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		return "", fmt.Errorf("stat current executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("current executable %q is not an executable regular file", executable)
	}
	return executable, nil
}

func requireManagedOrAbsent(path string) error {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read existing unit %q: %w", path, err)
	}
	if !strings.HasPrefix(string(content), systemdUnitMarker+"\n") {
		return fmt.Errorf("existing unit %q is not managed by IGoNotes", path)
	}
	return nil
}

func writeFileAtomically(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return fmt.Errorf("create unit directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".igonotes-service-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary unit: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set temporary unit permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write temporary unit: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary unit: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary unit: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace unit: %w", err)
	}
	return nil
}

func (m *SystemdUserManager) runSystemctl(ctx context.Context, systemctl string, arguments ...string) error {
	result, err := m.runner.Run(ctx, systemctl, arguments...)
	if err == nil {
		return nil
	}
	diagnostic := strings.TrimSpace(string(result.Diagnostic))
	if diagnostic == "" {
		return fmt.Errorf("systemctl %s: %w", strings.Join(arguments, " "), err)
	}
	return fmt.Errorf("systemctl %s: %w: %s", strings.Join(arguments, " "), err, diagnostic)
}
```

During implementation, keep the executable validation after all non-mutating preflight checks and before `MkdirAll`. Do not move relative `--config` normalization here; that belongs to the CLI task because the manager receives final unit arguments. Treat marker checks as safeguards against pre-existing/static collisions. Creation at an absent path must use no-replace, and install/update activation must remain inside the stable advisory lock shared by cooperating IGoNotes invocations; arbitrary concurrent same-user mutation remains unsupported.

- [ ] **Step 4: Run focused install tests and the existing directory-picker runner tests**

Run:

```bash
gofmt -w internal/service/systemd_user_service.go internal/service/systemd_user_service_test.go
go test ./internal/service -run '^(TestSystemdUserManagerInstall|TestExecCommandRunner|TestDirectoryPicker)' -count=1
```

Expected: PASS, proving reuse of `CommandRunner` did not alter directory selection.

- [ ] **Step 5: Commit user-unit installation**

```bash
git add internal/service/systemd_user_service.go internal/service/systemd_user_service_test.go
git commit -m "feat: install systemd user service"
```

### Task 3: Uninstall the Marker-Managed User Unit Cooperatively

**Files:**
- Modify: `internal/service/systemd_user_service.go`
- Modify: `internal/service/systemd_user_service_test.go`

- [ ] **Step 1: Write failing uninstall marker and ordering tests**

Append these tests to `internal/service/systemd_user_service_test.go`:

```go
func TestSystemdUserManagerUninstallDisablesThenRemovesUnit(t *testing.T) {
	runner := &recordingSystemdRunner{}
	manager, configRoot, _ := newTestSystemdManager(t, runner)
	unitPath := filepath.Join(configRoot, "systemd", "user", SystemdUserUnitName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		t.Fatalf("mkdir unit dir: %v", err)
	}
	if err := os.WriteFile(unitPath, []byte(systemdUnitMarker+"\n[Service]\n"), 0644); err != nil {
		t.Fatalf("write owned unit: %v", err)
	}

	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
		t.Fatalf("unit stat error = %v, want not exist", err)
	}
	wantCalls := []recordedSystemdCommand{
		{name: "/usr/bin/systemctl", args: []string{"--user", "show-environment"}},
		{name: "/usr/bin/systemctl", args: []string{"--user", "disable", "--now", SystemdUserUnitName}},
		{name: "/usr/bin/systemctl", args: []string{"--user", "daemon-reload"}},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("systemctl calls = %#v, want %#v", runner.calls, wantCalls)
	}
}

func TestSystemdUserManagerUninstallMissingUnitIsNoOp(t *testing.T) {
	runner := &recordingSystemdRunner{}
	manager, _, _ := newTestSystemdManager(t, runner)
	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("systemctl calls = %#v, want none", runner.calls)
	}
}

func TestSystemdUserManagerUninstallDoesNotTouchForeignUnit(t *testing.T) {
	runner := &recordingSystemdRunner{}
	manager, configRoot, _ := newTestSystemdManager(t, runner)
	unitPath := filepath.Join(configRoot, "systemd", "user", SystemdUserUnitName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		t.Fatalf("mkdir unit dir: %v", err)
	}
	want := []byte("[Service]\nExecStart=/custom/server\n")
	if err := os.WriteFile(unitPath, want, 0644); err != nil {
		t.Fatalf("write foreign unit: %v", err)
	}

	err := manager.Uninstall(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not managed by IGoNotes") {
		t.Fatalf("Uninstall() error = %v, want marker collision error", err)
	}
	got, readErr := os.ReadFile(unitPath)
	if readErr != nil || string(got) != string(want) {
		t.Fatalf("foreign unit changed: content %q, error %v", got, readErr)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("systemctl calls = %#v, want none", runner.calls)
	}
}
```

Append the two failure-order tests:

```go
func TestSystemdUserManagerUninstallKeepsUnitWhenDisableFails(t *testing.T) {
	runner := &recordingSystemdRunner{failAt: "--user disable --now igonotes.service"}
	manager, configRoot, _ := newTestSystemdManager(t, runner)
	unitPath := filepath.Join(configRoot, "systemd", "user", SystemdUserUnitName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		t.Fatalf("mkdir unit dir: %v", err)
	}
	if err := os.WriteFile(unitPath, []byte(systemdUnitMarker+"\n"), 0644); err != nil {
		t.Fatalf("write owned unit: %v", err)
	}

	err := manager.Uninstall(context.Background())
	if err == nil || !strings.Contains(err.Error(), "disable systemd user unit") {
		t.Fatalf("Uninstall() error = %v, want disable error", err)
	}
	if _, statErr := os.Stat(unitPath); statErr != nil {
		t.Fatalf("owned unit was removed after disable failure: %v", statErr)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("systemctl call count = %d, want availability and disable", len(runner.calls))
	}
}

func TestSystemdUserManagerUninstallReportsReloadFailureAfterRemoval(t *testing.T) {
	runner := &recordingSystemdRunner{failAt: "--user daemon-reload"}
	manager, configRoot, _ := newTestSystemdManager(t, runner)
	unitPath := filepath.Join(configRoot, "systemd", "user", SystemdUserUnitName)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		t.Fatalf("mkdir unit dir: %v", err)
	}
	if err := os.WriteFile(unitPath, []byte(systemdUnitMarker+"\n"), 0644); err != nil {
		t.Fatalf("write owned unit: %v", err)
	}

	err := manager.Uninstall(context.Background())
	if err == nil || !strings.Contains(err.Error(), "reload systemd after unit removal") {
		t.Fatalf("Uninstall() error = %v, want reload error", err)
	}
	if _, statErr := os.Stat(unitPath); !os.IsNotExist(statErr) {
		t.Fatalf("unit stat error = %v, want removed before reload", statErr)
	}
}
```

- [ ] **Step 2: Run uninstall tests and verify they fail**

Run:

```bash
go test ./internal/service -run '^TestSystemdUserManagerUninstall' -count=1
```

Expected: build failure because `Uninstall` does not exist.

- [ ] **Step 3: Implement idempotent, marker-guarded uninstall**

Add this method to `internal/service/systemd_user_service.go`:

```go
func (m *SystemdUserManager) Uninstall(ctx context.Context) error {
	if err := m.requireLinux(); err != nil {
		return err
	}
	unitPath, err := m.unitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat systemd user unit: %w", err)
	}
	if err := requireManagedOrAbsent(unitPath); err != nil {
		return err
	}
	systemctl, err := m.availableSystemctl(ctx)
	if err != nil {
		return err
	}
	if err := m.runSystemctl(ctx, systemctl, "--user", "disable", "--now", SystemdUserUnitName); err != nil {
		return fmt.Errorf("disable systemd user unit: %w", err)
	}
	if err := os.Remove(unitPath); err != nil {
		return fmt.Errorf("remove systemd user unit: %w", err)
	}
	if err := m.runSystemctl(ctx, systemctl, "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd after unit removal: %w", err)
	}
	return nil
}
```

The missing-file branch must occur before `LookPath` or `systemctl`; this prevents an idempotent uninstall from disabling a static vendor unit with the same name. For a present entry, hold the same stable advisory lock used by install through disable, removal, and reload, and perform best-effort marker revalidation before removal. This serializes cooperating IGoNotes processes only; it does not exclude a non-cooperating same-user writer and must not be described as an authorization or security guarantee.

- [ ] **Step 4: Run all systemd manager tests**

Run:

```bash
gofmt -w internal/service/systemd_user_service.go internal/service/systemd_user_service_test.go
go test ./internal/service -run '^(TestSystemdUserManager|TestRenderSystemdUserUnit|TestQuoteSystemdExecArg)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit marker-guarded uninstallation**

```bash
git add internal/service/systemd_user_service.go internal/service/systemd_user_service_test.go
git commit -m "feat: uninstall systemd user service"
```

### Task 4: Dispatch Service CLI Commands

**Files:**
- Create: `cmd/api/service_command.go`
- Create: `cmd/api/service_command_test.go`

- [ ] **Step 1: Write failing dispatch, parsing, and output tests**

Create `cmd/api/service_command_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"IGoNotes/internal/service"
)

type fakeUserServiceManager struct {
	installOptions service.SystemdInstallOptions
	installResult  service.SystemdInstallResult
	installErr     error
	uninstallCalls int
	uninstallErr   error
}

func (m *fakeUserServiceManager) Install(_ context.Context, options service.SystemdInstallOptions) (service.SystemdInstallResult, error) {
	m.installOptions = options
	return m.installResult, m.installErr
}

func (m *fakeUserServiceManager) Uninstall(context.Context) error {
	m.uninstallCalls++
	return m.uninstallErr
}

func TestDispatchCommandLeavesOrdinaryArgumentsUnchanged(t *testing.T) {
	want := []string{"--port", "9000", "--no-browser"}
	var got []string
	err := dispatchCommand(
		context.Background(),
		want,
		&bytes.Buffer{},
		&fakeUserServiceManager{},
		func(_ context.Context, args []string) error {
			got = append([]string(nil), args...)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("dispatchCommand() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("server args = %#v, want %#v", got, want)
	}
}

func TestDispatchCommandInstallsServiceWithNormalizedOptions(t *testing.T) {
	manager := &fakeUserServiceManager{installResult: service.SystemdInstallResult{
		UnitPath: "/home/user/.config/systemd/user/igonotes.service",
		URL:      "http://127.0.0.1:9000",
	}}
	workingDirectory := t.TempDir()
	output := &bytes.Buffer{}
	err := runServiceCommand(
		context.Background(),
		[]string{"install", "--port", "9000", "--config", "relative config", "--base", "work"},
		output,
		manager,
		func(path string) (string, error) { return filepath.Join(workingDirectory, path), nil },
	)
	if err != nil {
		t.Fatalf("runServiceCommand() error = %v", err)
	}
	want := service.SystemdInstallOptions{
		Port:      "9000",
		ConfigDir: filepath.Join(workingDirectory, "relative config"),
		Base:      "work",
	}
	if manager.installOptions != want {
		t.Fatalf("install options = %#v, want %#v", manager.installOptions, want)
	}
	for _, value := range []string{manager.installResult.URL, manager.installResult.UnitPath, "systemctl --user status", "journalctl --user-unit"} {
		if !strings.Contains(output.String(), value) {
			t.Errorf("output %q does not contain %q", output, value)
		}
	}
}

func TestRunServiceCommandUsesInstallDefaults(t *testing.T) {
	manager := &fakeUserServiceManager{}
	err := runServiceCommand(context.Background(), []string{"install"}, &bytes.Buffer{}, manager, filepath.Abs)
	if err != nil {
		t.Fatalf("runServiceCommand() error = %v", err)
	}
	want := service.SystemdInstallOptions{Port: "8080"}
	if manager.installOptions != want {
		t.Fatalf("install options = %#v, want %#v", manager.installOptions, want)
	}
}

func TestRunServiceCommandUninstallsWithoutOptions(t *testing.T) {
	manager := &fakeUserServiceManager{}
	output := &bytes.Buffer{}
	if err := runServiceCommand(context.Background(), []string{"uninstall"}, output, manager, filepath.Abs); err != nil {
		t.Fatalf("runServiceCommand() error = %v", err)
	}
	if manager.uninstallCalls != 1 || !strings.Contains(output.String(), "removed") {
		t.Fatalf("uninstall calls = %d, output = %q", manager.uninstallCalls, output)
	}
}

func TestRunServiceCommandRejectsInvalidSyntax(t *testing.T) {
	tests := [][]string{
		nil,
		{"unknown"},
		{"install", "--unknown"},
		{"install", "extra"},
		{"uninstall", "--port", "9000"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			err := runServiceCommand(context.Background(), args, &bytes.Buffer{}, &fakeUserServiceManager{}, filepath.Abs)
			if err == nil {
				t.Fatalf("runServiceCommand(%q) error = nil", args)
			}
		})
	}
}

func TestRunServiceCommandPropagatesManagerErrors(t *testing.T) {
	wantErr := errors.New("systemd unavailable")
	manager := &fakeUserServiceManager{installErr: wantErr}
	err := runServiceCommand(context.Background(), []string{"install"}, &bytes.Buffer{}, manager, filepath.Abs)
	if !errors.Is(err, wantErr) {
		t.Fatalf("runServiceCommand() error = %v, want %v", err, wantErr)
	}
}
```

- [ ] **Step 2: Run service command tests and verify they fail**

Run:

```bash
go test ./cmd/api -run '^(TestDispatchCommand|TestRunServiceCommand)' -count=1
```

Expected: build failure because the dispatcher and command parser do not exist.

- [ ] **Step 3: Implement top-level dispatch and dedicated install flags**

Create `cmd/api/service_command.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"IGoNotes/internal/service"
)

type userServiceManager interface {
	Install(context.Context, service.SystemdInstallOptions) (service.SystemdInstallResult, error)
	Uninstall(context.Context) error
}

type serverRunner func(context.Context, []string) error

func dispatchCommand(
	ctx context.Context,
	args []string,
	output io.Writer,
	manager userServiceManager,
	runServer serverRunner,
) error {
	if len(args) == 0 || args[0] != "service" {
		return runServer(ctx, args)
	}
	return runServiceCommand(ctx, args[1:], output, manager, filepath.Abs)
}

func runServiceCommand(
	ctx context.Context,
	args []string,
	output io.Writer,
	manager userServiceManager,
	absolutePath func(string) (string, error),
) error {
	if len(args) == 0 {
		return fmt.Errorf("service command requires install or uninstall")
	}

	switch args[0] {
	case "install":
		flags := flag.NewFlagSet("service install", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		port := flags.String("port", "8080", "service port")
		configDir := flags.String("config", "", "application config directory")
		base := flags.String("base", "", "base name")
		if err := flags.Parse(args[1:]); err != nil {
			return fmt.Errorf("parse service install options: %w", err)
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("service install does not accept operands: %v", flags.Args())
		}
		if *configDir != "" {
			resolved, err := absolutePath(*configDir)
			if err != nil {
				return fmt.Errorf("resolve service config path: %w", err)
			}
			*configDir = resolved
		}
		result, err := manager.Install(ctx, service.SystemdInstallOptions{
			Port:      *port,
			ConfigDir: *configDir,
			Base:      *base,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "IGoNotes service installed and started.\nURL: %s\nUnit: %s\n", result.URL, result.UnitPath)
		fmt.Fprintf(output, "Status: systemctl --user status %s\nLogs: journalctl --user-unit %s\n", service.SystemdUserUnitName, service.SystemdUserUnitName)
		return nil

	case "uninstall":
		if len(args) != 1 {
			return fmt.Errorf("service uninstall does not accept options or operands")
		}
		if err := manager.Uninstall(ctx); err != nil {
			return err
		}
		fmt.Fprintln(output, "IGoNotes user service removed; notes and configuration were not changed.")
		return nil

	default:
		return fmt.Errorf("unknown service command %q: expected install or uninstall", args[0])
	}
}
```

Use `filepath.Abs` directly in production. In tests, pass `func(path string) (string, error) { return filepath.Join(workingDirectory, path), nil }` rather than `Getwd`; this exactly models the production normalization seam and avoids changing process working directory.

- [ ] **Step 4: Run CLI tests**

Run:

```bash
gofmt -w cmd/api/service_command.go cmd/api/service_command_test.go
go test ./cmd/api -run '^(TestDispatchCommand|TestRunServiceCommand)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit service command dispatch**

```bash
git add cmd/api/service_command.go cmd/api/service_command_test.go
git commit -m "feat: add systemd service commands"
```

### Task 5: Add a Graceful HTTP Server Lifecycle

**Files:**
- Create: `cmd/api/server.go`
- Modify: `cmd/api/main.go:19-55`
- Modify: `cmd/api/server_address_test.go`
- Modify: `cmd/api/system_routes_test.go:192-213`

- [ ] **Step 1: Replace old serve-callback tests with failing lifecycle tests**

Keep `TestLocalServerEndpointUsesLoopback`. Add `context`, `sync`, and `time` to the test imports, replace the old injected serve callback with this fake, and add graceful and forced shutdown tests:

```go
type fakeHTTPServer struct {
	serveStarted chan struct{}
	serveDone    chan struct{}
	shutdownCall chan context.Context
	listener     net.Listener
	onServe      func()
	closeCalls   int
	serveErr     error
	shutdownErr  error
	once         sync.Once
}

func newFakeHTTPServer() *fakeHTTPServer {
	return &fakeHTTPServer{
		serveStarted: make(chan struct{}),
		serveDone:    make(chan struct{}),
		shutdownCall: make(chan context.Context, 1),
		serveErr:     http.ErrServerClosed,
	}
}

func (s *fakeHTTPServer) Serve(listener net.Listener) error {
	s.listener = listener
	if s.onServe != nil {
		s.onServe()
	}
	close(s.serveStarted)
	<-s.serveDone
	return s.serveErr
}

func (s *fakeHTTPServer) Shutdown(ctx context.Context) error {
	s.shutdownCall <- ctx
	if s.shutdownErr == nil {
		s.once.Do(func() { close(s.serveDone) })
	}
	return s.shutdownErr
}

func (s *fakeHTTPServer) Close() error {
	s.closeCalls++
	s.once.Do(func() { close(s.serveDone) })
	return nil
}

func TestServeLocalShutsDownCleanlyAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := newFakeHTTPServer()
	ready := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- serveLocal(ctx, "127.0.0.1:0", server, func() { close(ready) }, 10*time.Second)
	}()

	<-ready
	<-server.serveStarted
	cancel()
	shutdownContext := <-server.shutdownCall
	if _, ok := shutdownContext.Deadline(); !ok {
		t.Fatal("Shutdown() context has no deadline")
	}
	if err := <-result; err != nil {
		t.Fatalf("serveLocal() error = %v", err)
	}
	if server.closeCalls != 0 {
		t.Fatalf("Close() calls = %d, want 0", server.closeCalls)
	}
}

func TestServeLocalForcesCloseAfterShutdownFailure(t *testing.T) {
	wantErr := context.DeadlineExceeded
	ctx, cancel := context.WithCancel(context.Background())
	server := newFakeHTTPServer()
	server.shutdownErr = wantErr
	result := make(chan error, 1)
	go func() {
		result <- serveLocal(ctx, "127.0.0.1:0", server, func() {}, time.Millisecond)
	}()
	<-server.serveStarted
	cancel()
	err := <-result
	if !errors.Is(err, wantErr) {
		t.Fatalf("serveLocal() error = %v, want %v", err, wantErr)
	}
	if server.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", server.closeCalls)
	}
}

func TestServeLocalPropagatesUnexpectedServeError(t *testing.T) {
	wantErr := errors.New("serve failed")
	server := newFakeHTTPServer()
	server.serveErr = wantErr
	readyCalled := false
	server.onServe = func() {
		if !readyCalled {
			t.Error("serveLocal() called Serve before ready callback")
		}
	}
	server.once.Do(func() { close(server.serveDone) })
	err := serveLocal(context.Background(), "127.0.0.1:0", server, func() { readyCalled = true }, time.Second)
	if !errors.Is(err, wantErr) {
		t.Fatalf("serveLocal() error = %v, want %v", err, wantErr)
	}
	if _, err := server.listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("listener.Accept() error = %v, want %v", err, net.ErrClosed)
	}
}
```

Replace the occupied-address test with the new signature:

```go
func TestServeLocalDoesNotSignalReadyWhenAddressIsOccupied(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer occupied.Close()

	readyCalled := false
	server := newFakeHTTPServer()
	err = serveLocal(
		context.Background(),
		occupied.Addr().String(),
		server,
		func() { readyCalled = true },
		time.Second,
	)
	if err == nil {
		t.Fatal("serveLocal() error = nil, want bind error")
	}
	if readyCalled {
		t.Error("serveLocal() called ready callback after bind failure")
	}
	select {
	case <-server.serveStarted:
		t.Error("serveLocal() called Serve after bind failure")
	default:
	}
}
```

- [ ] **Step 2: Run lifecycle tests and verify they fail**

Run:

```bash
go test ./cmd/api -run '^(TestLocalServerEndpoint|TestServeLocal)' -count=1
```

Expected: build failure because `serveLocal` still has the old signature.

- [ ] **Step 3: Move server helpers and implement graceful shutdown**

Create `cmd/api/server.go` and move `localServerEndpoint` and `openBrowser` from `main.go`. Implement the lifecycle helper as follows:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

type httpServerLifecycle interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
	Close() error
}

func newHTTPServer(handler http.Handler) httpServerLifecycle {
	return &http.Server{Handler: handler}
}

func localServerEndpoint(port string) (string, string) {
	address := net.JoinHostPort("127.0.0.1", port)
	return address, "http://" + address
}

func serveLocal(
	ctx context.Context,
	address string,
	server httpServerLifecycle,
	ready func(),
	shutdownTimeout time.Duration,
) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()

	serveErrors := make(chan error, 1)
	ready()
	go func() {
		serveErrors <- server.Serve(listener)
	}()

	select {
	case err := <-serveErrors:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err

	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			closeErr := server.Close()
			serveErr := <-serveErrors
			if errors.Is(serveErr, http.ErrServerClosed) {
				serveErr = nil
			}
			return errors.Join(
				fmt.Errorf("graceful HTTP shutdown: %w", shutdownErr),
				closeErr,
				serveErr,
			)
		}
		serveErr := <-serveErrors
		if serveErr == nil || errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return serveErr
	}
}

func openBrowser(url string) error {
	var command string
	var arguments []string
	switch runtime.GOOS {
	case "windows":
		command = "cmd"
		arguments = []string{"/c", "start"}
	case "darwin":
		command = "open"
	default:
		command = "xdg-open"
	}
	arguments = append(arguments, url)
	return exec.Command(command, arguments...).Start()
}
```

Remove the moved helper functions and now-unused `net`, `net/http`, and `os/exec` imports from `main.go`. Add `context` and `time` temporarily and adapt the existing blocking call so this intermediate commit compiles and passes all `cmd/api` tests:

```go
if err := serveLocal(context.Background(), address, newHTTPServer(router), func() {
	log.Printf("Сервер запущен на %s", url)
	if !*noBrowser {
		log.Printf("Открываем браузер: %s", url)
		if err := openBrowser(url); err != nil {
			log.Printf("Не удалось открыть браузер автоматически: %v", err)
		}
	}
}, 10*time.Second); err != nil {
	log.Fatal(err)
}
```

In `TestMainWiresDirectoryPickerRouteBeforeServing`, change only the final expected snippet to `if err := serveLocal(context.Background(), address, newHTTPServer(router)` for this intermediate shape. Task 6 replaces it with the final returning `runServer` shape. Do not attach the lifecycle context as `http.Server.BaseContext`; active requests need the full grace period.

- [ ] **Step 4: Run focused server tests and race detection**

Run:

```bash
gofmt -w cmd/api/server.go cmd/api/main.go cmd/api/server_address_test.go cmd/api/system_routes_test.go
go test ./cmd/api -count=1
go test -race ./cmd/api -run '^TestServeLocal' -count=1
```

Expected: PASS with no race report.

- [ ] **Step 5: Commit the server lifecycle**

```bash
git add cmd/api/server.go cmd/api/main.go cmd/api/server_address_test.go cmd/api/system_routes_test.go
git commit -m "feat: gracefully stop local server"
```

### Task 6: Integrate Signals, Resource Cleanup, and Service Dispatch

**Files:**
- Modify: `cmd/api/main.go`
- Modify: `cmd/api/system_routes_test.go:192-213`
- Create: `cmd/api/signals_unix.go`
- Create: `cmd/api/signals_windows.go`

- [ ] **Step 1: Write failing ordinary CLI and production-wiring regression tests**

Add tests near the existing CLI tests for ordinary server parsing:

```go
func TestParseServerOptionsPreservesExistingDefaults(t *testing.T) {
	options, err := parseServerOptions(nil)
	if err != nil {
		t.Fatalf("parseServerOptions() error = %v", err)
	}
	want := serverOptions{port: "8080"}
	if options != want {
		t.Fatalf("options = %#v, want %#v", options, want)
	}
}

func TestParseServerOptionsPreservesExistingFlags(t *testing.T) {
	options, err := parseServerOptions([]string{
		"--config", "/tmp/config",
		"--port", "9000",
		"--base", "work",
		"--no-browser",
	})
	if err != nil {
		t.Fatalf("parseServerOptions() error = %v", err)
	}
	want := serverOptions{configPath: "/tmp/config", port: "9000", base: "work", noBrowser: true}
	if options != want {
		t.Fatalf("options = %#v, want %#v", options, want)
	}
}
```

Replace `TestMainWiresDirectoryPickerRouteBeforeServing` with `TestRunServerWiresDirectoryPickerRouteBeforeServing`. Continue reading `main.go`, but require this order:

```go
orderedSnippets := []string{
	"directoryPicker := service.NewDirectoryPicker(service.ExecCommandRunner{}, runtime.GOOS)",
	"systemHandler := handlers.NewSystemHandler(directoryPicker)",
	"router := handlers.NewRouter(noteHandler, settingsHandler, settingsService, spaHandler)",
	"registerSystemRoutes(router, systemHandler)",
	"return serveLocal(ctx, address, newHTTPServer(router)",
}
```

This keeps the existing route-wiring regression assertion while allowing the server lifecycle to change.

- [ ] **Step 2: Run integration tests and verify they fail**

Run:

```bash
go test ./cmd/api -run '^(TestParseServerOptions|TestRunServerWiresDirectoryPickerRouteBeforeServing)' -count=1
```

Expected: build failure because `serverOptions` and `parseServerOptions` do not exist, plus the source assertion does not yet find the new call.

- [ ] **Step 3: Refactor `main` into a returning server runner and outer signal dispatcher**

Create `cmd/api/signals_unix.go` so Linux and macOS handle both terminal interrupts and systemd termination:

```go
//go:build !windows

package main

import (
	"os"
	"syscall"
)

func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
```

Create `cmd/api/signals_windows.go` to keep Windows release builds portable:

```go
//go:build windows

package main

import "os"

func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
```

Then replace the current global `flag` parsing and fatal calls in `cmd/api/main.go` with the following structure. Keep the existing handler/router construction block unchanged between the shown resource setup and `serveLocal` call:

```go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"IGoNotes/internal/handlers"
	"IGoNotes/internal/repository"
	"IGoNotes/internal/service"
	"IGoNotes/web"
)

const gracefulShutdownTimeout = 10 * time.Second

type serverOptions struct {
	configPath string
	port       string
	base       string
	noBrowser  bool
}

func parseServerOptions(args []string) (serverOptions, error) {
	flags := flag.NewFlagSet("igonotes", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "Каталог конфигурации (по умолчанию системный каталог пользователя)")
	port := flags.String("port", "8080", "Порт сервера")
	base := flags.String("base", "", "Имя базы для открытия")
	noBrowser := flags.Bool("no-browser", false, "Не открывать браузер автоматически")
	if err := flags.Parse(args); err != nil {
		return serverOptions{}, err
	}
	return serverOptions{
		configPath: *configPath,
		port:       *port,
		base:       *base,
		noBrowser:  *noBrowser,
	}, nil
}

func runServer(ctx context.Context, args []string) (returnErr error) {
	options, err := parseServerOptions(args)
	if err != nil {
		return fmt.Errorf("parse command line: %w", err)
	}

	resolvedConfigDir, err := resolveConfigDir(options.configPath, os.UserConfigDir)
	if err != nil {
		return fmt.Errorf("Ошибка определения каталога конфигурации: %w", err)
	}
	configFile := filepath.Join(resolvedConfigDir, "config.json")
	configService := service.NewConfigService(configFile)

	appDataDir, err := resolveDataDir(os.UserHomeDir)
	if err != nil {
		return fmt.Errorf("Ошибка определения каталога данных: %w", err)
	}
	basePath, err := service.ResolveStartupBase(configService, options.base, appDataDir)
	if err != nil {
		return fmt.Errorf("Ошибка выбора базы заметок: %w", err)
	}

	dbPath := filepath.Join(appDataDir, "metadata.db")
	db, err := repository.InitDB(dbPath)
	if err != nil {
		return fmt.Errorf("Ошибка инициализации БД: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close metadata database: %w", err))
		}
	}()

	noteRepo := repository.NewNoteRepository(db)
	noteService := service.NewNoteService(noteRepo, basePath)
	defer func() {
		if err := noteService.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close note service: %w", err))
		}
	}()

	settingsService, err := service.NewSettingsService(configService, noteService, options.base, log.Default())
	if err != nil {
		return fmt.Errorf("Ошибка инициализации сервиса настроек: %w", err)
	}

	go func() {
		log.Println("Запуск первичной синхронизации файловой системы...")
		if err := noteService.SyncFS(); err != nil {
			log.Printf("Ошибка первичной синхронизации: %v", err)
		} else {
			log.Println("Первичная синхронизация завершена успешно.")
		}
	}()

	noteHandler := handlers.NewNoteHandler(noteService)
	settingsHandler := handlers.NewSettingsHandler(settingsService)
	directoryPicker := service.NewDirectoryPicker(service.ExecCommandRunner{}, runtime.GOOS)
	systemHandler := handlers.NewSystemHandler(directoryPicker)

	distFS, err := web.GetDistFS()
	if err != nil {
		return fmt.Errorf("Ошибка инициализации статических файлов фронтенда: %w", err)
	}
	spaHandler := handlers.NewSPAHandler(distFS)
	router := handlers.NewRouter(noteHandler, settingsHandler, settingsService, spaHandler)
	registerSystemRoutes(router, systemHandler)

	address, url := localServerEndpoint(options.port)
	return serveLocal(ctx, address, newHTTPServer(router), func() {
		log.Printf("Сервер запущен на %s", url)
		if !options.noBrowser {
			log.Printf("Открываем браузер: %s", url)
			if err := openBrowser(url); err != nil {
				log.Printf("Не удалось открыть браузер автоматически: %v", err)
			}
		}
	}, gracefulShutdownTimeout)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	manager := service.NewSystemdUserManager(
		runtime.GOOS,
		service.ExecCommandRunner{},
		exec.LookPath,
		os.UserConfigDir,
		os.Executable,
	)
	if err := dispatchCommand(ctx, os.Args[1:], os.Stdout, manager, runServer); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
```

The defer registration order is intentional: note service is registered after SQLite and therefore closes first. Ordinary positional arguments remain ignored after `FlagSet.Parse`, matching the previous global `flag.Parse` behavior; only the new `service` prefix gets subcommand semantics. Only outer `main` calls `os.Exit`; all `runServer` defers have already run by then. Systemd management stays runtime-gated in common code; only OS signal constants use complementary build-tagged files.

- [ ] **Step 4: Run CLI, route, lifecycle, and full package tests**

Run:

```bash
gofmt -w cmd/api/main.go cmd/api/signals_unix.go cmd/api/signals_windows.go cmd/api/service_command_test.go cmd/api/system_routes_test.go
go test ./cmd/api -count=1
go test ./internal/service -count=1
go test -race ./cmd/api ./internal/service
```

Expected: PASS with no race report. Confirm the existing `TestResolveConfigDirUsesExplicitDirectory` still proves ordinary startup leaves explicit relative config behavior unchanged; only service installation normalizes it.

- [ ] **Step 5: Commit production integration**

```bash
git add cmd/api/main.go cmd/api/signals_unix.go cmd/api/signals_windows.go cmd/api/service_command_test.go cmd/api/system_routes_test.go
git commit -m "feat: run IGoNotes as user service"
```

### Task 7: Document Linux Service and Chromium/PWA Usage

**Files:**
- Modify: `docs/user.md:5-27,78-80`
- Modify: `site/docs/user.md:9-22,67-69`

- [ ] **Step 1: Add the full user-service section to the primary guide**

Insert after the ordinary launch example in `docs/user.md`:

````markdown
## Запуск как пользовательский сервис Linux

На Linux с systemd сервер можно запускать автоматически при входе пользователя, не открывая обычную вкладку браузера:

```bash
igonotes service install
```

Команда создаёт `$XDG_CONFIG_HOME/systemd/user/igonotes.service` или, если `XDG_CONFIG_HOME` не задан, `~/.config/systemd/user/igonotes.service`, добавляет `--no-browser`, немедленно запускает сервис и включает его для следующих пользовательских сессий. `sudo` не требуется; linger не включается, поэтому сервис работает только в пользовательской сессии.

Параметры сервера задаются при установке:

```bash
igonotes service install --port 9000 --config /path/to/config --base work-notes
```

Повторная установка обновляет unit и перезапускает сервис. Unit содержит абсолютный путь текущего бинарника: после перемещения или замены расположения бинарника повторите `service install`.

Проверка состояния и журнала:

```bash
systemctl --user status igonotes.service
journalctl --user-unit igonotes.service
```

После запуска сервиса откройте его адрес и установите страницу штатным средством Chromium как приложение. Chromium-приложение привязано к origin, включая порт; после изменения порта откройте и при необходимости установите приложение для нового адреса.

Для удаления только сервиса:

```bash
igonotes service uninstall
```

Команда останавливает и удаляет user-unit, но не удаляет бинарник, настройки, базы или заметки. На Windows и macOS команды `service` не поддерживаются; обычный запуск и `--no-browser` продолжают работать.
````

Replace the final shutdown section with:

```markdown
## Завершение работы

При обычном запуске нажмите `Ctrl+C` в терминале. Для временной остановки установленного сервиса используйте `systemctl --user stop igonotes.service`; для его отключения и удаления используйте `igonotes service uninstall`.
```

- [ ] **Step 2: Mirror the service workflow in the published guide**

Insert this concise mirror after line 20 of `site/docs/user.md`:

````markdown
## Запуск как пользовательский сервис Linux

На Linux с systemd сервер можно автоматически запускать при входе пользователя без открытия вкладки:

```bash
igonotes service install
```

Команда создаёт и запускает пользовательский `igonotes.service`; `sudo` не требуется, linger не включается. Параметры фиксируются при установке, например `igonotes service install --port 9000 --config /path/to/config --base work-notes`. Повторная установка обновляет unit и перезапускает сервис.

Unit ссылается на абсолютный путь бинарника, поэтому после его перемещения повторите установку. Состояние и журнал доступны командами `systemctl --user status igonotes.service` и `journalctl --user-unit igonotes.service`.

Установленное Chromium-приложение привязано к адресу и порту сервера. После изменения порта откройте новый адрес и при необходимости установите приложение для нового origin.

```bash
igonotes service uninstall
```

Удаление сервиса не удаляет бинарник, настройки, базы или заметки. На Windows и macOS используйте обычный запуск и флаг `--no-browser`.
````

Replace the published shutdown section with:

```markdown
## Завершение работы

При обычном запуске нажмите `Ctrl+C`. Установленный сервис временно останавливается командой `systemctl --user stop igonotes.service`, а отключается и удаляется командой `igonotes service uninstall`.
```

- [ ] **Step 3: Review both guides for exact command and behavior consistency**

Run:

```bash
rg -n 'service install|service uninstall|systemctl --user|journalctl --user-unit|linger|Chromium' docs/user.md site/docs/user.md
```

Expected: both files contain installation, diagnostics, PWA port, session lifecycle, and removal guidance. Confirm neither guide claims the command copies the binary or starts at boot before login.

- [ ] **Step 4: Run documentation-sensitive and complete Go tests**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit documentation**

```bash
git add docs/user.md site/docs/user.md
git commit -m "docs: explain systemd user service"
```

### Task 8: Verify Builds, Unit Syntax, and End-to-End Behavior

**Files:**
- No source changes expected.

- [ ] **Step 1: Run formatting, vet, and race-enabled backend tests**

Run:

```bash
gofmt -w cmd/api/main.go cmd/api/server.go cmd/api/service_command.go cmd/api/signals_unix.go cmd/api/signals_windows.go cmd/api/server_address_test.go cmd/api/service_command_test.go cmd/api/system_routes_test.go internal/service/systemd_unit.go internal/service/systemd_unit_test.go internal/service/systemd_user_service.go internal/service/systemd_user_service_test.go
git diff --check
go vet ./...
go test -race ./...
```

Expected: no formatting diff after the first command, no whitespace errors, no vet diagnostics, and all tests PASS without races.

- [ ] **Step 2: Build frontend assets and the normal binary**

Run:

```bash
make all
```

Expected: npm dependencies install, Vite builds `web/dist`, and `builds/igonotes` is produced successfully. On a clean checkout, `npm ci --prefix web` may be run before `npm --prefix web run build` for a lockfile-strict frontend build.

- [ ] **Step 3: Verify every release target still cross-compiles**

Run:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/igonotes-linux-amd64 ./cmd/api
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/igonotes-linux-arm64 ./cmd/api
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /tmp/igonotes-windows-amd64.exe ./cmd/api
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -o /tmp/igonotes-windows-arm64.exe ./cmd/api
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o /tmp/igonotes-darwin-amd64 ./cmd/api
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /tmp/igonotes-darwin-arm64 ./cmd/api
```

Expected: all six commands succeed without cgo or platform-symbol errors.

- [ ] **Step 4: Perform the Linux user-systemd smoke test**

Use a binary in its intended stable location, then run:

```bash
./builds/igonotes service install
systemd-analyze --user verify "$HOME/.config/systemd/user/igonotes.service"
systemctl --user status igonotes.service
curl http://127.0.0.1:8080/api/info
./builds/igonotes service install --port 9000
systemctl --user status igonotes.service
journalctl --user-unit igonotes.service
./builds/igonotes service uninstall
```

Expected: the unit verifies, starts without opening a browser, serves the API, restarts on port 9000 after reinstall, logs to the user journal, and is absent/inactive after uninstall. If the local XDG config directory is not `$HOME/.config`, pass the actual `<os.UserConfigDir()>/systemd/user/igonotes.service` path to `systemd-analyze`.

- [ ] **Step 5: Verify the installed Chromium/PWA workflow manually**

Install again on the final chosen port, open that exact `http://127.0.0.1:<port>` origin once in Chromium, install/open the PWA, and confirm no ordinary browser tab is opened by IGoNotes. Log out and back in, then confirm `systemctl --user is-active igonotes.service` reports `active` and the PWA reconnects. Finally uninstall the service and confirm notes/configuration remain intact.

- [ ] **Step 6: Inspect final repository state**

Run:

```bash
git status --short
git log --oneline -8
```

Expected: no unintended generated files are staged; commits are limited to renderer, manager install/uninstall, CLI/lifecycle integration, and documentation. Do not commit `web/dist`, `builds`, or `/tmp` artifacts.
