# Native Directory Picker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Добавить тестируемый нативный выбор каталога и доступный до завершения первоначальной настройки маршрут `POST /api/system/select-directory` с единообразными JSON-ошибками.

**Architecture:** Новый `service.DirectoryPicker` получает внедрённые `CommandRunner` и строку GOOS, запускает только фиксированные executable/arguments без shell и переводит платформенные результаты в выбранный путь либо две sentinel-ошибки: отмена и недоступность. Новый `handlers.SystemHandler` преобразует эти результаты в `200`, `204`, `501` или `500`, а маленькая функция пакета `main` регистрирует маршрут на `*http.ServeMux`, уже возвращённом backend-функцией `handlers.NewRouter`, и поэтому проверяется без запуска сервера.

**Tech Stack:** Go 1.26, стандартные пакеты `context`, `errors`, `os/exec`, `net/http`, `net/http/httptest`, PowerShell `System.Windows.Forms.FolderBrowserDialog`, macOS `osascript`, Linux `zenity` и `kdialog`.

---

## Границы плана

В этот план входят только command runner, platform picker, HTTP handler, регистрация маршрута и backend-проверки picker. В него не входят `SettingsService`, CRUD баз, миграция `setup_completed`, guard note API, live-переключение `NoteService`, frontend API-модуль и Svelte-компоненты.

`POST /api/system/select-directory` намеренно не подключается к setup guard: согласно спецификации маршрут доступен и до завершения мастера.

## Обязательная зависимость от backend-плана

До Task 3 должен быть выполнен backend-план setup/settings, экспортирующий из пакета `handlers` ровно такой helper:

```go
func WriteAPIError(w http.ResponseWriter, status int, code, message, field string)
```

Контракт helper:

- устанавливает `Content-Type: application/json`;
- записывает переданный HTTP status;
- кодирует один JSON-объект с обязательными строками `code` и `message`;
- добавляет `field` только когда аргумент `field` непустой;
- не вызывает `http.Error`, чтобы тело всегда оставалось структурированным JSON.

Этот план **не создаёт и не переопределяет** `WriteAPIError` или тип общей API-ошибки. Все вызовы ниже используют указанный exported contract, чтобы не получить второй несовместимый формат ошибок. Если dependency ещё не применена, Task 3 блокируется до её выполнения; platform service из Tasks 1-2 при этом можно реализовать независимо.

## Структура файлов

- Create: `internal/service/directory_picker.go` - контракты runner/picker, реальный `exec.CommandContext`, фиксированные платформенные команды и классификация исходов.
- Create: `internal/service/directory_picker_test.go` - fake runner, GOOS injection, точные executable/arguments, выбор, отмена, fallback, unavailable и operational errors.
- Create: `internal/handlers/system_handler.go` - HTTP mapping результата picker в REST-контракт.
- Create: `internal/handlers/system_handler_test.go` - тесты метода, статусов, JSON и пустого тела при отмене.
- Create: `cmd/api/system_routes.go` - регистрация только `/api/system/select-directory` на переданном mux.
- Create: `cmd/api/system_routes_test.go` - integration-тест регистрации точного пути.
- Modify: `cmd/api/main.go` - создание production runner/picker/handler и регистрация route на существующем `router` из `handlers.NewRouter` без изменения остальных API.

### Task 1: Контракты runner и Windows picker

**Files:**
- Create: `internal/service/directory_picker.go`
- Create: `internal/service/directory_picker_test.go`

- [ ] **Step 1: Написать RED-тесты успешного выбора и отмены в Windows**

Создать `internal/service/directory_picker_test.go`:

