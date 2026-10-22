package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"IGoNotes/internal/model"
)

func TestPrivateStorageStartupRejectsBeforeDBOpen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	configDir := t.TempDir()
	safe := t.TempDir()
	data := filepath.Join(home, ".igonotes")
	completed := true
	config := model.Config{Bases: []model.Base{{Name: "safe", Path: safe}, {Name: "inactive-private", Path: data}}, CurrentBase: "safe", SetupCompleted: &completed}
	before, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configFile, before, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = runServer(ctx, []string{"--config", configDir, "--base", "safe", "--no-browser", "--port", "0"})
	if err == nil || !strings.Contains(err.Error(), "inactive-private") || !strings.Contains(err.Error(), "вручную") {
		t.Fatalf("startup error = %v", err)
	}
	after, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("config changed")
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("SQLite/runtime writes happened before rejection: %v", err)
	}
}

func TestPrivateStorageStartupRejectsUnsafeCustomConfigBeforeDefaultWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	data := filepath.Join(home, ".igonotes")
	configDir := filepath.Join(data, "bases", "default", "config")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := runServer(ctx, []string{"--config", configDir, "--no-browser", "--port", "0"})
	if err == nil || !strings.Contains(err.Error(), "вручную") {
		t.Fatalf("startup error = %v", err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("default files created: %v", err)
	}
}
