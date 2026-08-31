package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"IGoNotes/internal/service"
)

type fakeUserServiceManager struct {
	installCalls     int
	uninstallCalls   int
	installContext   context.Context
	uninstallContext context.Context
	installOptions   service.SystemdInstallOptions
	installResult    service.SystemdInstallResult
	installErr       error
	uninstallErr     error
}

func (m *fakeUserServiceManager) Install(ctx context.Context, options service.SystemdInstallOptions) (service.SystemdInstallResult, error) {
	m.installCalls++
	m.installContext = ctx
	m.installOptions = options
	return m.installResult, m.installErr
}

func (m *fakeUserServiceManager) Uninstall(ctx context.Context) error {
	m.uninstallCalls++
	m.uninstallContext = ctx
	return m.uninstallErr
}

func TestDispatchCommandPassesOrdinaryArgumentsThroughUnchanged(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "empty", args: []string{}},
		{name: "server flags", args: []string{"--port", "9000", "--no-browser"}},
		{name: "non-service operand", args: []string{"serve", "--port", "9000"}},
		{name: "similar service name", args: []string{"Service", "install"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), struct{}{}, test.name)
			manager := &fakeUserServiceManager{}
			var gotContext context.Context
			var gotArgs []string
			runner := func(ctx context.Context, args []string) error {
				gotContext = ctx
				gotArgs = args
				return nil
			}

			if err := dispatchCommand(ctx, test.args, &bytes.Buffer{}, manager, runner); err != nil {
				t.Fatalf("dispatchCommand() error = %v", err)
			}
			if gotContext != ctx {
				t.Fatal("dispatchCommand() changed the context passed to the server runner")
			}
			if !reflect.DeepEqual(gotArgs, test.args) {
				t.Fatalf("server args = %#v, want %#v", gotArgs, test.args)
			}
			if len(test.args) > 0 && &gotArgs[0] != &test.args[0] {
				t.Fatal("dispatchCommand() did not pass the original argument slice")
			}
			if manager.installCalls != 0 || manager.uninstallCalls != 0 {
				t.Fatalf("manager calls = install %d, uninstall %d; want none", manager.installCalls, manager.uninstallCalls)
			}
		})
	}
}

func TestDispatchCommandDoesNotMutateArguments(t *testing.T) {
	args := []string{"service", "install", "--port", "9000"}
	want := append([]string(nil), args...)
	manager := &fakeUserServiceManager{installResult: service.SystemdInstallResult{URL: "http://127.0.0.1:9000"}}

	if err := dispatchCommand(context.Background(), args, &bytes.Buffer{}, manager, func(context.Context, []string) error {
		t.Fatal("server runner called for service command")
		return nil
	}); err != nil {
		t.Fatalf("dispatchCommand() error = %v", err)
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args after dispatch = %#v, want %#v", args, want)
	}
}

func TestRunServiceCommandInstallDefaultsAndContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "install")
	manager := &fakeUserServiceManager{}
	absCalls := 0

	err := runServiceCommand(ctx, []string{"install"}, &bytes.Buffer{}, manager, func(path string) (string, error) {
		absCalls++
		return "/unexpected/" + path, nil
	})
	if err != nil {
		t.Fatalf("runServiceCommand() error = %v", err)
	}
	if manager.installCalls != 1 {
		t.Fatalf("Install calls = %d, want 1", manager.installCalls)
	}
	want := service.SystemdInstallOptions{Port: "8080"}
	if manager.installOptions != want {
		t.Fatalf("Install options = %#v, want %#v", manager.installOptions, want)
	}
	if absCalls != 0 {
		t.Fatalf("absolutePath calls = %d, want 0 for omitted config", absCalls)
	}
	if manager.installContext != ctx {
		t.Fatal("Install received a different context")
	}
}

func TestRunServiceCommandInstallPassesOptionsAndNormalizesConfig(t *testing.T) {
	manager := &fakeUserServiceManager{}
	var absInput string

	err := runServiceCommand(context.Background(), []string{
		"install", "--port", "9123", "--config", "relative/config", "--base", "work notes",
	}, &bytes.Buffer{}, manager, func(path string) (string, error) {
		absInput = path
		return "/install-cwd/relative/config", nil
	})
	if err != nil {
		t.Fatalf("runServiceCommand() error = %v", err)
	}
	if absInput != "relative/config" {
		t.Fatalf("absolutePath input = %q, want %q", absInput, "relative/config")
	}
	want := service.SystemdInstallOptions{
		Port:      "9123",
		ConfigDir: "/install-cwd/relative/config",
		Base:      "work notes",
	}
	if manager.installOptions != want {
		t.Fatalf("Install options = %#v, want %#v", manager.installOptions, want)
	}
}