```go
package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type commandCall struct {
	name string
	args []string
}

type fakeCommandResponse struct {
	result CommandResult
	err    error
}

type fakeCommandRunner struct {
	responses []fakeCommandResponse
	calls     []commandCall
}

func (f *fakeCommandRunner) Run(_ context.Context, name string, args ...string) (CommandResult, error) {
	f.calls = append(f.calls, commandCall{name: name, args: append([]string(nil), args...)})
	if len(f.responses) == 0 {
		panic("fakeCommandRunner: unexpected command")
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response.result, response.err
}

func TestDirectoryPickerWindows(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		wantPath   string
		wantCancel bool
	}{
		{name: "selected", output: "C:\\Users\\alice\\Notes\r\n", wantPath: "C:\\Users\\alice\\Notes"},
		{name: "cancelled", output: directoryPickerCancelMarker + "\r\n", wantCancel: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeCommandRunner{responses: []fakeCommandResponse{{
				result: CommandResult{Output: []byte(tt.output)},
			}}}
			picker := NewDirectoryPicker(runner, "windows")

			got, err := picker.SelectDirectory(context.Background())
			if tt.wantCancel {
				if !errors.Is(err, ErrDirectorySelectionCanceled) {
					t.Fatalf("SelectDirectory() error = %v, want ErrDirectorySelectionCanceled", err)
				}
				if got != "" {
					t.Errorf("SelectDirectory() path = %q, want empty", got)
				}
			} else {
				if err != nil {
					t.Fatalf("SelectDirectory() error = %v, want nil", err)
				}
				if got != tt.wantPath {
					t.Errorf("SelectDirectory() path = %q, want %q", got, tt.wantPath)
				}
			}

			wantCalls := []commandCall{{
				name: "powershell.exe",
				args: []string{"-NoProfile", "-NonInteractive", "-STA", "-Command", windowsPickerScript},
			}}
			if !reflect.DeepEqual(runner.calls, wantCalls) {
				t.Errorf("runner calls = %#v, want %#v", runner.calls, wantCalls)
			}
		})
	}
}
```

Тест находится в пакете `service`, а не `service_test`, чтобы проверять неизменяемые script constants и при этом не экспортировать детали команд.

- [ ] **Step 2: Запустить Windows RED-тест и подтвердить ожидаемое падение**

Run: `go test ./internal/service -run TestDirectoryPickerWindows -v`

Expected: FAIL на компиляции с `undefined: CommandResult`, `undefined: NewDirectoryPicker` и `undefined: ErrDirectorySelectionCanceled`.

- [ ] **Step 3: Реализовать минимальный runner и Windows picker**

Создать `internal/service/directory_picker.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const directoryPickerCancelMarker = "__IGONOTES_DIRECTORY_PICKER_CANCELLED__"

const windowsPickerScript = `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
    [Console]::WriteLine($dialog.SelectedPath)
} else {
    [Console]::WriteLine("__IGONOTES_DIRECTORY_PICKER_CANCELLED__")
}`

var (
	ErrDirectorySelectionCanceled = errors.New("directory selection canceled")
	ErrDirectoryPickerUnavailable = errors.New("directory picker unavailable")
)

type CommandResult struct {
	Output   []byte
	ExitCode int
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (CommandResult, error)
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, name string, args ...string) (CommandResult, error) {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	result := CommandResult{Output: output}
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

func (p *DirectoryPicker) SelectDirectory(ctx context.Context) (string, error) {
	switch p.goos {
	case "windows":
		return p.runFixedCommand(
			ctx,
			"powershell.exe",
			[]string{"-NoProfile", "-NonInteractive", "-STA", "-Command", windowsPickerScript},
			false,
		)
	default:
		return "", ErrDirectoryPickerUnavailable
	}
}

func (p *DirectoryPicker) runFixedCommand(
	ctx context.Context,
	name string,
	args []string,
	exitOneCancels bool,
) (string, error) {
	result, err := p.runner.Run(ctx, name, args...)
	if errors.Is(err, exec.ErrNotFound) {
		return "", ErrDirectoryPickerUnavailable
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("directory picker %s interrupted: %w", name, ctx.Err())
		}
		if exitOneCancels && result.ExitCode == 1 {
			return "", ErrDirectorySelectionCanceled
		}
		detail := strings.TrimSpace(string(result.Output))
		if detail == "" {
			return "", fmt.Errorf("directory picker %s failed: %w", name, err)
		}
		return "", fmt.Errorf("directory picker %s failed: %w: %s", name, err, detail)
	}

	path := strings.TrimRight(string(result.Output), "\r\n")
	if path == directoryPickerCancelMarker {
		return "", ErrDirectorySelectionCanceled
	}
	if path == "" {
		return "", fmt.Errorf("directory picker %s returned an empty path", name)
	}
	return path, nil
}
```

`exec.CommandContext` получает executable и каждый argument отдельно. В коде нет `cmd /c`, `sh -c`, конкатенации пользовательской строки или подстановки выбранного пути в script. `TrimRight(..., "\r\n")` удаляет только перевод строки процесса и сохраняет допустимые пробелы в имени каталога.

- [ ] **Step 4: Отформатировать Windows slice**

Run: `gofmt -w internal/service/directory_picker.go internal/service/directory_picker_test.go`

- [ ] **Step 5: Выполнить GREEN-тест Windows**

