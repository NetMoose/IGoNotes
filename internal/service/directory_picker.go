package service

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

var (
	ErrDirectorySelectionCanceled = errors.New("directory selection canceled")
	ErrDirectoryPickerUnavailable = errors.New("directory picker unavailable")
)

type CommandResult struct {
	Output   []byte
	ExitCode int
}

type CommandRunner interface {
	Run(context.Context, string, ...string) (CommandResult, error)
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, name string, args ...string) (CommandResult, error) {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	result := CommandResult{Output: output}
	if err == nil {
		return result, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	return result, err
}

type DirectoryPicker struct {
	runner CommandRunner
	goos   string
}

func NewDirectoryPicker(runner CommandRunner, goos string) *DirectoryPicker {
	return &DirectoryPicker{runner: runner, goos: goos}
}

const windowsPickerCancelMarker = "__IGONOTES_DIRECTORY_SELECTION_CANCELED__"

const windowsPickerScript = `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
    [Console]::Out.Write($dialog.SelectedPath)
} else {
    [Console]::Out.Write('__IGONOTES_DIRECTORY_SELECTION_CANCELED__')
}`

func (p *DirectoryPicker) SelectDirectory(ctx context.Context) (string, error) {
	if p.goos != "windows" {
		return "", fmt.Errorf("directory picker is not supported on %s: %w", p.goos, ErrDirectoryPickerUnavailable)
	}
	if p.runner == nil {
		return "", fmt.Errorf("directory picker command runner is nil: %w", ErrDirectoryPickerUnavailable)
	}

	result, err := p.runner.Run(
		ctx,
		"powershell.exe",
		"-NoProfile",
		"-NonInteractive",
		"-STA",
		"-Command",
		windowsPickerScript,
	)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("run Windows directory picker: %w", ctxErr)
		}
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("run Windows directory picker: %w: %w", ErrDirectoryPickerUnavailable, err)
		}

		diagnostic := strings.TrimRight(string(result.Output), "\r\n")
		if diagnostic != "" {
			return "", fmt.Errorf("run Windows directory picker (exit code %d): %w: %s", result.ExitCode, err, diagnostic)
		}
		return "", fmt.Errorf("run Windows directory picker (exit code %d): %w", result.ExitCode, err)
	}

	path := strings.TrimRight(string(result.Output), "\r\n")
	if path == windowsPickerCancelMarker {
		return "", ErrDirectorySelectionCanceled
	}
	if path == "" {
		return "", errors.New("Windows directory picker returned an empty path")
	}
	return path, nil
}
