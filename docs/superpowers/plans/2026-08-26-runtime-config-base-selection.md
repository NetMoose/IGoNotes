# План реализации конфигурации при запуске и выбора базы заметок

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Все записанные шаги отмечены как завершённые.

**Goal:** Подключить сохранённую конфигурацию к запуску приложения, реализовать выбор базы через `--base` и использовать системный XDG-совместимый каталог конфигурации.

**Architecture:** Определение каталога конфигурации выделяется в небольшой модуль пакета `main`, а инициализация конфигурации и выбор базы выполняются отдельным сервисом в `internal/service`. `ConfigService` остаётся слоем хранения JSON, `main.go` только разбирает флаги и связывает готовые компоненты.

**Tech Stack:** Go 1.26, стандартные пакеты `flag`, `os`, `path/filepath`, табличные unit-тесты из `testing`, существующие модели `model.Config` и `model.Base`.

---

## Структура файлов

- Создать `cmd/api/config_path.go`: определить каталог конфигурации с приоритетом явного `--config` над `os.UserConfigDir()`.
- Создать `cmd/api/config_path_test.go`: проверить явный путь, системный путь и ошибку системного определения.
- Изменить `internal/service/config_service.go`: добавить проверку отсутствующего или пустого файла без смешивания этого состояния с корректным JSON `{}`.
- Создать `internal/service/config_service_test.go`: проверить состояния файла конфигурации.
- Создать `internal/service/startup_service.go`: создать первоначальную конфигурацию, выбрать и проверить базу заметок.
- Создать `internal/service/startup_service_test.go`: покрыть инициализацию, приоритеты выбора и ошибки конфигурации.
- Изменить `cmd/api/main.go`: использовать новые компоненты вместо жёсткого `bases/default` и игнорируемого `--base`.
- Изменить `AGENTS.md`, `docs/user.md`, `docs/developer.md`: описать фактическое XDG-поведение и первоначальную базу `default`.

## Task 1: Определение XDG-совместимого каталога конфигурации

**Files:**
- Create: `cmd/api/config_path.go`
- Create: `cmd/api/config_path_test.go`

- [x] **Step 1: Написать падающие тесты определения каталога**

Создать `cmd/api/config_path_test.go`:

```go
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
	got, err := resolveConfigDir("", func() (string, error) {
		return systemDir, nil
	})
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
	_, err := resolveConfigDir("", func() (string, error) {
		return "", wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("resolveConfigDir() error = %v, want wrapped %v", err, wantErr)
	}
}
```

- [x] **Step 2: Запустить тест и подтвердить ожидаемое падение**

Run: `go test ./cmd/api -run TestResolveConfigDir -v`

Expected: FAIL с ошибкой `undefined: resolveConfigDir`.

- [x] **Step 3: Реализовать минимальное определение каталога**

Создать `cmd/api/config_path.go`:

```go
package main

import (
	"fmt"
	"path/filepath"
)

func resolveConfigDir(explicitDir string, userConfigDir func() (string, error)) (string, error) {
	if explicitDir != "" {
		return explicitDir, nil
	}

	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("не удалось определить пользовательский каталог конфигурации: %w", err)
	}
	return filepath.Join(dir, "igonotes"), nil
}
```

- [x] **Step 4: Отформатировать код и подтвердить прохождение теста**

Run: `gofmt -w cmd/api/config_path.go cmd/api/config_path_test.go`

Run: `go test ./cmd/api -run TestResolveConfigDir -v`

Expected: PASS для трёх тестов `TestResolveConfigDir...`.

- [x] **Step 5: Зафиксировать определение каталога**

```bash
git add cmd/api/config_path.go cmd/api/config_path_test.go
git commit -m "feat: resolve platform config directory"
```

## Task 2: Отличать первоначальный запуск от некорректной конфигурации

**Files:**
- Modify: `internal/service/config_service.go:21-63`
- Create: `internal/service/config_service_test.go`