Run: `go test ./internal/service -run TestDirectoryPickerWindows -v`

Expected: PASS для `selected` и `cancelled`; fake фиксирует один вызов `powershell.exe` с пятью неизменяемыми arguments.

- [ ] **Step 6: Зафиксировать Windows slice**

```bash
git add internal/service/directory_picker.go internal/service/directory_picker_test.go
git commit -m "feat: add injectable Windows directory picker"
```

### Task 2: macOS, Linux fallback и полная классификация ошибок

**Files:**
- Modify: `internal/service/directory_picker.go`
- Modify: `internal/service/directory_picker_test.go`

- [ ] **Step 1: Добавить RED-тесты фиксированных macOS/Linux команд**

Добавить imports `"os/exec"` в `internal/service/directory_picker_test.go`, затем добавить тесты:

```go
func TestDirectoryPickerDarwin(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		wantPath   string
		wantCancel bool
	}{
		{name: "selected", output: "/Users/alice/Notes/\n", wantPath: "/Users/alice/Notes/"},
		{name: "cancelled", output: directoryPickerCancelMarker + "\n", wantCancel: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeCommandRunner{responses: []fakeCommandResponse{{
				result: CommandResult{Output: []byte(tt.output)},
			}}}
			picker := NewDirectoryPicker(runner, "darwin")

			got, err := picker.SelectDirectory(context.Background())
			if tt.wantCancel {
				if !errors.Is(err, ErrDirectorySelectionCanceled) {
					t.Fatalf("SelectDirectory() error = %v, want ErrDirectorySelectionCanceled", err)
				}
			} else {
				if err != nil {
					t.Fatalf("SelectDirectory() error = %v, want nil", err)
				}
				if got != tt.wantPath {
					t.Errorf("SelectDirectory() path = %q, want %q", got, tt.wantPath)
				}
			}

			wantCalls := []commandCall{{name: "osascript", args: []string{"-e", macOSPickerScript}}}
			if !reflect.DeepEqual(runner.calls, wantCalls) {
				t.Errorf("runner calls = %#v, want %#v", runner.calls, wantCalls)
			}
		})
	}
}

func TestDirectoryPickerLinuxSelectionAndFallback(t *testing.T) {
	tests := []struct {
		name      string
		responses []fakeCommandResponse
		wantPath  string
		wantCalls []commandCall
	}{
		{
			name:      "zenity selected",
			responses: []fakeCommandResponse{{result: CommandResult{Output: []byte("/home/alice/Notes\n")}}},
			wantPath:  "/home/alice/Notes",
			wantCalls: []commandCall{{name: "zenity", args: []string{"--file-selection", "--directory"}}},
		},
		{
			name: "missing zenity falls back to kdialog",
			responses: []fakeCommandResponse{
				{err: exec.ErrNotFound},
				{result: CommandResult{Output: []byte("/home/alice/Notes\n")}},
			},
			wantPath: "/home/alice/Notes",
			wantCalls: []commandCall{
				{name: "zenity", args: []string{"--file-selection", "--directory"}},
				{name: "kdialog", args: []string{"--getexistingdirectory"}},
			},
		},
		{
			name: "empty selected path is an operational error",
			responses: []fakeCommandResponse{{result: CommandResult{Output: []byte("\n")}}},
			wantCalls: []commandCall{{name: "zenity", args: []string{"--file-selection", "--directory"}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeCommandRunner{responses: tt.responses}
			got, err := NewDirectoryPicker(runner, "linux").SelectDirectory(context.Background())
			if tt.wantPath == "" {
				if err == nil || errors.Is(err, ErrDirectorySelectionCanceled) || errors.Is(err, ErrDirectoryPickerUnavailable) {
					t.Fatalf("SelectDirectory() error = %v, want operational error", err)
				}
			} else {
				if err != nil {
					t.Fatalf("SelectDirectory() error = %v, want nil", err)
				}
				if got != tt.wantPath {
					t.Errorf("SelectDirectory() path = %q, want %q", got, tt.wantPath)
				}
			}
			if !reflect.DeepEqual(runner.calls, tt.wantCalls) {
				t.Errorf("runner calls = %#v, want %#v", runner.calls, tt.wantCalls)
			}
		})
	}
}

func TestDirectoryPickerClassifiesCancelUnavailableAndFailure(t *testing.T) {
	commandErr := errors.New("process failed")
	tests := []struct {
		name      string
		goos      string
		responses []fakeCommandResponse
		wantErr   error
		wantCause error
		wantCalls int
	}{
		{
			name:      "zenity cancel does not open kdialog",
			goos:      "linux",
			responses: []fakeCommandResponse{{result: CommandResult{ExitCode: 1}, err: commandErr}},
			wantErr:   ErrDirectorySelectionCanceled,
			wantCalls: 1,
		},
		{
			name: "kdialog cancel after unavailable zenity",
			goos: "linux",
			responses: []fakeCommandResponse{
				{err: exec.ErrNotFound},
				{result: CommandResult{ExitCode: 1}, err: commandErr},
			},
			wantErr:   ErrDirectorySelectionCanceled,
			wantCalls: 2,
		},
		{
			name: "both Linux programs unavailable",
			goos: "linux",
			responses: []fakeCommandResponse{
				{err: exec.ErrNotFound},
				{err: exec.ErrNotFound},
			},
			wantErr:   ErrDirectoryPickerUnavailable,
			wantCalls: 2,
		},
		{
			name:      "zenity operational failure does not open kdialog",
			goos:      "linux",
			responses: []fakeCommandResponse{{result: CommandResult{Output: []byte("GTK failure"), ExitCode: 2}, err: commandErr}},
			wantCause: commandErr,
			wantCalls: 1,
		},
		{
			name:      "PowerShell unavailable",
			goos:      "windows",
			responses: []fakeCommandResponse{{err: exec.ErrNotFound}},
			wantErr:   ErrDirectoryPickerUnavailable,
			wantCalls: 1,
		},
		{
			name:      "osascript operational failure",
			goos:      "darwin",
			responses: []fakeCommandResponse{{result: CommandResult{Output: []byte("AppleScript error"), ExitCode: 1}, err: commandErr}},
			wantCause: commandErr,
			wantCalls: 1,
		},
		{
			name:      "unsupported OS",
			goos:      "freebsd",
			wantErr:   ErrDirectoryPickerUnavailable,
			wantCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeCommandRunner{responses: tt.responses}
			path, err := NewDirectoryPicker(runner, tt.goos).SelectDirectory(context.Background())
			if path != "" {
				t.Errorf("SelectDirectory() path = %q, want empty", path)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("SelectDirectory() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantCause != nil && !errors.Is(err, tt.wantCause) {
				t.Errorf("SelectDirectory() error = %v, want wrapped cause %v", err, tt.wantCause)
			}
			if len(runner.calls) != tt.wantCalls {
				t.Errorf("runner call count = %d, want %d", len(runner.calls), tt.wantCalls)
			}
		})
	}
}
```

