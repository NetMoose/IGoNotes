package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type portableSystemdRunner struct {
	calls int
}

func (r *portableSystemdRunner) Run(context.Context, string, ...string) (CommandResult, error) {
	r.calls++
	return CommandResult{}, errors.New("must not be called")
}

func TestSystemdUserManagerInstallRejectsUnsupportedOSBeforeDependencies(t *testing.T) {
	runner := &portableSystemdRunner{}
	called := false
	dependency := func() (string, error) {
		called = true
		return "", errors.New("must not be called")
	}
	lookPath := func(string) (string, error) {
		called = true
		return "", errors.New("must not be called")
	}
	manager := NewSystemdUserManager("darwin", runner, lookPath, dependency, dependency)

	_, err := manager.Install(context.Background(), SystemdInstallOptions{Port: "8080"})
	if err == nil || !strings.Contains(err.Error(), "linux") {
		t.Fatalf("Install() error = %v, want unsupported Linux-only error", err)
	}
	if called {
		t.Error("Install() called an injected dependency for unsupported OS")
	}
	if runner.calls != 0 {
		t.Errorf("Run calls = %d, want 0", runner.calls)
	}
}