func TestRunServiceCommandInstallAbsolutePathErrorPreventsManagerCall(t *testing.T) {
	absErr := errors.New("cannot resolve working directory")
	manager := &fakeUserServiceManager{}

	err := runServiceCommand(context.Background(), []string{"install", "--config", "relative"}, &bytes.Buffer{}, manager, func(string) (string, error) {
		return "", absErr
	})
	if !errors.Is(err, absErr) {
		t.Fatalf("error = %v, want errors.Is(absErr)", err)
	}
	if manager.installCalls != 0 {
		t.Fatalf("Install calls = %d, want 0", manager.installCalls)
	}
}

func TestRunServiceCommandRejectsInvalidSyntaxWithoutManagerCall(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantMessage string
	}{
		{name: "missing subcommand", args: nil, wantMessage: "install or uninstall"},
		{name: "unknown subcommand", args: []string{"status"}, wantMessage: "expected install or uninstall"},
		{name: "unknown install flag", args: []string{"install", "--unknown"}, wantMessage: "flag provided but not defined"},
		{name: "no-browser is manager-owned", args: []string{"install", "--no-browser"}, wantMessage: "flag provided but not defined"},
		{name: "install operand", args: []string{"install", "extra"}, wantMessage: "does not accept operands"},
		{name: "install operand before flag", args: []string{"install", "extra", "--port", "9000"}, wantMessage: "does not accept operands"},
		{name: "uninstall option", args: []string{"uninstall", "--port", "9000"}, wantMessage: "does not accept options or operands"},
		{name: "uninstall operand", args: []string{"uninstall", "extra"}, wantMessage: "does not accept options or operands"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := &fakeUserServiceManager{}
			err := runServiceCommand(context.Background(), test.args, &bytes.Buffer{}, manager, func(path string) (string, error) {
				return path, nil
			})
			if err == nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("error = %v, want message containing %q", err, test.wantMessage)
			}
			if manager.installCalls != 0 || manager.uninstallCalls != 0 {
				t.Fatalf("manager calls = install %d, uninstall %d; want none", manager.installCalls, manager.uninstallCalls)
			}
		})
	}
}

func TestRunServiceCommandUninstallCallsManagerOnceWithContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "uninstall")
	manager := &fakeUserServiceManager{}

	if err := runServiceCommand(ctx, []string{"uninstall"}, &bytes.Buffer{}, manager, func(path string) (string, error) {
		return path, nil
	}); err != nil {
		t.Fatalf("runServiceCommand() error = %v", err)
	}
	if manager.uninstallCalls != 1 {
		t.Fatalf("Uninstall calls = %d, want 1", manager.uninstallCalls)
	}
	if manager.uninstallContext != ctx {
		t.Fatal("Uninstall received a different context")
	}
}

func TestRunServiceCommandPreservesManagerErrors(t *testing.T) {
	managerErr := errors.New("systemctl failed")
	tests := []struct {
		name    string
		args    []string
		manager *fakeUserServiceManager
	}{
		{name: "install", args: []string{"install"}, manager: &fakeUserServiceManager{installErr: managerErr}},
		{name: "uninstall", args: []string{"uninstall"}, manager: &fakeUserServiceManager{uninstallErr: managerErr}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := runServiceCommand(context.Background(), test.args, &bytes.Buffer{}, test.manager, func(path string) (string, error) {
				return path, nil
			})
			if !errors.Is(err, managerErr) {
				t.Fatalf("error = %v, want errors.Is(managerErr)", err)
			}
		})
	}
}

func TestRunServiceCommandInstallOutputIncludesOperationalDetails(t *testing.T) {
	result := service.SystemdInstallResult{
		URL:      "http://127.0.0.1:8123",
		UnitPath: "/home/test/.config/systemd/user/igonotes.service",
	}
	manager := &fakeUserServiceManager{installResult: result}
	var output bytes.Buffer

	if err := runServiceCommand(context.Background(), []string{"install", "--port", "8123"}, &output, manager, func(path string) (string, error) {
		return path, nil
	}); err != nil {
		t.Fatalf("runServiceCommand() error = %v", err)
	}
	for _, want := range []string{
		"installed", "started", result.URL, result.UnitPath,
		"systemctl --user status " + service.SystemdUserUnitName,
		"journalctl --user-unit " + service.SystemdUserUnitName,
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output %q does not contain %q", output.String(), want)
		}
	}
}

func TestRunServiceCommandUninstallOutputPreservesUserData(t *testing.T) {
	var output bytes.Buffer

	if err := runServiceCommand(context.Background(), []string{"uninstall"}, &output, &fakeUserServiceManager{}, func(path string) (string, error) {
		return path, nil
	}); err != nil {
		t.Fatalf("runServiceCommand() error = %v", err)
	}
	for _, want := range []string{"removed", "notes", "config", "untouched"} {
		if !strings.Contains(strings.ToLower(output.String()), want) {
			t.Errorf("output %q does not contain %q", output.String(), want)
		}
	}
}
