package service

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
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
	if m == nil {
		return SystemdInstallResult{}, fmt.Errorf("install systemd user service: manager is nil")
	}
	if m.goos != "linux" {
		return SystemdInstallResult{}, fmt.Errorf("install systemd user service is supported only on linux, not %s", m.goos)
	}
	if m.runner == nil {
		return SystemdInstallResult{}, fmt.Errorf("install systemd user service: command runner is nil")
	}
	if m.lookPath == nil {
		return SystemdInstallResult{}, fmt.Errorf("install systemd user service: LookPath dependency is nil")
	}
	if m.userConfigDir == nil {
		return SystemdInstallResult{}, fmt.Errorf("install systemd user service: UserConfigDir dependency is nil")
	}
	if m.executable == nil {
		return SystemdInstallResult{}, fmt.Errorf("install systemd user service: Executable dependency is nil")
	}

	port, err := strconv.Atoi(options.Port)
	if err != nil || port < 1 || port > 65535 {
		return SystemdInstallResult{}, fmt.Errorf("install systemd user service: invalid port %q: must be a decimal integer from 1 to 65535", options.Port)
	}

	configRoot, err := m.userConfigDir()
	if err != nil {
		return SystemdInstallResult{}, fmt.Errorf("resolve user config directory: %w", err)
	}
	if configRoot == "" {
		return SystemdInstallResult{}, fmt.Errorf("resolve user config directory: config root is empty")
	}
	unitPath := filepath.Join(configRoot, "systemd", "user", SystemdUserUnitName)

	systemctlPath, err := m.lookPath("systemctl")
	if err != nil {
		return SystemdInstallResult{}, fmt.Errorf("resolve systemctl: %w", err)
	}
	if systemctlPath == "" {
		return SystemdInstallResult{}, fmt.Errorf("resolve systemctl: LookPath returned an empty path")
	}
	systemctlPath, err = filepath.Abs(systemctlPath)
	if err != nil {
		return SystemdInstallResult{}, fmt.Errorf("make systemctl path absolute: %w", err)
	}
	if _, err := m.runSystemctl(ctx, systemctlPath, "probe systemctl --user show-environment", "--user", "show-environment"); err != nil {
		return SystemdInstallResult{}, err
	}

	existing, err := os.ReadFile(unitPath)
	if err == nil {
		if !hasSystemdUnitMarker(existing) {
			return SystemdInstallResult{}, fmt.Errorf("refuse to replace %q: existing unit is not managed by IGoNotes", unitPath)
		}
	} else if !os.IsNotExist(err) {
		return SystemdInstallResult{}, fmt.Errorf("read existing systemd user unit %q: %w", unitPath, err)
	}

	executablePath, err := m.executable()
	if err != nil {
		return SystemdInstallResult{}, fmt.Errorf("resolve current executable: %w", err)
	}
	if executablePath == "" {
		return SystemdInstallResult{}, fmt.Errorf("resolve current executable: executable path is empty")
	}
	executablePath, err = filepath.Abs(executablePath)
	if err != nil {
		return SystemdInstallResult{}, fmt.Errorf("make executable path absolute: %w", err)
	}
	executableInfo, err := os.Stat(executablePath)
	if err != nil {
		return SystemdInstallResult{}, fmt.Errorf("stat executable %q: %w", executablePath, err)
	}
	if !executableInfo.Mode().IsRegular() {
		return SystemdInstallResult{}, fmt.Errorf("validate executable %q: executable is not a regular file", executablePath)
	}
	if executableInfo.Mode().Perm()&0o111 == 0 {
		return SystemdInstallResult{}, fmt.Errorf("validate executable %q: executable has no execute bit", executablePath)
	}

	unit, err := renderSystemdUserUnit(executablePath, options)
	if err != nil {
		return SystemdInstallResult{}, fmt.Errorf("render systemd user unit: %w", err)
	}
	if err := installSystemdUnitAtomically(unitPath, unit); err != nil {
		return SystemdInstallResult{}, err
	}

	activationCommands := []struct {
		operation string
		args      []string
	}{
		{operation: "reload systemd user manager", args: []string{"--user", "daemon-reload"}},
		{operation: "enable " + SystemdUserUnitName, args: []string{"--user", "enable", SystemdUserUnitName}},
		{operation: "restart " + SystemdUserUnitName, args: []string{"--user", "restart", SystemdUserUnitName}},
	}
	for _, command := range activationCommands {
		result, runErr := m.runner.Run(ctx, systemctlPath, command.args...)
		if runErr != nil {
			return SystemdInstallResult{}, systemdActivationError(command.operation, result, runErr)
		}
	}

	return SystemdInstallResult{
		UnitPath: unitPath,
		URL:      "http://" + net.JoinHostPort("127.0.0.1", options.Port),
	}, nil
}

func (m *SystemdUserManager) runSystemctl(
	ctx context.Context,
	systemctlPath string,
	operation string,
	args ...string,
) (CommandResult, error) {
	result, err := m.runner.Run(ctx, systemctlPath, args...)
	if err == nil {
		return result, nil
	}
	diagnostic := commandDiagnostic(result)
	if diagnostic != "" {
		return result, fmt.Errorf("%s: %w: %s", operation, err, diagnostic)
	}
	return result, fmt.Errorf("%s: %w", operation, err)
}

func hasSystemdUnitMarker(content []byte) bool {
	prefix := systemdUnitMarker + "\n"
	return len(content) >= len(prefix) && string(content[:len(prefix)]) == prefix
}

func installSystemdUnitAtomically(unitPath string, content []byte) error {
	directory := filepath.Dir(unitPath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create systemd user unit directory %q: %w", directory, err)
	}

	temporary, err := os.CreateTemp(directory, ".igonotes-service-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary systemd user unit in %q: %w", directory, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("chmod temporary systemd user unit %q: %w", temporaryPath, err)
	}
	written, err := temporary.Write(content)
	if err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary systemd user unit %q: %w", temporaryPath, err)
	}
	if written != len(content) {
		_ = temporary.Close()
		return fmt.Errorf("write temporary systemd user unit %q: %w", temporaryPath, io.ErrShortWrite)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary systemd user unit %q: %w", temporaryPath, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary systemd user unit %q: %w", temporaryPath, err)
	}
	if err := os.Rename(temporaryPath, unitPath); err != nil {
		return fmt.Errorf("replace systemd user unit %q: %w", unitPath, err)
	}
	return nil
}

func systemdActivationError(operation string, result CommandResult, err error) error {
	diagnostic := commandDiagnostic(result)
	if diagnostic != "" {
		diagnostic = ": " + diagnostic
	}
	return fmt.Errorf(
		"%s: %w%s; inspect with `systemctl --user status %s` and `journalctl --user-unit %s`",
		operation,
		err,
		diagnostic,
		SystemdUserUnitName,
		SystemdUserUnitName,
	)
}
