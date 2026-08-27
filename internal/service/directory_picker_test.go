package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type recordingDirectoryPickerRunner struct {
	result CommandResult
	err    error
	calls  int
	name   string
	args   []string
}

func (r *recordingDirectoryPickerRunner) Run(_ context.Context, name string, args ...string) (CommandResult, error) {
	r.calls++
	r.name = name
	r.args = append([]string(nil), args...)
	return r.result, r.err
}

func TestDirectoryPickerSelectDirectoryWindows(t *testing.T) {
	t.Run("returns selected path and removes trailing CRLF", func(t *testing.T) {
		output := []byte("C:\\Users\\alice\\Notes\r\n")
		originalOutput := slices.Clone(output)
		runner := &recordingDirectoryPickerRunner{result: CommandResult{Output: output}}
		picker := NewDirectoryPicker(runner, "windows")

		got, err := picker.SelectDirectory(context.Background())
		if err != nil {
			t.Fatalf("SelectDirectory() error = %v, want nil", err)
		}
		if want := `C:\Users\alice\Notes`; got != want {
			t.Errorf("SelectDirectory() = %q, want %q", got, want)
		}
		if runner.calls != 1 {
			t.Fatalf("Run calls = %d, want 1", runner.calls)
		}
		if runner.name != "powershell.exe" {
			t.Errorf("Run executable = %q, want %q", runner.name, "powershell.exe")
		}
		wantArgs := []string{"-NoProfile", "-NonInteractive", "-STA", "-Command", windowsPickerScript}
		if !reflect.DeepEqual(runner.args, wantArgs) {
			t.Errorf("Run args = %#v, want %#v", runner.args, wantArgs)
		}
		if !slices.Equal(output, originalOutput) {
			t.Errorf("runner output mutated to %q, want %q", output, originalOutput)
		}
	})

	t.Run("returns canceled only for exact marker", func(t *testing.T) {
		runner := &recordingDirectoryPickerRunner{result: CommandResult{Output: []byte(windowsPickerCancelMarker + "\r\n")}}

		path, err := NewDirectoryPicker(runner, "windows").SelectDirectory(context.Background())
		if path != "" {
			t.Errorf("SelectDirectory() path = %q, want empty", path)
		}
		if !errors.Is(err, ErrDirectorySelectionCanceled) {
			t.Fatalf("SelectDirectory() error = %v, want ErrDirectorySelectionCanceled", err)
		}
	})

	t.Run("preserves leading and trailing path spaces", func(t *testing.T) {
		runner := &recordingDirectoryPickerRunner{result: CommandResult{Output: []byte(" C:\\Notes \\ \r\n")}}

		got, err := NewDirectoryPicker(runner, "windows").SelectDirectory(context.Background())
		if err != nil {
			t.Fatalf("SelectDirectory() error = %v, want nil", err)
		}
		if want := ` C:\Notes \ `; got != want {
			t.Errorf("SelectDirectory() = %q, want %q", got, want)
		}
	})

	t.Run("rejects empty output", func(t *testing.T) {
		runner := &recordingDirectoryPickerRunner{result: CommandResult{Output: []byte("\r\n")}}

		path, err := NewDirectoryPicker(runner, "windows").SelectDirectory(context.Background())
		if path != "" {
			t.Errorf("SelectDirectory() path = %q, want empty", path)
		}
		if err == nil {
			t.Fatal("SelectDirectory() error = nil, want operational error")
		}
		if errors.Is(err, ErrDirectorySelectionCanceled) || errors.Is(err, ErrDirectoryPickerUnavailable) {
			t.Errorf("SelectDirectory() error = %v, want non-sentinel operational error", err)
		}
	})

	t.Run("maps missing PowerShell to unavailable", func(t *testing.T) {
		runner := &recordingDirectoryPickerRunner{err: exec.ErrNotFound}

		path, err := NewDirectoryPicker(runner, "windows").SelectDirectory(context.Background())
		if path != "" {
			t.Errorf("SelectDirectory() path = %q, want empty", path)
		}
		if !errors.Is(err, ErrDirectoryPickerUnavailable) {
			t.Fatalf("SelectDirectory() error = %v, want ErrDirectoryPickerUnavailable", err)
		}
		if !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("SelectDirectory() error = %v, want wrapped exec.ErrNotFound", err)
		}
	})

	t.Run("wraps process failure with diagnostic output", func(t *testing.T) {
		commandErr := errors.New("exit status 9")
		runner := &recordingDirectoryPickerRunner{
			result: CommandResult{Output: []byte("PowerShell failed\r\n"), ExitCode: 9},
			err:    commandErr,
		}

		path, err := NewDirectoryPicker(runner, "windows").SelectDirectory(context.Background())
		if path != "" {
			t.Errorf("SelectDirectory() path = %q, want empty", path)
		}
		if !errors.Is(err, commandErr) {
			t.Fatalf("SelectDirectory() error = %v, want wrapped %v", err, commandErr)
		}
		if !strings.Contains(err.Error(), "PowerShell failed") {
			t.Errorf("SelectDirectory() error = %q, want diagnostic output", err)
		}
		if errors.Is(err, ErrDirectorySelectionCanceled) {
			t.Errorf("SelectDirectory() error = %v, do not want cancellation sentinel", err)
		}
	})

	t.Run("wraps canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runnerErr := errors.New("runner stopped")
		runner := &recordingDirectoryPickerRunner{err: runnerErr}

		path, err := NewDirectoryPicker(runner, "windows").SelectDirectory(ctx)
		if path != "" {
			t.Errorf("SelectDirectory() path = %q, want empty", path)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SelectDirectory() error = %v, want wrapped context.Canceled", err)
		}
		if errors.Is(err, ErrDirectorySelectionCanceled) {
			t.Errorf("SelectDirectory() error = %v, do not want selection cancellation", err)
		}
	})

	t.Run("does not treat marker from failed process as selection cancellation", func(t *testing.T) {
		commandErr := errors.New("process failed")
		runner := &recordingDirectoryPickerRunner{
			result: CommandResult{Output: []byte(windowsPickerCancelMarker), ExitCode: 1},
			err:    commandErr,
		}

		_, err := NewDirectoryPicker(runner, "windows").SelectDirectory(context.Background())
		if !errors.Is(err, commandErr) {
			t.Fatalf("SelectDirectory() error = %v, want wrapped %v", err, commandErr)
		}
		if errors.Is(err, ErrDirectorySelectionCanceled) {
			t.Errorf("SelectDirectory() error = %v, do not want cancellation sentinel", err)
		}
	})
}

