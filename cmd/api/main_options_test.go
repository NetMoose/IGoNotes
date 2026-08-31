package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
)

func TestParseServerOptionsDefaults(t *testing.T) {
	got, err := parseServerOptions(nil, io.Discard)
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
	}, io.Discard)
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
	first, err := parseServerOptions([]string{"--port", "9000", "--no-browser"}, io.Discard)
	if err != nil {
		t.Fatalf("first parseServerOptions() error = %v", err)
	}
	second, err := parseServerOptions(nil, io.Discard)
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
	got, err := parseServerOptions([]string{"--port", "9000", "remainder", "--base", "ignored"}, io.Discard)
	if err != nil {
		t.Fatalf("parseServerOptions() error = %v", err)
	}

	want := serverOptions{port: "9000"}
	if got != want {
		t.Fatalf("parseServerOptions() = %#v, want %#v", got, want)
	}
}

func TestParseServerOptionsReturnsParseError(t *testing.T) {
	var output bytes.Buffer
	_, err := parseServerOptions([]string{"--unknown"}, &output)
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("parseServerOptions() error = %v, want unknown flag error", err)
	}
	if got := commandExitCode(err); got != 2 {
		t.Fatalf("commandExitCode() = %d, want 2", got)
	}
	if shouldLogCommandError(err) {
		t.Fatal("shouldLogCommandError() = true for an error already reported by FlagSet")
	}
	if !strings.Contains(output.String(), "flag provided but not defined") {
		t.Fatalf("parser output = %q, want unknown flag diagnostic", output.String())
	}
}

func TestParseServerOptionsHelpPrintsUsageAndClassifiesSuccess(t *testing.T) {
	for _, helpFlag := range []string{"-h", "--help"} {
		t.Run(helpFlag, func(t *testing.T) {
			var output bytes.Buffer
			_, err := parseServerOptions([]string{helpFlag}, &output)
			if !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("parseServerOptions() error = %v, want errors.Is(flag.ErrHelp)", err)
			}
			if got := commandExitCode(err); got != 0 {
				t.Fatalf("commandExitCode() = %d, want 0", got)
			}
			if shouldLogCommandError(err) {
				t.Fatal("shouldLogCommandError() = true for help")
			}
			for _, want := range []string{"Usage of igonotes", "-config", "-port", "-base", "-no-browser"} {
				if !strings.Contains(output.String(), want) {
					t.Errorf("help output %q does not contain %q", output.String(), want)
				}
			}
		})
	}
}

func TestCommandExitCodeClassifiesRuntimeAndSuccess(t *testing.T) {
	if got := commandExitCode(nil); got != 0 {
		t.Fatalf("commandExitCode(nil) = %d, want 0", got)
	}
	runtimeErr := errors.New("startup failed")
	if got := commandExitCode(runtimeErr); got != 1 {
		t.Fatalf("commandExitCode(runtime error) = %d, want 1", got)
	}
	if !shouldLogCommandError(runtimeErr) {
		t.Fatal("shouldLogCommandError() = false for runtime error")
	}
}