- [ ] **Step 2: Запустить platform RED-тесты и подтвердить ожидаемые падения**

Run: `go test ./internal/service -run 'TestDirectoryPicker(Darwin|LinuxSelectionAndFallback|ClassifiesCancelUnavailableAndFailure)' -v`

Expected: FAIL: macOS и Linux сейчас возвращают `ErrDirectoryPickerUnavailable`, поэтому selected-сценарии не получают путь, а Linux fallback не вызывает `kdialog`.

- [ ] **Step 3: Добавить фиксированный AppleScript и платформенную маршрутизацию**

Добавить рядом с `windowsPickerScript`:

```go
const macOSPickerScript = `try
    return POSIX path of (choose folder with prompt "Choose a notes directory")
on error number -128
    return "__IGONOTES_DIRECTORY_PICKER_CANCELLED__"
end try`
```

Заменить `SelectDirectory` полной реализацией:

```go
func (p *DirectoryPicker) SelectDirectory(ctx context.Context) (string, error) {
	switch p.goos {
	case "windows":
		return p.runFixedCommand(
			ctx,
			"powershell.exe",
			[]string{"-NoProfile", "-NonInteractive", "-STA", "-Command", windowsPickerScript},
			false,
		)
	case "darwin":
		return p.runFixedCommand(ctx, "osascript", []string{"-e", macOSPickerScript}, false)
	case "linux":
		path, err := p.runFixedCommand(ctx, "zenity", []string{"--file-selection", "--directory"}, true)
		if !errors.Is(err, ErrDirectoryPickerUnavailable) {
			return path, err
		}
		return p.runFixedCommand(ctx, "kdialog", []string{"--getexistingdirectory"}, true)
	default:
		return "", ErrDirectoryPickerUnavailable
	}
}
```

