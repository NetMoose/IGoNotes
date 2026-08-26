package main

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestResolveDataDirUsesUserHome(t *testing.T) {
	homeDir := filepath.Join("home", "user")
	got, err := resolveDataDir(func() (string, error) { return homeDir, nil })
	if err != nil {
		t.Fatalf("resolveDataDir() error = %v, want nil", err)
	}
	want := filepath.Join(homeDir, ".igonotes")
	if got != want {
		t.Fatalf("resolveDataDir() = %q, want %q", got, want)
	}
}

func TestResolveDataDirReturnsHomeError(t *testing.T) {
	wantErr := errors.New("home directory unavailable")
	_, err := resolveDataDir(func() (string, error) { return "", wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("resolveDataDir() error = %v, want wrapped %v", err, wantErr)
	}
}

func TestResolveDataDirRejectsEmptyHome(t *testing.T) {
	_, err := resolveDataDir(func() (string, error) { return "", nil })
	if err == nil {
		t.Fatal("resolveDataDir() error = nil, want non-nil")
	}
}
