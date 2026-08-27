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
	result := CommandResult{Output: output, ExitCode: -1}
	if err == nil {
		result.ExitCode = 0
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

const directoryPickerCancelMarker = "__IGONOTES_DIRECTORY_SELECTION_CANCELED__"

const windowsPickerScript = `$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
    [Console]::Out.Write($dialog.SelectedPath)
} else {
    [Console]::Out.Write('` + directoryPickerCancelMarker + `')
}`

const macOSPickerScript = `try
    return POSIX path of (choose folder with prompt "Choose a notes directory")
on error errorMessage number errorNumber
    if errorNumber is -128 then
        return "` + directoryPickerCancelMarker + `"
    end if
    error errorMessage number errorNumber
end try`

func (p *DirectoryPicker) SelectDirectory(ctx context.Context) (string, error) {
	switch p.goos {
	case "windows", "darwin", "linux":
	default:
		return "", fmt.Errorf("directory picker is not supported on %s: %w", p.goos, ErrDirectoryPickerUnavailable)
	}
	if p.runner == nil {
		return "", fmt.Errorf("directory picker command runner is nil: %w", ErrDirectoryPickerUnavailable)
	}

	switch p.goos {
	case "windows":
		path, _, err := p.runFixedCommand(
			ctx,
			"run Windows directory picker",
			"powershell.exe",
			"-NoProfile",
			"-NonInteractive",
			"-STA",
			"-Command",
			windowsPickerScript,
		)
		return path, err
	case "darwin":
		path, _, err := p.runFixedCommand(
			ctx,
			"run macOS directory picker",
			"osascript",
			"-e",
			macOSPickerScript,
		)
		return path, err
	default:
		return p.selectLinuxDirectory(ctx)
	}
}

func (p *DirectoryPicker) selectLinuxDirectory(ctx context.Context) (string, error) {
	path, result, err := p.runFixedCommand(
		ctx,
		"run zenity directory picker",
		"zenity",
		"--file-selection",
		"--directory",
	)
	if err == nil {
		return path, nil
	}
	if ctx.Err() != nil {
		return "", err
	}
	if result.ExitCode == 1 {
		return "", ErrDirectorySelectionCanceled
	}
	if !errors.Is(err, ErrDirectoryPickerUnavailable) && !errors.Is(err, exec.ErrNotFound) {
		return "", err
	}

	path, result, err = p.runFixedCommand(
		ctx,
		"run kdialog directory picker",
		"kdialog",
		"--getexistingdirectory",
	)
	if err != nil && result.ExitCode == 1 {
		return "", ErrDirectorySelectionCanceled
	}
	return path, err
}

func (p *DirectoryPicker) runFixedCommand(
	ctx context.Context,
	operation string,
	name string,
	args ...string,
) (string, CommandResult, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", CommandResult{ExitCode: -1}, fmt.Errorf("%s: %w", operation, ctxErr)
	}

	result, err := p.runner.Run(
		ctx,
		name,
		args...,
	)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", result, fmt.Errorf("%s: %w", operation, ctxErr)
		}
		if errors.Is(err, exec.ErrNotFound) {
			return "", result, fmt.Errorf("%s: %w: %w", operation, ErrDirectoryPickerUnavailable, err)
		}

		diagnostic := strings.TrimRight(string(result.Output), "\r\n")
		if result.ExitCode >= 0 {
			operation = fmt.Sprintf("%s (exit code %d)", operation, result.ExitCode)
		}
		if diagnostic != "" {
			return "", result, fmt.Errorf("%s: %w: %s", operation, err, diagnostic)
		}
		return "", result, fmt.Errorf("%s: %w", operation, err)
	}

	path := strings.TrimRight(string(result.Output), "\r\n")
	if path == directoryPickerCancelMarker {
		return "", result, ErrDirectorySelectionCanceled
	}
	if path == "" {
		return "", result, fmt.Errorf("%s returned an empty path", operation)
	}
	return path, result, nil
}