Fallback выполняется только при `exec.ErrNotFound`. Отмена `zenity` завершает запрос с `204`, а operational error `zenity` идёт в `500`; оба исхода не открывают второй диалог. AppleScript и PowerShell сами преобразуют нативную отмену в fixed marker, поэтому exit code 1 у этих программ остаётся настоящей ошибкой исполнения.

- [ ] **Step 4: Отформатировать полный picker service**

Run: `gofmt -w internal/service/directory_picker.go internal/service/directory_picker_test.go`

- [ ] **Step 5: Выполнить GREEN для всего picker service**

Run: `go test ./internal/service -run TestDirectoryPicker -v`

Expected: PASS для Windows, macOS, обоих Linux picker, fallback, отмены, недоступности, operational errors и unsupported GOOS.

- [ ] **Step 6: Зафиксировать полный platform picker**

```bash
git add internal/service/directory_picker.go internal/service/directory_picker_test.go
git commit -m "feat: support native directory pickers"
```

### Task 3: HTTP handler и structured JSON errors

**Files:**
- Create: `internal/handlers/system_handler.go`
- Create: `internal/handlers/system_handler_test.go`
- Dependency, do not modify here: backend-планом созданный файл с `handlers.WriteAPIError`

- [ ] **Step 1: Проверить точный prerequisite общего error helper**

Run: `git grep -n 'func WriteAPIError(w http.ResponseWriter, status int, code, message, field string)' -- internal/handlers`

Expected: одна exported declaration из backend-плана. Если вывода нет, не добавлять локальный helper и не продолжать Task 3: сначала выполнить backend-план setup/settings, предоставляющий контракт из раздела «Обязательная зависимость».

- [ ] **Step 2: Написать RED-тесты REST mapping**

Создать `internal/handlers/system_handler_test.go`:

```go
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"IGoNotes/internal/service"
)

type fakeDirectorySelector struct {
	path  string
	err   error
	calls int
}

func (f *fakeDirectorySelector) SelectDirectory(context.Context) (string, error) {
	f.calls++
	return f.path, f.err
}

func TestSystemHandlerSelectDirectory(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		err         error
		wantStatus  int
		wantPath    string
		wantCode    string
		wantMessage string
	}{
		{
			name:       "selected",
			path:       "/home/alice/Notes",
			wantStatus: http.StatusOK,
			wantPath:   "/home/alice/Notes",
		},
		{
			name:       "cancelled",
			err:        service.ErrDirectorySelectionCanceled,
			wantStatus: http.StatusNoContent,
		},
		{
			name:        "unavailable",
			err:         service.ErrDirectoryPickerUnavailable,
			wantStatus:  http.StatusNotImplemented,
			wantCode:    "directory_picker_unavailable",
			wantMessage: "Системный выбор каталога недоступен",
		},
		{
			name:        "unexpected command failure",
			err:         errors.New("zenity failed: display unavailable"),
			wantStatus:  http.StatusInternalServerError,
			wantCode:    "directory_picker_failed",
			wantMessage: "Не удалось открыть системный выбор каталога",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector := &fakeDirectorySelector{path: tt.path, err: tt.err}
			handler := NewSystemHandler(selector)
			request := httptest.NewRequest(http.MethodPost, "/api/system/select-directory", nil)
			response := httptest.NewRecorder()

			handler.SelectDirectory(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %q", response.Code, tt.wantStatus, response.Body.String())
			}
			if selector.calls != 1 {
				t.Errorf("selector calls = %d, want 1", selector.calls)
			}
			if tt.wantStatus == http.StatusNoContent {
				if response.Body.Len() != 0 {
					t.Errorf("204 body = %q, want empty", response.Body.String())
				}
				return
			}

			var body struct {
				Path    string `json:"path"`
				Code    string `json:"code"`
				Message string `json:"message"`
				Field   string `json:"field"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("response is not JSON: %v; body = %q", err, response.Body.String())
			}
			if body.Path != tt.wantPath || body.Code != tt.wantCode || body.Message != tt.wantMessage {
				t.Errorf("body = %#v, want path=%q code=%q message=%q", body, tt.wantPath, tt.wantCode, tt.wantMessage)
			}
			if body.Field != "" {
				t.Errorf("body field = %q, want omitted or empty", body.Field)
			}
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
		})
	}
}