- [x] **Step 1: Написать падающие тесты состояния файла конфигурации**

Создать `internal/service/config_service_test.go`:

```go
package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigServiceNeedsInitialization(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		service := NewConfigService(filepath.Join(t.TempDir(), "config.json"))
		got, err := service.NeedsInitialization()
		if err != nil {
			t.Fatalf("NeedsInitialization() error = %v", err)
		}
		if !got {
			t.Fatal("NeedsInitialization() = false, want true")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(configPath, nil, 0644); err != nil {
			t.Fatal(err)
		}
		service := NewConfigService(configPath)
		got, err := service.NeedsInitialization()
		if err != nil {
			t.Fatalf("NeedsInitialization() error = %v", err)
		}
		if !got {
			t.Fatal("NeedsInitialization() = false, want true")
		}
	})

	t.Run("non-empty file", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
		service := NewConfigService(configPath)
		got, err := service.NeedsInitialization()
		if err != nil {
			t.Fatalf("NeedsInitialization() error = %v", err)
		}
		if got {
			t.Fatal("NeedsInitialization() = true, want false")
		}
	})

	t.Run("stat error", func(t *testing.T) {
		parentFile := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(parentFile, []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
		service := NewConfigService(filepath.Join(parentFile, "config.json"))
		if _, err := service.NeedsInitialization(); err == nil {
			t.Fatal("NeedsInitialization() error = nil, want filesystem error")
		}
	})
}
```

- [x] **Step 2: Запустить тест и подтвердить ожидаемое падение**

Run: `go test ./internal/service -run TestConfigServiceNeedsInitialization -v`

Expected: FAIL с ошибкой `service.NeedsInitialization undefined`.

- [x] **Step 3: Добавить проверку отсутствующего или пустого файла**

Добавить в `internal/service/config_service.go` перед методом `Exists`:

```go
// NeedsInitialization сообщает, отсутствует ли файл конфигурации или является пустым.
func (s *ConfigService) NeedsInitialization() (bool, error) {
	info, err := os.Stat(s.configPath)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return info.Size() == 0, nil
}
```

Метод `Exists` пока оставить без изменений: он будет удалён после переключения `main.go` в Task 5.

- [x] **Step 4: Отформатировать код и подтвердить прохождение теста**

Run: `gofmt -w internal/service/config_service.go internal/service/config_service_test.go`

Run: `go test ./internal/service -run TestConfigServiceNeedsInitialization -v`

Expected: PASS для всех четырёх подтестов.

- [x] **Step 5: Зафиксировать различение первоначального запуска**

```bash
git add internal/service/config_service.go internal/service/config_service_test.go
git commit -m "test: distinguish uninitialized config"
```

## Task 3: Создать конфигурацию и выбрать базу при запуске

**Files:**
- Create: `internal/service/startup_service.go`
- Create: `internal/service/startup_service_test.go`

- [x] **Step 1: Написать падающие тесты успешных сценариев запуска**

Создать `internal/service/startup_service_test.go`:

```go
package service

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"IGoNotes/internal/model"
)

func TestResolveStartupBaseInitializesDefaultConfig(t *testing.T) {
	for _, test := range []struct {
		name            string
		createEmptyFile bool
	}{
		{name: "missing config"},
		{name: "empty config", createEmptyFile: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "config", "config.json")
			if test.createEmptyFile {
				if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(configPath, nil, 0644); err != nil {
					t.Fatal(err)
				}
			}

			dataDir := filepath.Join(root, "data")
			configService := NewConfigService(configPath)
			got, err := ResolveStartupBase(configService, "", dataDir)
			if err != nil {
				t.Fatalf("ResolveStartupBase() error = %v", err)
			}

			baseRoot := filepath.Join(dataDir, "bases")
			wantPath := filepath.Join(baseRoot, "default")
			if got != wantPath {
				t.Fatalf("ResolveStartupBase() = %q, want %q", got, wantPath)
			}
			info, err := os.Stat(wantPath)
			if err != nil {
				t.Fatalf("default base was not created: %v", err)
			}
			if !info.IsDir() {
				t.Fatalf("default base path %q is not a directory", wantPath)
			}

			gotConfig, err := configService.Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			wantConfig := &model.Config{
				BaseDir: baseRoot,
				Bases: []model.Base{{
					Name:     "default",
					Path:     wantPath,
					AutoSync: false,
				}},
				CurrentBase: "default",
			}
			if !reflect.DeepEqual(gotConfig, wantConfig) {
				t.Fatalf("saved config = %#v, want %#v", gotConfig, wantConfig)
			}
		})
	}
}

func TestResolveStartupBaseSelectsConfiguredBase(t *testing.T) {
	for _, test := range []struct {
		name      string
		requested string
		wantName  string
	}{
		{name: "current base", wantName: "personal"},
		{name: "CLI override", requested: "work", wantName: "work"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			personalPath := filepath.Join(root, "personal")
			workPath := filepath.Join(root, "work")
			for _, path := range []string{personalPath, workPath} {
				if err := os.MkdirAll(path, 0755); err != nil {
					t.Fatal(err)
				}
			}

			configService := NewConfigService(filepath.Join(root, "config", "config.json"))
			config := &model.Config{
				BaseDir: root,
				Bases: []model.Base{
					{Name: "personal", Path: personalPath},
					{Name: "work", Path: workPath},
				},
				CurrentBase: "personal",
			}
			if err := configService.Save(config); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(root, "config", "config.json"))
			if err != nil {
				t.Fatal(err)
			}

			got, err := ResolveStartupBase(configService, test.requested, filepath.Join(root, "data"))
			if err != nil {
				t.Fatalf("ResolveStartupBase() error = %v", err)
			}
			want := map[string]string{"personal": personalPath, "work": workPath}[test.wantName]
			if got != want {
				t.Fatalf("ResolveStartupBase() = %q, want %q", got, want)
			}

			after, err := os.ReadFile(filepath.Join(root, "config", "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("ResolveStartupBase() modified an existing valid config")
			}
		})
	}
}
```

- [x] **Step 2: Запустить тесты и подтвердить ожидаемое падение**

Run: `go test ./internal/service -run TestResolveStartupBase -v`

Expected: FAIL с ошибкой `undefined: ResolveStartupBase`.

- [x] **Step 3: Реализовать инициализацию и успешный выбор базы**

Создать `internal/service/startup_service.go`:

```go
package service

import (
	"fmt"
	"os"
	"path/filepath"

	"IGoNotes/internal/model"
)

const defaultBaseName = "default"

// ResolveStartupBase инициализирует конфигурацию при первом запуске и возвращает путь выбранной базы.
func ResolveStartupBase(configService *ConfigService, requestedBase, dataDir string) (string, error) {
	needsInitialization, err := configService.NeedsInitialization()
	if err != nil {
		return "", fmt.Errorf("не удалось проверить конфигурацию: %w", err)
	}

	var config *model.Config
	if needsInitialization {
		config, err = initializeDefaultConfig(configService, dataDir)
		if err != nil {
			return "", err
		}
	} else {
		config, err = configService.Load()
		if err != nil {
			return "", fmt.Errorf("не удалось загрузить конфигурацию: %w", err)
		}
	}

	return selectConfiguredBase(config, requestedBase)
}

func initializeDefaultConfig(configService *ConfigService, dataDir string) (*model.Config, error) {
	baseRoot := filepath.Join(dataDir, "bases")
	basePath := filepath.Join(baseRoot, defaultBaseName)
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("не удалось создать базу по умолчанию %q: %w", basePath, err)
	}

	config := &model.Config{
		BaseDir: baseRoot,
		Bases: []model.Base{{
			Name:     defaultBaseName,
			Path:     basePath,
			AutoSync: false,
		}},
		CurrentBase: defaultBaseName,
	}
	if err := configService.Save(config); err != nil {
		return nil, fmt.Errorf("не удалось сохранить первоначальную конфигурацию: %w", err)
	}
	return config, nil
}

func selectConfiguredBase(config *model.Config, requestedBase string) (string, error) {
	selectedName := requestedBase
	source := "--base"
	if selectedName == "" {
		selectedName = config.CurrentBase
		source = "current_base"
	}
	if selectedName == "" {
		return "", fmt.Errorf("поле %s не может быть пустым", source)
	}

	for _, base := range config.Bases {
		if base.Name != selectedName {
			continue
		}
		if base.Path == "" {
			return "", fmt.Errorf("у базы %q пустой путь", selectedName)
		}
		info, err := os.Stat(base.Path)
		if os.IsNotExist(err) {
			return "", fmt.Errorf("путь базы %q не существует", base.Path)
		}
		if err != nil {
			return "", fmt.Errorf("не удалось проверить путь базы %q: %w", base.Path, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("путь базы %q не является каталогом", base.Path)
		}
		return base.Path, nil
	}

	return "", fmt.Errorf("%s %q не соответствует настроенной базе", source, selectedName)
}
```

