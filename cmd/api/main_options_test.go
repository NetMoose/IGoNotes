package main

import (
	"strings"
	"testing"
)

func TestParseServerOptionsDefaults(t *testing.T) {
	got, err := parseServerOptions(nil)
	if err != nil {
		t.Fatalf("parseServerOptions() error = %v", err)
	}

	want := serverOptions{port: "8080"}
	if got != want {
		t.Fatalf("parseServerOptions() = %#v, want %#v", got, want)
	}
}

func TestParseServerOptionsAllFlags(t *testing.T) {
	got, err := parseServerOptions([]string{
		"--config", "relative/config",
		"--port", "9123",
		"--base", "work notes",
		"--no-browser",
	})
	if err != nil {
		t.Fatalf("parseServerOptions() error = %v", err)
	}

	want := serverOptions{
		configPath: "relative/config",
		port:       "9123",
		base:       "work notes",
		noBrowser:  true,
	}
	if got != want {
		t.Fatalf("parseServerOptions() = %#v, want %#v", got, want)
	}
}

func TestParseServerOptionsDoesNotRetainPreviousValues(t *testing.T) {
	first, err := parseServerOptions([]string{"--port", "9000", "--no-browser"})
	if err != nil {
		t.Fatalf("first parseServerOptions() error = %v", err)
	}
	second, err := parseServerOptions(nil)
	if err != nil {
		t.Fatalf("second parseServerOptions() error = %v", err)
	}

	if first != (serverOptions{port: "9000", noBrowser: true}) {
		t.Fatalf("first parseServerOptions() = %#v", first)
	}
	if second != (serverOptions{port: "8080"}) {
		t.Fatalf("second parseServerOptions() = %#v, want defaults", second)
	}
}

func TestParseServerOptionsIgnoresPositionalRemainder(t *testing.T) {
	got, err := parseServerOptions([]string{"--port", "9000", "remainder", "--base", "ignored"})
	if err != nil {
		t.Fatalf("parseServerOptions() error = %v", err)
	}

	want := serverOptions{port: "9000"}
	if got != want {
		t.Fatalf("parseServerOptions() = %#v, want %#v", got, want)
	}
}

func TestParseServerOptionsReturnsParseError(t *testing.T) {
	_, err := parseServerOptions([]string{"--unknown"})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("parseServerOptions() error = %v, want unknown flag error", err)
	}
}