func TestSystemHandlerSelectDirectoryRejectsOtherMethods(t *testing.T) {
	selector := &fakeDirectorySelector{}
	handler := NewSystemHandler(selector)
	request := httptest.NewRequest(http.MethodGet, "/api/system/select-directory", nil)
	response := httptest.NewRecorder()

	handler.SelectDirectory(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
	if got := response.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow = %q, want %q", got, http.MethodPost)
	}
	if selector.calls != 0 {
		t.Errorf("selector calls = %d, want 0", selector.calls)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if body.Code != "method_not_allowed" {
		t.Errorf("error code = %q, want method_not_allowed", body.Code)
	}
}
```

- [ ] **Step 3: Запустить handler RED-тест и подтвердить ожидаемое падение**

Run: `go test ./internal/handlers -run TestSystemHandlerSelectDirectory -v`

Expected: FAIL на компиляции с `undefined: NewSystemHandler`. `WriteAPIError` уже должен компилироваться благодаря prerequisite Step 1.

- [ ] **Step 4: Реализовать handler без раскрытия command diagnostics клиенту**

Создать `internal/handlers/system_handler.go`:

```go
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"IGoNotes/internal/service"
)

type DirectorySelector interface {
	SelectDirectory(ctx context.Context) (string, error)
}

type SystemHandler struct {
	directorySelector DirectorySelector
}

func NewSystemHandler(directorySelector DirectorySelector) *SystemHandler {
	return &SystemHandler{directorySelector: directorySelector}
}

func (h *SystemHandler) SelectDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Метод не поддерживается", "")
		return
	}

	path, err := h.directorySelector.SelectDirectory(r.Context())
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"path": path})
	case errors.Is(err, service.ErrDirectorySelectionCanceled):
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, service.ErrDirectoryPickerUnavailable):
		WriteAPIError(
			w,
			http.StatusNotImplemented,
			"directory_picker_unavailable",
			"Системный выбор каталога недоступен",
			"",
		)
	default:
		WriteAPIError(
			w,
			http.StatusInternalServerError,
			"directory_picker_failed",
			"Не удалось открыть системный выбор каталога",
			"",
		)
	}
}
```

Handler не возвращает клиенту stderr PowerShell/osascript/zenity/kdialog: это исключает утечку platform diagnostics и сохраняет стабильный пользовательский REST-контракт. Ошибка кодирования двухстрочного success JSON после записи response практически не восстанавливается; здесь намеренно используется тот же простой `json.Encoder` pattern, что и в текущих handlers.

- [ ] **Step 5: Отформатировать HTTP slice**

Run: `gofmt -w internal/handlers/system_handler.go internal/handlers/system_handler_test.go`

- [ ] **Step 6: Выполнить handler GREEN-тест**

Run: `go test ./internal/handlers -run TestSystemHandlerSelectDirectory -v`

Expected: PASS для `200` с `{"path":...}`, пустого `204`, structured `501/500` и structured `405` с `Allow: POST`.

- [ ] **Step 7: Зафиксировать HTTP slice**

```bash
git add internal/handlers/system_handler.go internal/handlers/system_handler_test.go
git commit -m "feat: expose directory picker API handler"
```

### Task 4: Регистрация route и production wiring

**Files:**
- Create: `cmd/api/system_routes.go`
- Create: `cmd/api/system_routes_test.go`
- Modify: `cmd/api/main.go`

- [ ] **Step 1: Написать RED integration-тест точного маршрута**

Создать `cmd/api/system_routes_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"IGoNotes/internal/handlers"
)

type routeDirectorySelector struct{}

func (routeDirectorySelector) SelectDirectory(context.Context) (string, error) {
	return "/selected/by/route", nil
}

func TestRegisterSystemRoutes(t *testing.T) {
	mux := http.NewServeMux()
	registerSystemRoutes(mux, handlers.NewSystemHandler(routeDirectorySelector{}))
	request := httptest.NewRequest(http.MethodPost, "/api/system/select-directory", nil)
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %q", response.Code, http.StatusOK, response.Body.String())
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if body.Path != "/selected/by/route" {
		t.Errorf("path = %q, want /selected/by/route", body.Path)
	}
}
```

- [ ] **Step 2: Собрать обязательный embed-контент для тестов пакета `cmd/api`**

Run: `npm --prefix web ci`

Expected: dependencies frontend воспроизводимо установлены из `web/package-lock.json` без изменения lockfile.

Run: `npm --prefix web run build`

Expected: Vite завершается успешно и создаёт `web/dist`, требуемый директивой `//go:embed all:dist` при компиляции `cmd/api`.

- [ ] **Step 3: Запустить route RED-тест и подтвердить ожидаемое падение**

Run: `go test ./cmd/api -run TestRegisterSystemRoutes -v`

