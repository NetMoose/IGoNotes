package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type recordingDirectoryPickerRunner struct {
	result        CommandResult
	err           error
	responses     []directoryPickerResponse
	calls         int
	name          string
	args          []string
	recordedCalls []directoryPickerCall
}

func (r *recordingDirectoryPickerRunner) Run(_ context.Context, name string, args ...string) (CommandResult, error) {
	callIndex := r.calls
	r.calls++
	r.name = name
	r.args = append([]string(nil), args...)
	r.recordedCalls = append(r.recordedCalls, directoryPickerCall{name: name, args: append([]string(nil), args...)})
	if callIndex < len(r.responses) {
		response := r.responses[callIndex]
		return response.result, response.err
	}
	return r.result, r.err
}

type directoryPickerResponse struct {
	result CommandResult
	err    error
}

type directoryPickerCall struct {
	name string
	args []string
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
		runner := &recordingDirectoryPickerRunner{result: CommandResult{Output: []byte(directoryPickerCancelMarker + "\r\n")}}

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
		runner := &recordingDirectoryPickerRunner{result: CommandResult{ExitCode: -1}, err: exec.ErrNotFound}

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

	t.Run("wraps launch failure without an exit code", func(t *testing.T) {
		commandErr := errors.New("permission denied")
		runner := &recordingDirectoryPickerRunner{
			result: CommandResult{ExitCode: -1},
			err:    commandErr,
		}

		path, err := NewDirectoryPicker(runner, "windows").SelectDirectory(context.Background())
		if path != "" {
			t.Errorf("SelectDirectory() path = %q, want empty", path)
		}
		if !errors.Is(err, commandErr) {
			t.Fatalf("SelectDirectory() error = %v, want wrapped %v", err, commandErr)
		}
		if strings.Contains(err.Error(), "exit code") {
			t.Errorf("SelectDirectory() error = %q, do not want unavailable exit code", err)
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
		if !strings.Contains(err.Error(), "exit code 9") {
			t.Errorf("SelectDirectory() error = %q, want real exit code", err)
		}
		if errors.Is(err, ErrDirectorySelectionCanceled) {
			t.Errorf("SelectDirectory() error = %v, do not want cancellation sentinel", err)
		}
	})

	t.Run("wraps canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runnerErr := errors.New("runner stopped")
		runner := &recordingDirectoryPickerRunner{result: CommandResult{ExitCode: -1}, err: runnerErr}

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
		if runner.calls != 0 {
			t.Errorf("Run calls = %d, want 0 for pre-canceled context", runner.calls)
		}
	})

	t.Run("does not treat marker from failed process as selection cancellation", func(t *testing.T) {
		commandErr := errors.New("process failed")
		runner := &recordingDirectoryPickerRunner{
			result: CommandResult{Output: []byte(directoryPickerCancelMarker), ExitCode: 1},
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
	for _, goos := range []string{"windows", "darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			picker := NewDirectoryPicker(nil, goos)

			path, err := picker.SelectDirectory(context.Background())
			if path != "" {
				t.Errorf("SelectDirectory() path = %q, want empty", path)
			}
			if !errors.Is(err, ErrDirectoryPickerUnavailable) {
				t.Fatalf("SelectDirectory() error = %v, want ErrDirectoryPickerUnavailable", err)
			}
		})
	}
}

func TestWindowsPickerScript(t *testing.T) {
	const stopOnError = "$ErrorActionPreference = 'Stop'"
	if !strings.HasPrefix(windowsPickerScript, stopOnError+"\n") {
		t.Errorf("windowsPickerScript = %q, want %q as first statement", windowsPickerScript, stopOnError)
	}

	required := []string{
		"[Console]::OutputEncoding",
		"UTF8",
		"System.Windows.Forms",
		"FolderBrowserDialog",
		"SelectedPath",
		"DialogResult]::OK",
		directoryPickerCancelMarker,
	}
	for _, fragment := range required {
		if !strings.Contains(windowsPickerScript, fragment) {
			t.Errorf("windowsPickerScript missing %q", fragment)
		}
	}
	if got := strings.Count(windowsPickerScript, directoryPickerCancelMarker); got != 1 {
		t.Errorf("windowsPickerScript cancel marker count = %d, want 1", got)
	}
}

const wantMacOSPickerScript = `try
    return POSIX path of (choose folder with prompt "Choose a notes directory")
on error errorMessage number errorNumber
    if errorNumber is -128 then
        return "__IGONOTES_DIRECTORY_SELECTION_CANCELED__"
    end if
    error errorMessage number errorNumber
end try`

func TestDirectoryPickerSelectDirectoryDarwin(t *testing.T) {
	t.Run("returns selected POSIX path and preserves spaces and trailing slash", func(t *testing.T) {
		output := []byte(" /Volumes/Notes / \r\n")
		originalOutput := slices.Clone(output)
		runner := &recordingDirectoryPickerRunner{result: CommandResult{Output: output}}

		got, err := NewDirectoryPicker(runner, "darwin").SelectDirectory(context.Background())
		if err != nil {
			t.Fatalf("SelectDirectory() error = %v, want nil", err)
		}
		if want := " /Volumes/Notes / "; got != want {
			t.Errorf("SelectDirectory() = %q, want %q", got, want)
		}
		wantCall := directoryPickerCall{name: "osascript", args: []string{"-e", wantMacOSPickerScript}}
		if !reflect.DeepEqual(runner.recordedCalls, []directoryPickerCall{wantCall}) {
			t.Errorf("Run calls = %#v, want %#v", runner.recordedCalls, []directoryPickerCall{wantCall})
		}
		if !slices.Equal(output, originalOutput) {
			t.Errorf("runner output mutated to %q, want %q", output, originalOutput)
		}
	})

	t.Run("maps exact script marker to canceled", func(t *testing.T) {
		runner := &recordingDirectoryPickerRunner{result: CommandResult{Output: []byte(directoryPickerCancelMarker + "\n")}}

		path, err := NewDirectoryPicker(runner, "darwin").SelectDirectory(context.Background())
		if path != "" {
			t.Errorf("SelectDirectory() path = %q, want empty", path)
		}
		if !errors.Is(err, ErrDirectorySelectionCanceled) {
			t.Fatalf("SelectDirectory() error = %v, want ErrDirectorySelectionCanceled", err)
		}
	})

	t.Run("keeps osascript exit one operational", func(t *testing.T) {
		commandErr := errors.New("exit status 1")
		runner := &recordingDirectoryPickerRunner{
			result: CommandResult{Output: []byte("execution error: denied\n"), ExitCode: 1},
			err:    commandErr,
		}

		path, err := NewDirectoryPicker(runner, "darwin").SelectDirectory(context.Background())
		if path != "" {
			t.Errorf("SelectDirectory() path = %q, want empty", path)
		}
		if !errors.Is(err, commandErr) {
			t.Fatalf("SelectDirectory() error = %v, want wrapped %v", err, commandErr)
		}
		if errors.Is(err, ErrDirectorySelectionCanceled) {
			t.Errorf("SelectDirectory() error = %v, do not want cancellation", err)
		}
		if !strings.Contains(err.Error(), "exit code 1") {
			t.Errorf("SelectDirectory() error = %q, want exit code", err)
		}
	})
}

func TestMacOSPickerScript(t *testing.T) {
	if macOSPickerScript != wantMacOSPickerScript {
		t.Errorf("macOSPickerScript = %q, want %q", macOSPickerScript, wantMacOSPickerScript)
	}
	if strings.Count(macOSPickerScript, directoryPickerCancelMarker) != 1 {
		t.Errorf("macOSPickerScript must contain the fixed cancel marker exactly once")
	}
}

func TestDirectoryPickerSelectDirectoryLinux(t *testing.T) {
	zenityCall := directoryPickerCall{name: "zenity", args: []string{"--file-selection", "--directory"}}
	kdialogCall := directoryPickerCall{name: "kdialog", args: []string{"--getexistingdirectory"}}

	t.Run("returns zenity selection and preserves spaces", func(t *testing.T) {
		runner := &recordingDirectoryPickerRunner{result: CommandResult{Output: []byte(" /home/alice/My Notes \r\n")}}

		got, err := NewDirectoryPicker(runner, "linux").SelectDirectory(context.Background())
		if err != nil {
			t.Fatalf("SelectDirectory() error = %v, want nil", err)
		}
		if want := " /home/alice/My Notes "; got != want {
			t.Errorf("SelectDirectory() = %q, want %q", got, want)
		}
		if !reflect.DeepEqual(runner.recordedCalls, []directoryPickerCall{zenityCall}) {
			t.Errorf("Run calls = %#v, want %#v", runner.recordedCalls, []directoryPickerCall{zenityCall})
		}
	})

	t.Run("falls back to kdialog only when zenity is missing", func(t *testing.T) {
		runner := &recordingDirectoryPickerRunner{responses: []directoryPickerResponse{
			{result: CommandResult{ExitCode: -1}, err: fmt.Errorf("find zenity: %w", exec.ErrNotFound)},
			{result: CommandResult{Output: []byte("/notes/from-kdialog\n")}},
		}}

		got, err := NewDirectoryPicker(runner, "linux").SelectDirectory(context.Background())
		if err != nil {
			t.Fatalf("SelectDirectory() error = %v, want nil", err)
		}
		if got != "/notes/from-kdialog" {
			t.Errorf("SelectDirectory() = %q, want %q", got, "/notes/from-kdialog")
		}
		wantCalls := []directoryPickerCall{zenityCall, kdialogCall}
		if !reflect.DeepEqual(runner.recordedCalls, wantCalls) {
			t.Errorf("Run calls = %#v, want %#v", runner.recordedCalls, wantCalls)
		}
	})

	t.Run("does not fall back when zenity is canceled", func(t *testing.T) {
		runner := &recordingDirectoryPickerRunner{
			result: CommandResult{ExitCode: 1},
			err:    errors.New("exit status 1"),
		}

		_, err := NewDirectoryPicker(runner, "linux").SelectDirectory(context.Background())
		if !errors.Is(err, ErrDirectorySelectionCanceled) {
			t.Fatalf("SelectDirectory() error = %v, want ErrDirectorySelectionCanceled", err)
		}
		if runner.calls != 1 {
			t.Errorf("Run calls = %d, want 1", runner.calls)
		}
	})

	t.Run("maps kdialog exit one to canceled", func(t *testing.T) {
		runner := &recordingDirectoryPickerRunner{responses: []directoryPickerResponse{
			{result: CommandResult{ExitCode: -1}, err: exec.ErrNotFound},
			{result: CommandResult{ExitCode: 1}, err: errors.New("exit status 1")},
		}}

		_, err := NewDirectoryPicker(runner, "linux").SelectDirectory(context.Background())
		if !errors.Is(err, ErrDirectorySelectionCanceled) {
			t.Fatalf("SelectDirectory() error = %v, want ErrDirectorySelectionCanceled", err)
		}
		wantCalls := []directoryPickerCall{zenityCall, kdialogCall}
		if !reflect.DeepEqual(runner.recordedCalls, wantCalls) {
			t.Errorf("Run calls = %#v, want %#v", runner.recordedCalls, wantCalls)
		}
	})

	t.Run("returns unavailable when both pickers are missing", func(t *testing.T) {
		runner := &recordingDirectoryPickerRunner{responses: []directoryPickerResponse{
			{result: CommandResult{ExitCode: -1}, err: exec.ErrNotFound},
			{result: CommandResult{ExitCode: -1}, err: exec.ErrNotFound},
		}}

		_, err := NewDirectoryPicker(runner, "linux").SelectDirectory(context.Background())
		if !errors.Is(err, ErrDirectoryPickerUnavailable) {
			t.Fatalf("SelectDirectory() error = %v, want ErrDirectoryPickerUnavailable", err)
		}
		if runner.calls != 2 {
			t.Errorf("Run calls = %d, want 2", runner.calls)
		}
	})

	t.Run("does not fall back after operational exit", func(t *testing.T) {
		commandErr := errors.New("exit status 2")
		runner := &recordingDirectoryPickerRunner{
			result: CommandResult{Output: []byte("display unavailable\n"), ExitCode: 2},
			err:    commandErr,
		}

		_, err := NewDirectoryPicker(runner, "linux").SelectDirectory(context.Background())
		if !errors.Is(err, commandErr) {
			t.Fatalf("SelectDirectory() error = %v, want wrapped %v", err, commandErr)
		}
		if errors.Is(err, ErrDirectorySelectionCanceled) || errors.Is(err, ErrDirectoryPickerUnavailable) {
			t.Errorf("SelectDirectory() error = %v, want operational error", err)
		}
		if runner.calls != 1 {
			t.Errorf("Run calls = %d, want 1", runner.calls)
		}
	})

	t.Run("does not fall back after non-missing start failure", func(t *testing.T) {
		commandErr := errors.New("permission denied")
		runner := &recordingDirectoryPickerRunner{
			result: CommandResult{ExitCode: -1},
			err:    commandErr,
		}

		_, err := NewDirectoryPicker(runner, "linux").SelectDirectory(context.Background())
		if !errors.Is(err, commandErr) {
			t.Fatalf("SelectDirectory() error = %v, want wrapped %v", err, commandErr)
		}
		if runner.calls != 1 {
			t.Errorf("Run calls = %d, want 1", runner.calls)
		}
	})

	t.Run("rejects empty successful output", func(t *testing.T) {
		runner := &recordingDirectoryPickerRunner{result: CommandResult{Output: []byte("\r\n")}}

		_, err := NewDirectoryPicker(runner, "linux").SelectDirectory(context.Background())
		if err == nil {
			t.Fatal("SelectDirectory() error = nil, want operational error")
		}
		if errors.Is(err, ErrDirectorySelectionCanceled) || errors.Is(err, ErrDirectoryPickerUnavailable) {
			t.Errorf("SelectDirectory() error = %v, want operational error", err)
		}
	})

	t.Run("does not invoke a picker for pre-canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runner := &recordingDirectoryPickerRunner{}

		_, err := NewDirectoryPicker(runner, "linux").SelectDirectory(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SelectDirectory() error = %v, want context.Canceled", err)
		}
		if runner.calls != 0 {
			t.Errorf("Run calls = %d, want 0", runner.calls)
		}
	})
}

func TestExecCommandRunner(t *testing.T) {
	t.Run("returns zero exit code on success", func(t *testing.T) {
		result, err := (ExecCommandRunner{}).Run(
			context.Background(),
			os.Args[0],
			"-test.run=TestExecCommandRunnerHelperProcess",
			"--",
			"success",
		)
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
		if result.ExitCode != 0 {
			t.Errorf("Run() ExitCode = %d, want 0", result.ExitCode)
		}
	})

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

	t.Run("uses unavailable exit code when process does not start", func(t *testing.T) {
		missingExecutable := filepath.Join(t.TempDir(), "missing-executable")

		result, err := (ExecCommandRunner{}).Run(context.Background(), missingExecutable)
		if err == nil {
			t.Fatal("Run() error = nil, want start error")
		}
		if result.ExitCode != -1 {
			t.Errorf("Run() ExitCode = %d, want -1", result.ExitCode)
		}
	})

	t.Run("preserves canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		result, err := (ExecCommandRunner{}).Run(ctx, os.Args[0], "-test.run=TestExecCommandRunnerHelperProcess", "--", "success")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
		if result.ExitCode != -1 {
			t.Errorf("Run() ExitCode = %d, want -1", result.ExitCode)
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
