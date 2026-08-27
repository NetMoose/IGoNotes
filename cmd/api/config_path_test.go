package main

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestResolveConfigDirUsesExplicitDirectory(t *testing.T) {
	called := false
	got, err := resolveConfigDir("/custom/config", func() (string, error) {
		called = true
		return "", errors.New("must not be called")
	})
	if err != nil {
		t.Fatalf("resolveConfigDir() error = %v", err)
	}
	if called {
		t.Fatal("resolveConfigDir() called userConfigDir for an explicit directory")
	}
	if got != "/custom/config" {
		t.Fatalf("resolveConfigDir() = %q, want %q", got, "/custom/config")
	}
}

func TestResolveConfigDirUsesSystemDirectory(t *testing.T) {
	systemDir := filepath.Join("home", "user", ".config")
	got, err := resolveConfigDir("", func() (string, error) { return systemDir, nil })
	if err != nil {
		t.Fatalf("resolveConfigDir() error = %v", err)
	}
	want := filepath.Join(systemDir, "igonotes")
	if got != want {
		t.Fatalf("resolveConfigDir() = %q, want %q", got, want)
	}
}

func TestResolveConfigDirReturnsSystemError(t *testing.T) {
	wantErr := errors.New("config directory unavailable")
	_, err := resolveConfigDir("", func() (string, error) { return "", wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("resolveConfigDir() error = %v, want wrapped %v", err, wantErr)
	}
}