Expected: FAIL на компиляции с `undefined: registerSystemRoutes`.

- [ ] **Step 4: Создать маленькую тестируемую функцию регистрации**

Создать `cmd/api/system_routes.go`:

```go
package main

import (
	"net/http"

	"IGoNotes/internal/handlers"
)

func registerSystemRoutes(mux *http.ServeMux, systemHandler *handlers.SystemHandler) {
	mux.HandleFunc("/api/system/select-directory", systemHandler.SelectDirectory)
}
```

- [ ] **Step 5: Подключить production runner и route к существующему router в `main.go`**

Этот шаг выполняется после backend-плана: `ConfigHandler` и package-global `http.DefaultServeMux` уже удалены, а `handlers.NewRouter` возвращает локальный `*http.ServeMux`. В блоке создания handlers добавить production composition:

```go
	noteHandler := handlers.NewNoteHandler(noteService)
	settingsHandler := handlers.NewSettingsHandler(settingsService)
	directoryPicker := service.NewDirectoryPicker(service.ExecCommandRunner{}, runtime.GOOS)
	systemHandler := handlers.NewSystemHandler(directoryPicker)
```

После создания SPA handler сохранить backend router и зарегистрировать на нём только новый system route:

```go
	router := handlers.NewRouter(noteHandler, settingsHandler, settingsService, spaHandler)
	registerSystemRoutes(router, systemHandler)
```

В конце `main` оставить server call backend-плана на том же router:

```go
	if err := http.ListenAndServe(address, router); err != nil {
		log.Fatal(err)
	}
```

`runtime` уже импортирован и используется функцией `openBrowser`, поэтому нового import для GOOS injection не требуется. Не восстанавливать удалённый `ConfigHandler`, не создавать второй mux и не регистрировать package-level routes: более специфичный `/api/system/select-directory` добавляется на тот же router рядом с уже существующим SPA fallback `/`.

- [ ] **Step 6: Отформатировать route wiring**

Run: `gofmt -w cmd/api/main.go cmd/api/system_routes.go cmd/api/system_routes_test.go`

- [ ] **Step 7: Выполнить route GREEN-тест**

Run: `go test ./cmd/api -run TestRegisterSystemRoutes -v`

Expected: PASS; POST на точный path проходит через зарегистрированный `SystemHandler` и возвращает выбранный путь.

- [ ] **Step 8: Выполнить regression-тесты изменённых Go-пакетов**

Run: `go test ./cmd/api ./internal/handlers ./internal/service`

Expected: PASS во всех трёх изменённых пакетах.

- [ ] **Step 9: Зафиксировать production wiring**

```bash
git add cmd/api/main.go cmd/api/system_routes.go cmd/api/system_routes_test.go
git commit -m "feat: wire directory picker route"
```

### Task 5: Race, static analysis и cross-platform build verification

**Files:**
- Verify: `internal/service/directory_picker.go`
- Verify: `internal/service/directory_picker_test.go`
- Verify: `internal/handlers/system_handler.go`
- Verify: `internal/handlers/system_handler_test.go`
- Verify: `cmd/api/system_routes.go`
- Verify: `cmd/api/system_routes_test.go`
- Verify: `cmd/api/main.go`

- [ ] **Step 1: Подтвердить наличие актуального embed-контента**

Run: `npm --prefix web run build`

Expected: Vite завершается успешно и обновляет `web/dist`; последующие Go-команды не падают с `pattern all:dist: no matching files found`.

- [ ] **Step 2: Запустить полный Go test suite с race detector**

Run: `go test -race ./...`

Expected: PASS во всех пакетах; picker tests не открывают окна, потому что используют `fakeCommandRunner` и injected GOOS.

- [ ] **Step 3: Запустить static analysis**

Run: `go vet ./...`

Expected: exit code 0 без диагностик.

- [ ] **Step 4: Скомпилировать platform service tests для Windows и macOS**

Run: `GOOS=windows GOARCH=amd64 go test -c -o /tmp/opencode/igonotes-service-windows-amd64.test.exe ./internal/service`

Expected: exit code 0; создаётся Windows test binary без попытки выполнить его на Linux.

Run: `GOOS=darwin GOARCH=amd64 go test -c -o /tmp/opencode/igonotes-service-darwin-amd64.test ./internal/service`

Expected: exit code 0; создаётся macOS test binary без попытки выполнить его на Linux.

- [ ] **Step 5: Выполнить cross-build всего backend entrypoint**

