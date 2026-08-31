package service

import (
	"context"
	"errors"
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
	goos             string
	runner           CommandRunner
	lookPath         func(string) (string, error)
	userConfigDir    func() (string, error)
	executable       func() (string, error)
	remove           func(string) error
	syncDirectory    func(string) error
	onLockContention func()
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
		remove:        os.Remove,
		syncDirectory: syncSystemdUnitDirectory,
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

	if !isASCIIDecimal(options.Port) {
		return SystemdInstallResult{}, fmt.Errorf("install systemd user service: invalid port %q: must be a decimal integer from 1 to 65535", options.Port)
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

	existing, targetExists, err := readSystemdUnitEntry(unitPath)
	if err != nil {
		return SystemdInstallResult{}, fmt.Errorf("read existing systemd user unit %q: %w", unitPath, err)
	}
	if targetExists {
		if !hasSystemdUnitMarker(existing) {
			return SystemdInstallResult{}, fmt.Errorf("refuse to replace %q: existing unit is not managed by IGoNotes", unitPath)
		}
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
	unitDirectory := filepath.Dir(unitPath)
	if err := os.MkdirAll(unitDirectory, 0o755); err != nil {
		return SystemdInstallResult{}, fmt.Errorf("create systemd user unit directory %q: %w", unitDirectory, err)
	}

	activationCommands := []struct {
		operation string
		args      []string
	}{
		{operation: "reload systemd user manager", args: []string{"--user", "daemon-reload"}},
		{operation: "enable " + SystemdUserUnitName, args: []string{"--user", "enable", SystemdUserUnitName}},
		{operation: "restart " + SystemdUserUnitName, args: []string{"--user", "restart", SystemdUserUnitName}},
	}
	err = withSystemdUnitLock(ctx, unitPath, m.onLockContention, func() error {
		existing, targetExists, err := readSystemdUnitEntry(unitPath)
		if err != nil {
			return fmt.Errorf("read existing systemd user unit %q under lock: %w", unitPath, err)
		}
		if targetExists {
			if !hasSystemdUnitMarker(existing) {
				return fmt.Errorf("refuse to replace %q: existing unit is not managed by IGoNotes", unitPath)
			}
		}
		if err := installSystemdUnitAtomically(unitPath, unit, targetExists); err != nil {
			return err
		}
		for _, command := range activationCommands {
			result, runErr := m.runner.Run(ctx, systemctlPath, command.args...)
			if runErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					runErr = errors.Join(ctxErr, runErr)
				}
				return systemdActivationError(command.operation, result, runErr)
			}
		}
		return nil
	})
	if err != nil {
		return SystemdInstallResult{}, err
	}

	return SystemdInstallResult{
		UnitPath: unitPath,
		URL:      "http://" + net.JoinHostPort("127.0.0.1", options.Port),
	}, nil
}