func TestDirectoryPickerSelectDirectoryUnsupported(t *testing.T) {
	runner := &recordingDirectoryPickerRunner{}
	picker := NewDirectoryPicker(runner, "plan9")

	path, err := picker.SelectDirectory(context.Background())
	if path != "" {
		t.Errorf("SelectDirectory() path = %q, want empty", path)
	}
	if !errors.Is(err, ErrDirectoryPickerUnavailable) {
		t.Fatalf("SelectDirectory() error = %v, want ErrDirectoryPickerUnavailable", err)
	}
	if runner.calls != 0 {
		t.Errorf("Run calls = %d, want 0", runner.calls)
	}
}

func TestDirectoryPickerSelectDirectoryNilRunner(t *testing.T) {
	picker := NewDirectoryPicker(nil, "windows")

	path, err := picker.SelectDirectory(context.Background())
	if path != "" {
		t.Errorf("SelectDirectory() path = %q, want empty", path)
	}
	if !errors.Is(err, ErrDirectoryPickerUnavailable) {
		t.Fatalf("SelectDirectory() error = %v, want ErrDirectoryPickerUnavailable", err)
	}
}

func TestWindowsPickerScript(t *testing.T) {
	required := []string{
		"[Console]::OutputEncoding",
		"UTF8",
		"System.Windows.Forms",
		"FolderBrowserDialog",
		"SelectedPath",
		"DialogResult]::OK",
		windowsPickerCancelMarker,
	}
	for _, fragment := range required {
		if !strings.Contains(windowsPickerScript, fragment) {
			t.Errorf("windowsPickerScript missing %q", fragment)
		}
	}
}

func TestExecCommandRunner(t *testing.T) {
	t.Run("captures combined output and exit code", func(t *testing.T) {
		result, err := (ExecCommandRunner{}).Run(
			context.Background(),
			os.Args[0],
			"-test.run=TestExecCommandRunnerHelperProcess",
			"--",
			"exit",
		)
		if err == nil {
			t.Fatal("Run() error = nil, want exit error")
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("Run() error = %T %v, want *exec.ExitError", err, err)
		}
		if result.ExitCode != 7 {
			t.Errorf("Run() ExitCode = %d, want 7", result.ExitCode)
		}
		output := string(result.Output)
		if !strings.Contains(output, "stdout diagnostic") || !strings.Contains(output, "stderr diagnostic") {
			t.Errorf("Run() Output = %q, want stdout and stderr diagnostics", output)
		}
	})

	t.Run("preserves canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := (ExecCommandRunner{}).Run(ctx, os.Args[0], "-test.run=TestExecCommandRunnerHelperProcess", "--", "success")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	})
}

func TestExecCommandRunnerHelperProcess(t *testing.T) {
	args := os.Args
	separator := slices.Index(args, "--")
	if separator == -1 || separator+1 >= len(args) {
		return
	}
	switch args[separator+1] {
	case "exit":
		_, _ = os.Stdout.WriteString("stdout diagnostic\n")
		_, _ = os.Stderr.WriteString("stderr diagnostic\n")
		os.Exit(7)
	case "success":
		return
	}
}