Run: `GOOS=windows GOARCH=amd64 go build -o /tmp/opencode/igonotes-windows-amd64.exe ./cmd/api`

Expected: exit code 0 и Windows executable в `/tmp/opencode`; PowerShell используется только как runtime child process и не создаёт compile-time dependency.

Run: `GOOS=darwin GOARCH=amd64 go build -o /tmp/opencode/igonotes-darwin-amd64 ./cmd/api`

Expected: exit code 0 и macOS executable в `/tmp/opencode`.

Run: `GOOS=linux GOARCH=amd64 go build -o /tmp/opencode/igonotes-linux-amd64 ./cmd/api`

Expected: exit code 0 и Linux executable в `/tmp/opencode`.

- [ ] **Step 6: Выполнить полную project build**

Run: `make all`

Expected: `npm install`, Vite build и Go build завершаются успешно; production binary создаётся как `builds/igonotes`.

- [ ] **Step 7: Проверить безопасность invocation и отсутствие scope creep**

Run: `git grep -n -E 'CommandContext\(ctx,|powershell\.exe|osascript|zenity|kdialog' -- internal/service/directory_picker.go`

Expected: видны прямой `exec.CommandContext(ctx, name, args...)` и только четыре ожидаемых executable; отсутствуют `sh`, `bash`, `cmd /c` и пользовательская интерполяция.

Run: `git diff --name-only HEAD~4 --`

Expected: только семь implementation/test файлов этого плана. Не должны появиться изменения в `SettingsService`, `NoteService`, config CRUD, `web/src` или спецификации; незакоммиченный файл плана проверяется отдельно через `git status`.

- [ ] **Step 8: Проверить форматирование и итоговое состояние**

Run: `gofmt -l internal/service/directory_picker.go internal/service/directory_picker_test.go internal/handlers/system_handler.go internal/handlers/system_handler_test.go cmd/api/main.go cmd/api/system_routes.go cmd/api/system_routes_test.go`

Expected: нет вывода.

Run: `git diff --check`

Expected: exit code 0 без whitespace diagnostics.

Run: `git status --short`

Expected: implementation-файлы чистые после четырёх task commits; допустимо видеть только незакоммиченный `docs/superpowers/plans/2026-08-27-setup-settings-directory-picker.md`, потому что создание этого плана не является implementation commit.

## Ключевые контракты после выполнения

```go
type CommandResult struct {
	Output   []byte
	ExitCode int
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (CommandResult, error)
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, name string, args ...string) (CommandResult, error)

var ErrDirectorySelectionCanceled error
var ErrDirectoryPickerUnavailable error

func NewDirectoryPicker(runner CommandRunner, goos string) *DirectoryPicker
func (p *DirectoryPicker) SelectDirectory(ctx context.Context) (string, error)

type DirectorySelector interface {
	SelectDirectory(ctx context.Context) (string, error)
}

func NewSystemHandler(directorySelector DirectorySelector) *SystemHandler
func (h *SystemHandler) SelectDirectory(w http.ResponseWriter, r *http.Request)

func registerSystemRoutes(mux *http.ServeMux, systemHandler *handlers.SystemHandler)

// package handlers; dependency supplied by the backend setup/settings plan:
func WriteAPIError(w http.ResponseWriter, status int, code, message, field string)
```

## Проверяемая матрица исходов

| Платформа/исход | Service result | HTTP result |
|---|---|---|
| Windows FolderBrowserDialog выбрал каталог | path, `nil` | `200 {"path":"..."}` |
| macOS choose folder выбрал каталог | path, `nil` | `200 {"path":"..."}` |
| Linux zenity выбрал каталог | path, `nil` | `200 {"path":"..."}` |
| Linux zenity отсутствует, kdialog выбрал | path, `nil` | `200 {"path":"..."}` |
| Пользователь отменил любой доступный picker | `ErrDirectorySelectionCanceled` | `204`, пустое тело |
| Windows/macOS executable отсутствует | `ErrDirectoryPickerUnavailable` | `501`, `directory_picker_unavailable` |
| Оба Linux executable отсутствуют | `ErrDirectoryPickerUnavailable` | `501`, `directory_picker_unavailable` |
| Unsupported GOOS | `ErrDirectoryPickerUnavailable` | `501`, `directory_picker_unavailable` |
| Доступный executable завершился operational error | wrapped command error | `500`, `directory_picker_failed` |
| Метод отличен от POST | picker не вызывается | `405`, `method_not_allowed`, `Allow: POST` |