func (m *SystemdUserManager) Uninstall(ctx context.Context) error {
	if m == nil {
		return fmt.Errorf("uninstall systemd user service: manager is nil")
	}
	if m.goos != "linux" {
		return fmt.Errorf("uninstall systemd user service is supported only on linux, not %s", m.goos)
	}
	if m.runner == nil {
		return fmt.Errorf("uninstall systemd user service: command runner is nil")
	}
	if m.lookPath == nil {
		return fmt.Errorf("uninstall systemd user service: LookPath dependency is nil")
	}
	if m.userConfigDir == nil {
		return fmt.Errorf("uninstall systemd user service: UserConfigDir dependency is nil")
	}

	configRoot, err := m.userConfigDir()
	if err != nil {
		return fmt.Errorf("resolve user config directory: %w", err)
	}
	if configRoot == "" {
		return fmt.Errorf("resolve user config directory: config root is empty")
	}
	unitPath := filepath.Join(configRoot, "systemd", "user", SystemdUserUnitName)
	if _, err := os.Lstat(unitPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat systemd user unit %q: %w", unitPath, err)
	}

	return withSystemdUnitLock(ctx, unitPath, m.onLockContention, func() error {
		content, targetExists, err := readSystemdUnitEntry(unitPath)
		if err != nil {
			return fmt.Errorf("read systemd user unit %q: %w", unitPath, err)
		}
		if !targetExists {
			return nil
		}
		if !hasSystemdUnitMarker(content) {
			return fmt.Errorf("refuse to uninstall %q: unit is not managed by IGoNotes", unitPath)
		}

		systemctlPath, err := m.lookPath("systemctl")
		if err != nil {
			return fmt.Errorf("resolve systemctl: %w", err)
		}
		if systemctlPath == "" {
			return fmt.Errorf("resolve systemctl: LookPath returned an empty path")
		}
		systemctlPath, err = filepath.Abs(systemctlPath)
		if err != nil {
			return fmt.Errorf("make systemctl path absolute: %w", err)
		}
		if _, err := m.runSystemctl(ctx, systemctlPath, "probe systemctl --user show-environment", "--user", "show-environment"); err != nil {
			return err
		}
		if _, err := m.runSystemctl(ctx, systemctlPath, "disable systemd user service", "--user", "disable", "--now", SystemdUserUnitName); err != nil {
			return err
		}
		// This is a collision safeguard, not atomic exclusion from non-cooperating same-user writers.
		content, err = os.ReadFile(unitPath)
		if err != nil {
			return fmt.Errorf("revalidate systemd user unit %q before removal: %w", unitPath, err)
		}
		if !hasSystemdUnitMarker(content) {
			return fmt.Errorf("refuse to remove %q: unit is not managed by IGoNotes after disable", unitPath)
		}
		if err := m.remove(unitPath); err != nil {
			return fmt.Errorf("remove systemd user unit %q: %w", unitPath, err)
		}

		durabilityErr := m.syncDirectory(filepath.Dir(unitPath))
		if durabilityErr != nil {
			durabilityErr = fmt.Errorf("persist systemd user unit removal %q: %w", unitPath, durabilityErr)
		}
		_, reloadErr := m.runSystemctl(ctx, systemctlPath, "reload systemd user manager", "--user", "daemon-reload")
		return errors.Join(durabilityErr, reloadErr)
	})
}

func syncSystemdUnitDirectory(directory string) error {
	parent, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open systemd user unit directory %q after removal: %w", directory, err)
	}
	if err := parent.Sync(); err != nil {
		_ = parent.Close()
		return fmt.Errorf("sync systemd user unit directory %q after removal: %w", directory, err)
	}
	if err := parent.Close(); err != nil {
		return fmt.Errorf("close systemd user unit directory %q after removal: %w", directory, err)
	}
	return nil
}

func isASCIIDecimal(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
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
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = errors.Join(ctxErr, err)
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

func readSystemdUnitEntry(path string) ([]byte, bool, error) {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, true, err
	}
	return content, true, nil
}

func installSystemdUnitAtomically(unitPath string, content []byte, targetExisted bool) error {
	directory := filepath.Dir(unitPath)
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

	parent, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open systemd user unit directory %q: %w", directory, err)
	}
	defer parent.Close()

	if targetExisted {
		existing, err := os.ReadFile(unitPath)
		if err != nil {
			return fmt.Errorf("revalidate existing systemd user unit %q: %w", unitPath, err)
		}
		if !hasSystemdUnitMarker(existing) {
			return fmt.Errorf("refuse to replace %q: existing unit is no longer managed by IGoNotes", unitPath)
		}
		if err := os.Rename(temporaryPath, unitPath); err != nil {
			return fmt.Errorf("replace systemd user unit %q: %w", unitPath, err)
		}
	} else {
		if err := renameNoReplace(parent, filepath.Base(temporaryPath), filepath.Base(unitPath)); err != nil {
			return fmt.Errorf("replace initially absent systemd user unit %q without overwriting: %w", unitPath, err)
		}
	}

	if err := parent.Sync(); err != nil {
		return fmt.Errorf("sync systemd user unit directory %q: %w", directory, err)
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