- [x] **Step 4: Отформатировать код и подтвердить успешные сценарии**

Run: `gofmt -w internal/service/startup_service.go internal/service/startup_service_test.go`

Run: `go test ./internal/service -run TestResolveStartupBase -v`

Expected: PASS для сценариев отсутствующего и пустого config, `current_base` и CLI override.

- [x] **Step 5: Зафиксировать первоначальную конфигурацию и выбор базы**

```bash
git add internal/service/startup_service.go internal/service/startup_service_test.go
git commit -m "feat: resolve startup notes base"
```

## Task 4: Проверить некорректные конфигурации и пути

**Files:**
- Modify: `internal/service/startup_service.go`
- Modify: `internal/service/startup_service_test.go`

- [x] **Step 1: Добавить падающие тесты ошибок конфигурации**

Добавить в `internal/service/startup_service_test.go`:

```go
func TestResolveStartupBaseRejectsInvalidConfig(t *testing.T) {
	root := t.TempDir()
	validPath := filepath.Join(root, "valid")
	if err := os.MkdirAll(validPath, 0755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(root, "base-file")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		config    model.Config
		requested string
		wantParts []string
	}{
		{
			name: "unknown CLI base lists available names",
			config: model.Config{
				Bases:       []model.Base{{Name: "personal", Path: validPath}, {Name: "work", Path: validPath}},
				CurrentBase: "personal",
			},
			requested: "missing",
			wantParts: []string{"--base", "missing", "personal, work"},
		},
		{
			name: "unknown current base",
			config: model.Config{
				Bases:       []model.Base{{Name: "personal", Path: validPath}},
				CurrentBase: "missing",
			},
			wantParts: []string{"current_base", "missing"},
		},
		{
			name: "empty current base",
			config: model.Config{
				Bases: []model.Base{{Name: "", Path: validPath}},
			},
			wantParts: []string{"current_base", "пустым"},
		},
		{
			name: "duplicate names",
			config: model.Config{
				Bases: []model.Base{
					{Name: "personal", Path: validPath},
					{Name: "personal", Path: validPath},
				},
				CurrentBase: "personal",
			},
			wantParts: []string{"повторяющееся", "personal"},
		},
		{
			name: "empty selected path",
			config: model.Config{
				Bases:       []model.Base{{Name: "personal"}},
				CurrentBase: "personal",
			},
			wantParts: []string{"personal", "пустой путь"},
		},
		{
			name: "missing selected path",
			config: model.Config{
				Bases:       []model.Base{{Name: "personal", Path: filepath.Join(root, "missing")}},
				CurrentBase: "personal",
			},
			wantParts: []string{"не существует"},
		},
		{
			name: "selected path is a file",
			config: model.Config{
				Bases:       []model.Base{{Name: "personal", Path: filePath}},
				CurrentBase: "personal",
			},
			wantParts: []string{"не является каталогом"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.json")
			configService := NewConfigService(configPath)
			if err := configService.Save(&test.config); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ResolveStartupBase(configService, test.requested, filepath.Join(root, "data"))
			if err == nil {
				t.Fatal("ResolveStartupBase() error = nil, want validation error")
			}
			for _, part := range test.wantParts {
				if !strings.Contains(err.Error(), part) {
					t.Fatalf("ResolveStartupBase() error = %q, want substring %q", err, part)
				}
			}
			after, readErr := os.ReadFile(configPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(after) != string(before) {
				t.Fatal("ResolveStartupBase() modified an invalid existing config")
			}
		})
	}
}

func TestResolveStartupBaseDoesNotOverwriteMalformedConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	original := []byte(`{"bases": [`)
	if err := os.WriteFile(configPath, original, 0644); err != nil {
		t.Fatal(err)
	}

	configService := NewConfigService(configPath)
	if _, err := ResolveStartupBase(configService, "", t.TempDir()); err == nil {
		t.Fatal("ResolveStartupBase() error = nil, want JSON error")
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("malformed config was overwritten: got %q, want %q", after, original)
	}
}
```

Добавить `"strings"` в imports файла теста.

- [x] **Step 2: Запустить тесты и подтвердить падение новых требований**

Run: `go test ./internal/service -run 'TestResolveStartupBase(RejectsInvalidConfig|DoesNotOverwriteMalformedConfig)' -v`

Expected: FAIL как минимум для duplicate names и сообщения unknown CLI base, не содержащего `personal, work`.

- [x] **Step 3: Добавить уникальность имён и информативные ошибки**

Добавить `"strings"` в imports `internal/service/startup_service.go` и заменить `selectConfiguredBase`:

```go
func selectConfiguredBase(config *model.Config, requestedBase string) (string, error) {
	basesByName := make(map[string]model.Base, len(config.Bases))
	availableNames := make([]string, 0, len(config.Bases))
	for _, base := range config.Bases {
		if _, exists := basesByName[base.Name]; exists {
			return "", fmt.Errorf("конфигурация содержит повторяющееся имя базы %q", base.Name)
		}
		basesByName[base.Name] = base
		availableNames = append(availableNames, base.Name)
	}

	selectedName := requestedBase
	source := "--base"
	if selectedName == "" {
		selectedName = config.CurrentBase
		source = "current_base"
	}
	if selectedName == "" {
		return "", fmt.Errorf("поле %s не может быть пустым", source)
	}

	base, exists := basesByName[selectedName]
	if !exists {
		available := strings.Join(availableNames, ", ")
		if available == "" {
			available = "нет настроенных баз"
		}
		return "", fmt.Errorf("%s %q не соответствует настроенной базе; доступны: %s", source, selectedName, available)
	}
	if base.Path == "" {
		return "", fmt.Errorf("у базы %q пустой путь", selectedName)
	}

	info, err := os.Stat(base.Path)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("путь базы %q не существует", base.Path)
	}
	if err != nil {
		return "", fmt.Errorf("не удалось проверить путь базы %q: %w", base.Path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("путь базы %q не является каталогом", base.Path)
	}
	return base.Path, nil
}
```

- [x] **Step 4: Отформатировать код и запустить весь пакет сервиса**

Run: `gofmt -w internal/service/startup_service.go internal/service/startup_service_test.go`

Run: `go test ./internal/service -v`

Expected: PASS для тестов `ConfigService` и всех сценариев `ResolveStartupBase`.

- [x] **Step 5: Зафиксировать строгую проверку конфигурации**

```bash
git add internal/service/startup_service.go internal/service/startup_service_test.go
git commit -m "feat: validate configured notes bases"
```

## Task 5: Подключить конфигурацию и `--base` к runtime

**Files:**
- Modify: `cmd/api/main.go:35-95`
- Modify: `internal/service/config_service.go:59-63`

- [x] **Step 1: Запустить целевые тесты перед подключением**

Run: `go test ./cmd/api ./internal/service`

Expected: PASS. Это фиксирует рабочее состояние компонентов до изменения точки входа.

- [x] **Step 2: Заменить жёсткие пути и игнорирование `--base` в `main.go`**

> **Историческое примечание:** следующий snippet отражает реализацию Task 5 с `os.Getenv("HOME")`. Окончательная реализация заменена в Task 8 на `resolveDataDir(os.UserHomeDir)` с явной обработкой ошибки и пустого домашнего каталога.

Заменить блок от определения флагов до создания `NoteService` в `cmd/api/main.go` следующим кодом:

```go
	// Определение CLI-флагов
	configPath := flag.String("config", "", "Каталог конфигурации (по умолчанию системный каталог пользователя)")
	port := flag.String("port", "8080", "Порт сервера")
	base := flag.String("base", "", "Имя базы для открытия")
	noBrowser := flag.Bool("no-browser", false, "Не открывать браузер автоматически")
	flag.Parse()

	resolvedConfigDir, err := resolveConfigDir(*configPath, os.UserConfigDir)
	if err != nil {
		log.Fatal("Ошибка определения каталога конфигурации: ", err)
	}
	configFile := filepath.Join(resolvedConfigDir, "config.json")
	configService := service.NewConfigService(configFile)

	appDataDir := filepath.Join(os.Getenv("HOME"), ".igonotes")
	basePath, err := service.ResolveStartupBase(configService, *base, appDataDir)
	if err != nil {
		log.Fatal("Ошибка выбора базы заметок: ", err)
	}

	// Инициализация базы данных
	dbPath := filepath.Join(appDataDir, "metadata.db")
	db, err := repository.InitDB(dbPath)
	if err != nil {
		log.Fatal("Ошибка инициализации БД:", err)
	}
	defer db.Close()

	noteRepo := repository.NewNoteRepository(db)
	noteService := service.NewNoteService(noteRepo, basePath)
```

Оставить создание `configHandler` через уже созданный `configService`. Полностью удалить старый блок `if !configService.Exists()` с комментарием о будущем мастере настройки.

- [x] **Step 3: Удалить больше не используемый `ConfigService.Exists`**

Удалить из `internal/service/config_service.go`:

```go
// Exists проверяет, существует ли файл конфигурации
func (s *ConfigService) Exists() bool {
	_, err := os.Stat(s.configPath)
	return !os.IsNotExist(err)
}
```

- [x] **Step 4: Отформатировать и проверить компиляцию точки входа**

Run: `gofmt -w cmd/api/main.go internal/service/config_service.go`

Run: `go test ./cmd/api ./internal/service`

Expected: PASS.

Run: `go build -o /tmp/opencode/igonotes-config-check ./cmd/api`

Expected: команда завершается с кодом 0 и создаёт проверочный бинарник вне рабочего дерева.

- [x] **Step 5: Проверить отсутствие старой заглушки**

Run: `git grep -n -E '_ = base|defaultBaseDir|Запуск мастера настройки|\.Exists\(\)' -- cmd/api internal/service`

Expected: команда не выводит совпадений.

- [x] **Step 6: Зафиксировать runtime-интеграцию**

```bash
git add cmd/api/main.go internal/service/config_service.go
git commit -m "feat: load configured base at startup"
```

## Task 6: Синхронизировать документацию с новым поведением

**Files:**
- Modify: `AGENTS.md:24-25,59-79,98-102`
- Modify: `docs/user.md:5-28,54-59`
- Modify: `docs/developer.md:78-84,126-133`

- [x] **Step 1: Обновить статус и пути в `AGENTS.md`**

Заменить пункт статуса конфигурации:

```markdown
- [x] Конфигурация подключена к runtime и хранится в системном каталоге пользователя (XDG-совместимо); доступен GET/PUT API
```

Пункт мастера первого запуска оставить незавершённым, поскольку UI не входит в эту задачу.

В структуре данных заменить `~/.config/igonotes/` на Linux/XDG-представление:

```text
$XDG_CONFIG_HOME/igonotes/
└── config.json
```

Заменить пояснение пути:

```markdown
- Конфигурация приложения: `<os.UserConfigDir()>/igonotes/config.json`; в Linux по умолчанию `~/.config/igonotes/config.json`
```

Заменить описание CLI-флага:

```markdown
- `--config` — каталог конфигурации (по умолчанию `<os.UserConfigDir()>/igonotes`)
```

- [x] **Step 2: Исправить первый запуск и CLI в `docs/user.md`**

Заменить описание `--config`:

```markdown
- `--config` — каталог конфигурации (по умолчанию системный каталог пользователя; в Linux обычно `~/.config/igonotes`)
```

Заменить раздел «Настройка при первом запуске»:

```markdown
## Настройка при первом запуске

Если конфигурация отсутствует или пуста, приложение автоматически:

1. Создаёт базу `default` в `~/.igonotes/bases/default`.
2. Сохраняет `config.json` в системном каталоге пользовательской конфигурации (`$XDG_CONFIG_HOME/igonotes` в Linux).
3. Открывает базу `default` в редакторе.

Для выбора другой уже настроенной базы используйте `--base <имя>`. Неизвестное имя базы завершает запуск с ошибкой и списком доступных баз.
```

Заменить строку о конфигурации в файловой структуре:

```markdown
- Конфигурация — `<os.UserConfigDir()>/igonotes/config.json` (в Linux учитывается `XDG_CONFIG_HOME`)
```

- [x] **Step 3: Обновить разработческую документацию**

В `docs/developer.md` заменить дерево конфигурации:

```text
$XDG_CONFIG_HOME/igonotes/
└── config.json
```

Заменить пояснение:

```markdown
- Конфигурация приложения: `<os.UserConfigDir()>/igonotes/config.json` (в Linux учитывается `XDG_CONFIG_HOME`)
```

Заменить описание флага:

```markdown
- `--config` — каталог конфигурации (по умолчанию `<os.UserConfigDir()>/igonotes`)
```

- [x] **Step 4: Проверить документацию на старое утверждение о пути**

Run: `git grep -n 'по умолчанию.*~/.config/igonotes\|Конфигурация.*~/.config/igonotes' -- AGENTS.md docs/user.md docs/developer.md`

Expected: команда не выводит совпадений.

Run: `git diff --check`

Expected: команда завершается с кодом 0 без вывода.

- [x] **Step 5: Зафиксировать документацию runtime-конфигурации**

```bash
git add AGENTS.md docs/user.md docs/developer.md
git commit -m "docs: describe runtime config selection"
```

## Task 7: Полная проверка реализации

**Files:**
- Verify: `cmd/api/config_path.go`
- Verify: `cmd/api/config_path_test.go`
- Verify: `internal/service/config_service.go`
- Verify: `internal/service/config_service_test.go`
- Verify: `internal/service/startup_service.go`
- Verify: `internal/service/startup_service_test.go`
- Verify: `cmd/api/main.go`
- Verify: `AGENTS.md`
- Verify: `docs/user.md`
- Verify: `docs/developer.md`

- [x] **Step 1: Запустить все Go-тесты**

Run: `go test ./...`

Expected: PASS во всех пакетах; `cmd/api` и `internal/service` выполняют новые тесты, остальные пакеты могут сообщить `[no test files]`.

- [x] **Step 2: Запустить статический анализ**

Run: `go vet ./...`

Expected: команда завершается с кодом 0 без диагностик.

- [x] **Step 3: Выполнить полную сборку проекта**

Run: `make all`

Expected: `npm install`, Vite build и `go build` завершаются успешно; бинарник появляется в `builds/igonotes`.

- [x] **Step 4: Проверить форматирование и итоговый diff**

Run: `gofmt -l cmd/api internal/service`

Expected: команда не выводит изменённых Go-файлов.

Run: `git diff --check`

Expected: команда завершается с кодом 0 без вывода.

Run: `git status --short`

Expected: рабочее дерево чистое, кроме самого файла плана, если он не был включён в отдельный документационный коммит до начала выполнения.

- [x] **Step 5: Сверить реализацию со спецификацией**

Проверить каждый раздел `docs/superpowers/specs/2026-08-26-runtime-config-base-selection-design.md`:

- явный `--config` не вызывает `os.UserConfigDir()`;
- отсутствующий или пустой config создаёт и сохраняет `default`;
- `--base` имеет приоритет и не меняет `current_base`;
- неизвестные и повторяющиеся имена завершают запуск ошибкой;
- настроенные отсутствующие каталоги не создаются;
- HTTP API и расположение SQLite не меняются.

Expected: каждый пункт подтверждён тестом или прямой runtime-интеграцией в `main.go`.

## Task 8: Исправления финального review

**Files:**
- Create: `cmd/api/data_path.go`
- Create: `cmd/api/data_path_test.go`
- Modify: `cmd/api/main.go`
- Modify: `AGENTS.md`
- Modify: `README.md`
- Modify: `docs/user.md`
- Modify: `docs/developer.md`
- Modify: `site/docs/user.md`
- Modify: `site/docs/developer.md`
- Modify: `docs/superpowers/specs/2026-08-26-runtime-config-base-selection-design.md`
- Modify: `docs/superpowers/plans/2026-08-26-runtime-config-base-selection.md`

- [x] **Step 1: Зафиксировать RED-тесты системного домашнего каталога**

Добавлены тесты успешного разрешения `<home>/.igonotes`, возврата ошибки `os.UserHomeDir()` и отклонения пустого домашнего каталога. Первый запуск `go test ./cmd/api -run TestResolveDataDir -v` завершился ожидаемым FAIL из-за отсутствующего `resolveDataDir`.

- [x] **Step 2: Реализовать системное разрешение каталога данных**

Добавлен `resolveDataDir`, который вызывает переданную функцию домашнего каталога, оборачивает её ошибку, отклоняет пустой результат и возвращает `<home>/.igonotes`. `main.go` подключён через `resolveDataDir(os.UserHomeDir)` и завершает запуск с контекстной ошибкой, если каталог определить нельзя. Целевые тесты после реализации прошли.

- [x] **Step 3: Зафиксировать runtime-исправление**

Файлы `cmd/api/data_path.go`, `cmd/api/data_path_test.go` и `cmd/api/main.go` зафиксированы commit `fix: resolve platform data directory`.

- [x] **Step 4: Синхронизировать команды запуска и описание путей**

Пользовательский пример запуска больше не подставляет ненастроенное имя базы, а executable-команды в `README.md`, `docs` и `site/docs` используют пакет `./cmd/api`. Флаг `--base` описан только как выбор имени из существующего `config.json`. Спецификация фиксирует семантический путь `~/.igonotes`, разрешение home через `os.UserHomeDir()` и понятную ошибку для недоступного или пустого home. Исторический snippet Task 5 помечен как заменённый этой задачей.

- [x] **Step 5: Выполнить полную проверку и зафиксировать документацию**

Проверены отсутствие устаревших команд repository-wide grep `git grep -n -E 'go (run|build)( -o [^ ]+)? +cmd/api/main\.go' -- '*.md'`, отсутствие незавершённых Step в плане, package build и run/help, `git diff --check` и полный smoke-run `go test ./...`. Документационные исправления зафиксированы commit `docs: align startup path documentation`, а финальные package-command исправления — commit `docs: fix package-level Go commands`.
