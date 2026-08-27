# Backend мастера настройки и управления базами Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Реализовать backend первоначальной настройки и runtime-управления базами: миграцию `setup_completed`, атомарный config, согласованное переключение `NoteService`, REST API настроек и блокировку note API до завершения мастера.

**Architecture:** `SettingsService` становится единственным владельцем изменяемого снимка `model.Config` и сериализует setup/CRUD/switch/PUT одним mutex. `NoteService` защищает путь базы и SQLite-индекс одним `sync.RWMutex`: файловые операции удерживают read-lock, а `SyncFS`, rename с переиндексацией и `SwitchBase` удерживают write-lock; новый индекс строится в памяти и заменяется одной SQL-транзакцией до публикации нового пути. HTTP-слой использует единый JSON error contract, отдельный setup guard и тестируемый `handlers.NewRouter`; native directory picker и его маршрут остаются для второго backend-плана.

**Tech Stack:** Go 1.26, `net/http`, `encoding/json`, `sync`, `os`, `path/filepath`, `database/sql`, `modernc.org/sqlite`, `golang.org/x/sys/windows`, стандартные `testing`/`httptest`, race detector.

---

## Границы и итоговые контракты

Этот план изменяет только backend. Он не добавляет Svelte-код, frontend-тесты, документацию пользователя и native directory picker. `handlers.NewRouter` создаётся в форме, которую второй план расширит регистрацией `POST /api/system/select-directory`; в текущем плане этот путь не регистрируется и возвращает SPA fallback, как любой неизвестный путь.

Итоговые публичные Go-контракты, на которые могут ссылаться picker/frontend планы:

```go
// internal/model/config.go
type Config struct {
	BaseDir        string `json:"base_dir"`
	Bases          []Base `json:"bases"`
	CurrentBase    string `json:"current_base"`
	SetupCompleted *bool  `json:"setup_completed"`
}

// internal/model/api.go
type BaseMutationRequest struct {
	Mode string `json:"mode"` // "create" или "connect"
	Name string `json:"name"`
	Path string `json:"path"`
}

type BaseUpdateRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type BaseSwitchRequest struct {
	Name string `json:"name"`
}

type SettingsResponse struct {
	Config   Config `json:"config"`
	BasePath string `json:"base_path"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}
```

```go
// internal/service/settings_service.go
type ConfigStore interface {
	Load() (*model.Config, error)
	Save(*model.Config) error
}

type BaseRuntime interface {
	GetBasePath() string
	SwitchBase(string) error
}

func NewSettingsService(ConfigStore, BaseRuntime, string, *log.Logger) (*SettingsService, error)
func (s *SettingsService) GetConfig() model.Config
func (s *SettingsService) SetupCompleted() bool
func (s *SettingsService) CompleteSetup(model.BaseMutationRequest) (model.SettingsResponse, error)
func (s *SettingsService) AddBase(model.BaseMutationRequest) (model.SettingsResponse, error)
func (s *SettingsService) UpdateBase(string, model.BaseUpdateRequest) (model.SettingsResponse, error)
func (s *SettingsService) ForgetBase(string) (model.SettingsResponse, error)
func (s *SettingsService) SwitchBase(string) (model.SettingsResponse, error)
func (s *SettingsService) ReplaceConfig(model.Config) (model.SettingsResponse, error)
```

```go
// internal/service/settings_errors.go
var (
	ErrSetupRequired         = errors.New("setup required")
	ErrSetupAlreadyCompleted = errors.New("setup already completed")
	ErrSetupCannotReopen     = errors.New("setup cannot be reopened")
	ErrInvalidConfig         = errors.New("invalid config")
	ErrInvalidMode           = errors.New("invalid base mode")
	ErrInvalidName           = errors.New("invalid base name")
	ErrInvalidPath           = errors.New("invalid base path")
	ErrBaseNotFound          = errors.New("base not found")
	ErrBaseNameConflict      = errors.New("base name conflict")
	ErrBasePathConflict      = errors.New("base path conflict")
	ErrActiveBase            = errors.New("active base cannot be forgotten")
	ErrLastBase              = errors.New("last base cannot be forgotten")
	ErrRollbackFailed        = errors.New("rollback failed")
)

type FieldError struct {
	Kind    error
	Field   string
	Message string
}
```

```go
// internal/handlers/errors.go и internal/handlers/routes.go
func WriteAPIError(http.ResponseWriter, int, string, string, string)
func NewRouter(*NoteHandler, *SettingsHandler, SetupState, http.Handler) *http.ServeMux
```

REST success contracts:

- `GET /api/config` возвращает `model.Config` напрямую; после startup `setup_completed` всегда JSON boolean, не `null`.
- `POST /api/setup`, `POST /api/bases`, `PUT /api/bases?name=...`, `DELETE /api/bases?name=...`, `POST /api/bases/switch` и `PUT /api/config` возвращают `model.SettingsResponse`.
- Любая API-ошибка возвращает top-level `model.APIError` с `Content-Type: application/json`; status/code mapping фиксируется в Task 8.
- `POST /api/setup` с `mode=create` трактует `path` как существующий parent и создаёт `<path>/<name>`; `mode=connect` трактует `path` как существующий точный каталог.
- При успешном setup placeholder `default` целиком заменяется единственной выбранной базой, `current_base` становится её именем, `setup_completed` становится `true`, а `base_dir` становится parent итогового пути.
- `PUT /api/config` с `setup_completed: null` сохраняет текущее состояние; явный `false` после завершения setup возвращает `409 setup_cannot_reopen`; явный `true` разрешён при полностью валидном config.

Единый порядок блокировок: command methods берут `SettingsService.mu`, затем при необходимости `NoteService.baseMu`, затем repository открывает SQL transaction. Note handlers никогда не берут `SettingsService.mu`; setup guard отпускает read-lock SettingsService до входа в NoteHandler. Scanner/repository не вызывают SettingsService или NoteService, поэтому обратного порядка `baseMu -> SettingsService.mu` нет.

## Структура файлов

- Modify `internal/model/config.go`: nullable migration field `SetupCompleted`.
- Modify `internal/model/api.go`: setup/base request, settings response и JSON error contracts.
- Modify `internal/service/startup_service.go`: новый placeholder config получает `setup_completed: false`.
- Modify `internal/service/startup_service_test.go`: first-run schema и legacy expectations.
- Modify `internal/service/config_service.go`: temp-file + atomic replace.
- Create `internal/service/config_replace_unix.go`: atomic `os.Rename` для non-Windows.
- Create `internal/service/config_replace_windows.go`: `MoveFileEx(REPLACE_EXISTING|WRITE_THROUGH)`.
- Modify `internal/service/config_service_test.go`: success, replace failure, cleanup.
- Modify `go.mod`: сделать существующую зависимость `golang.org/x/sys` прямой.
- Modify `internal/repository/db.go`: добавить CHECK constraint для допустимого типа note node.
- Modify `internal/repository/note_repo.go`: transactional `ReplaceAll`.
- Create `internal/repository/note_repo_test.go`: commit/rollback index replacement.
- Modify `internal/service/note_service.go`: RW locking, scanner, `SwitchBase`, locked raw file.
- Create `internal/service/note_service_test.go`: switch, rollback and concurrency.
- Create `internal/service/settings_errors.go`: stable sentinels and field errors.
- Create `internal/service/settings_service.go`: migration, setup/base CRUD/switch/full PUT.
- Create `internal/service/settings_service_test.go`: service-level state, validation and rollback.
- Create `internal/handlers/errors.go`: JSON writer and service error mapping.
- Create `internal/handlers/errors_test.go`: status/code/field mapping.
- Delete `internal/handlers/config_handler.go`: direct ConfigService mutation is forbidden.
- Create `internal/handlers/settings_handler.go`: config/setup/base handlers through SettingsService.
- Create `internal/handlers/settings_handler_test.go`: request/response integration.
- Create `internal/handlers/setup_guard.go`: current-state `428` middleware.
- Create `internal/handlers/setup_guard_test.go`: dynamic guard behavior.
- Modify `internal/handlers/note_handler.go`: structured errors and locked raw serving.
- Create `internal/handlers/routes.go`: all API routes and guard placement.
- Create `internal/handlers/routes_test.go`: route matrix including seven guarded paths.
- Modify `cmd/api/main.go`: SettingsService construction and `NewRouter` wiring.

### Task 1: Добавить schema `setup_completed` и first-run значение

**Files:**
- Modify: `internal/model/config.go:3-16`
- Modify: `internal/model/api.go:1-17`
- Modify: `internal/service/startup_service.go:47-55`
- Modify: `internal/service/startup_service_test.go:15-102`

- [ ] **Step 1: Написать RED-проверки nullable schema и нового placeholder config**

В `internal/service/startup_service_test.go` добавить helper и assertions в оба подтеста `TestResolveStartupBaseInitializesDefaultConfig`:

```go
func boolPointer(value bool) *bool { return &value }

// В wantConfig:
SetupCompleted: boolPointer(false),

// После DeepEqual:
data, err := os.ReadFile(configPath)
if err != nil {
	t.Fatalf("os.ReadFile() error = %v", err)
}
if !bytes.Contains(data, []byte(`"setup_completed": false`)) {
	t.Fatalf("config.json = %s, want setup_completed false", data)
}
```

- [ ] **Step 2: Запустить RED-тест**

Run: `go test ./internal/service -run TestResolveStartupBaseInitializesDefaultConfig -v`

Expected: FAIL при компиляции с `unknown field SetupCompleted in struct literal of type model.Config`.

- [ ] **Step 3: Добавить модели и first-run false**

Заменить `model.Config` и дополнить `internal/model/api.go`:

```go
type Config struct {
	BaseDir        string `json:"base_dir"`
	Bases          []Base `json:"bases"`
	CurrentBase    string `json:"current_base"`
	SetupCompleted *bool  `json:"setup_completed"`
}
```

```go
type BaseMutationRequest struct {
	Mode string `json:"mode"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type BaseUpdateRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type BaseSwitchRequest struct {
	Name string `json:"name"`
}

type SettingsResponse struct {
	Config   Config `json:"config"`
	BasePath string `json:"base_path"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}
```

В `initializeDefaultConfig` создать pointer без package-global mutable value:

```go
	setupCompleted := false
	config := &model.Config{
		BaseDir: filepath.Clean(baseRoot),
		Bases: []model.Base{{
			Name:     defaultBaseName,
			Path:     filepath.Clean(basePath),
			AutoSync: false,
		}},
		CurrentBase:    defaultBaseName,
		SetupCompleted: &setupCompleted,
	}
```

- [ ] **Step 4: Обновить существующие legacy fixtures**

Существующие config literals в `TestResolveStartupBaseSelectsConfiguredBase` и `TestResolveStartupBaseRejectsInvalidConfig` намеренно оставить с nil: они моделируют старые файлы. Изменить только `wantConfig` first-run теста; это сохраняет отдельное покрытие миграции в Task 5.

- [ ] **Step 5: Отформатировать и запустить GREEN-тесты startup**

Run: `gofmt -w internal/model/config.go internal/model/api.go internal/service/startup_service.go internal/service/startup_service_test.go`

Run: `go test ./internal/service -run TestResolveStartupBase -v`

Expected: PASS для всех startup-тестов; first-run JSON содержит `setup_completed: false`.

- [ ] **Step 6: Зафиксировать schema**

```bash
git add internal/model/config.go internal/model/api.go internal/service/startup_service.go internal/service/startup_service_test.go
git commit -m "feat: add setup completion state"
```

### Task 2: Сделать `ConfigService.Save` атомарным

**Files:**
- Modify: `internal/service/config_service.go:12-58`
- Create: `internal/service/config_replace_unix.go`
- Create: `internal/service/config_replace_windows.go`
- Modify: `internal/service/config_service_test.go:1-76`
- Modify: `go.mod`

- [ ] **Step 1: Написать RED-тест сохранности исходного файла**

Добавить в `internal/service/config_service_test.go` imports `errors`, `strings`, `IGoNotes/internal/model` и тест:

```go
func TestConfigServiceSavePreservesOriginalWhenReplaceFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	original := []byte(`{"current_base":"old"}`)
	if err := os.WriteFile(configPath, original, 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewConfigService(configPath)
	svc.replace = func(_, _ string) error { return errors.New("replace failed") }
	err := svc.Save(&model.Config{CurrentBase: "new"})
	if err == nil || !strings.Contains(err.Error(), "replace failed") {
		t.Fatalf("Save() error = %v, want replace failure", err)
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("config after failed Save = %q, want %q", after, original)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".config-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files after failed Save = %v, want none", matches)
	}
}
```

- [ ] **Step 2: Запустить RED-тест atomic save**

Run: `go test ./internal/service -run TestConfigServiceSavePreservesOriginalWhenReplaceFails -v`

Expected: FAIL при компиляции с `svc.replace undefined`.

- [ ] **Step 3: Реализовать полностью записанный temp и injectable replace**

Изменить `ConfigService` и полностью заменить `Save`:

```go
type ConfigService struct {
	configPath string
	replace    func(string, string) error
}

func NewConfigService(configPath string) *ConfigService {
	return &ConfigService{configPath: configPath, replace: replaceFile}
}

func (s *ConfigService) Save(config *model.Config) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	temp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tempPath := temp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temp.Close()
		}
		_ = os.Remove(tempPath)
	}()

	if err := temp.Chmod(0644); err != nil {
		return fmt.Errorf("set temporary config permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	closed = true
	if err := s.replace(tempPath, s.configPath); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Добавить platform-specific atomic replace**

Создать `internal/service/config_replace_unix.go`:

```go
//go:build !windows

package service

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}
```

Создать `internal/service/config_replace_windows.go`:

```go
//go:build windows

package service

import "golang.org/x/sys/windows"

func replaceFile(source, target string) error {
	sourcePath, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPath, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(
		sourcePath,
		targetPath,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}
```

Promote `golang.org/x/sys v0.44.0` из indirect в direct dependency, не меняя версию.

- [ ] **Step 5: Добавить GREEN success test и проверить JSON**

```go
func TestConfigServiceSaveAtomicallyReplacesConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	svc := NewConfigService(configPath)
	completed := true
	want := &model.Config{CurrentBase: "work", SetupCompleted: &completed}
	if err := svc.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := svc.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.CurrentBase != "work" || got.SetupCompleted == nil || !*got.SetupCompleted {
		t.Fatalf("Load() = %#v, want saved config", got)
	}
}
```

Run: `gofmt -w internal/service/config_service.go internal/service/config_service_test.go internal/service/config_replace_unix.go internal/service/config_replace_windows.go`

Run: `go test ./internal/service -run 'TestConfigService(Save|NeedsInitialization)' -v`

Expected: PASS; forced replace failure leaves original bytes and no `.config-*.tmp`.

- [ ] **Step 6: Проверить Windows compile path**

Run: `GOOS=windows GOARCH=amd64 go test -c -o /tmp/opencode/service-windows.test.exe ./internal/service`

Expected: exit 0; Windows build разрешает `windows.MoveFileEx` и flags.

- [ ] **Step 7: Зафиксировать atomic config**

```bash
git add go.mod internal/service/config_service.go internal/service/config_service_test.go internal/service/config_replace_unix.go internal/service/config_replace_windows.go
git commit -m "feat: save config atomically"
```

### Task 3: Заменять SQLite-индекс одной транзакцией

**Files:**
- Modify: `internal/repository/db.go:25-42`
- Modify: `internal/repository/note_repo.go:1-73`
- Create: `internal/repository/note_repo_test.go`

- [ ] **Step 1: Написать RED-тест commit и rollback**

Создать `internal/repository/note_repo_test.go` с imports `database/sql`, `path/filepath`, `testing`, `IGoNotes/internal/model`, helper и тестом:

```go
func openTestNoteRepository(t *testing.T) (*NoteRepository, *sql.DB) {
	t.Helper()
	db, err := InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	return NewNoteRepository(db), db
}

func TestNoteRepositoryReplaceAllIsTransactional(t *testing.T) {
	repo, db := openTestNoteRepository(t)
	defer db.Close()
	if err := repo.UpsertNode("old.md", "old", "old.md", nil, "file"); err != nil {
		t.Fatal(err)
	}

	if err := repo.ReplaceAll([]model.NoteNode{{
		ID: "new.md", Name: "new", Path: "new.md", Type: "file",
	}}); err != nil {
		t.Fatalf("ReplaceAll() error = %v", err)
	}
	nodes, err := repo.GetAllNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].ID != "new.md" {
		t.Fatalf("nodes = %#v, want only new.md", nodes)
	}

	if err := repo.ReplaceAll([]model.NoteNode{{
		ID: "broken.md", Name: "broken", Path: "broken.md", Type: "",
	}}); err == nil {
		t.Fatal("ReplaceAll() error = nil, want NOT NULL/CHECK failure")
	}
	nodes, err = repo.GetAllNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].ID != "new.md" {
		t.Fatalf("nodes after rollback = %#v, want only new.md", nodes)
	}
}
```

Для детерминированного failure изменить schema `notes.type` в `internal/repository/db.go` на `type TEXT NOT NULL CHECK(type IN ('file', 'dir'))`; существующие значения уже соответствуют constraint.

- [ ] **Step 2: Запустить RED-тест repository**

Run: `go test ./internal/repository -run TestNoteRepositoryReplaceAllIsTransactional -v`

Expected: FAIL при компиляции с `repo.ReplaceAll undefined`.

- [ ] **Step 3: Реализовать transactional replacement**

Добавить в `internal/repository/note_repo.go`:

```go
func (r *NoteRepository) ReplaceAll(nodes []model.NoteNode) (err error) {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec("DELETE FROM notes"); err != nil {
		return err
	}
	statement, err := tx.Prepare(`
		INSERT INTO notes (id, title, path, parent_id, type)
		VALUES (?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer statement.Close()

	for _, node := range nodes {
		var parentID any
		if node.ParentID != "" {
			parentID = node.ParentID
		}
		if _, err = statement.Exec(node.ID, node.Name, node.Path, parentID, node.Type); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}
```

- [ ] **Step 4: Проверить GREEN и весь repository package**

Run: `gofmt -w internal/repository/db.go internal/repository/note_repo.go internal/repository/note_repo_test.go`

Run: `go test ./internal/repository -v`

Expected: PASS; invalid second replacement откатывает `DELETE` и сохраняет `new.md`.

- [ ] **Step 5: Зафиксировать transactional index**

```bash
git add internal/repository/db.go internal/repository/note_repo.go internal/repository/note_repo_test.go
git commit -m "feat: replace note index transactionally"
```

### Task 4: Добавить RW locking и транзакционный `NoteService.SwitchBase`

**Files:**
- Modify: `internal/service/note_service.go:1-354`
- Create: `internal/service/note_service_test.go`

- [ ] **Step 1: Написать RED-тест успешного switch и scan rollback**

Создать test repository fake, реализующий точные методы интерфейса:

```go
type fakeNoteRepository struct {
	mu      sync.Mutex
	nodes   []model.NoteNode
	failSet bool
}

func (r *fakeNoteRepository) UpsertNode(id, title, path string, parentID *string, nodeType string) error { return nil }
func (r *fakeNoteRepository) DeleteNode(string) error { return nil }
func (r *fakeNoteRepository) GetAllNodes() ([]model.NoteNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.NoteNode(nil), r.nodes...), nil
}
func (r *fakeNoteRepository) ReplaceAll(nodes []model.NoteNode) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failSet {
		return errors.New("replace failed")
	}
	r.nodes = append([]model.NoteNode(nil), nodes...)
	return nil
}
```

```go
func TestNoteServiceSwitchBasePublishesPathAfterIndex(t *testing.T) {
	oldBase := t.TempDir()
	newBase := t.TempDir()
	if err := os.WriteFile(filepath.Join(newBase, "new.md"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	repo := &fakeNoteRepository{}
	svc := NewNoteService(repo, oldBase)
	if err := svc.SwitchBase(newBase); err != nil {
		t.Fatalf("SwitchBase() error = %v", err)
	}
	if got := svc.GetBasePath(); got != newBase {
		t.Fatalf("GetBasePath() = %q, want %q", got, newBase)
	}
	if len(repo.nodes) != 1 || repo.nodes[0].ID != "new.md" {
		t.Fatalf("index = %#v, want new.md", repo.nodes)
	}
}

func TestNoteServiceSwitchBaseKeepsOldStateOnFailure(t *testing.T) {
	oldBase := t.TempDir()
	newBase := t.TempDir()
	repo := &fakeNoteRepository{nodes: []model.NoteNode{{ID: "old.md"}}, failSet: true}
	svc := NewNoteService(repo, oldBase)
	if err := svc.SwitchBase(newBase); err == nil {
		t.Fatal("SwitchBase() error = nil, want replacement error")
	}
	if got := svc.GetBasePath(); got != oldBase {
		t.Fatalf("GetBasePath() = %q, want old path %q", got, oldBase)
	}
	if len(repo.nodes) != 1 || repo.nodes[0].ID != "old.md" {
		t.Fatalf("index = %#v, want old index", repo.nodes)
	}
}
```

- [ ] **Step 2: Запустить RED-тест switch**

Run: `go test ./internal/service -run 'TestNoteServiceSwitchBase' -v`

Expected: FAIL при компиляции: constructor требует `*repository.NoteRepository`, а `SwitchBase` отсутствует.

- [ ] **Step 3: Ввести repository interface, scanner и единый lock order**

В `note_service.go` определить:

```go
type noteRepository interface {
	UpsertNode(id, title, path string, parentID *string, nodeType string) error
	GetAllNodes() ([]model.NoteNode, error)
	ReplaceAll([]model.NoteNode) error
	DeleteNode(id string) error
}

type noteScanner func(string) ([]model.NoteNode, error)

type NoteService struct {
	repo            noteRepository
	basePath        string
	baseMu          sync.RWMutex
	initialSyncDone chan struct{}
	once            sync.Once
	scan            noteScanner
}

func NewNoteService(repo noteRepository, basePath string) *NoteService {
	return &NoteService{
		repo:            repo,
		basePath:        basePath,
		initialSyncDone: make(chan struct{}),
		scan:            scanNotes,
	}
}
```

Lock order, который должен быть отражён коротким comment над struct: всегда `baseMu` перед вызовом repository/SQL; ни repository, ни scanner не вызывают методы `NoteService`. Удалить `syncMu`.

- [ ] **Step 4: Выделить pure scan и реализовать `SyncFS`/`SwitchBase`**

Полностью заменить текущий Walk-блок следующими функциями:

```go
func scanNotes(basePath string) ([]model.NoteNode, error) {
	if basePath == "" {
		return nil, nil
	}
	nodes := make([]model.NoteNode, 0)
	err := filepath.Walk(basePath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() && path != basePath && (strings.HasPrefix(info.Name(), ".") || info.Name() == "assets") {
			return filepath.SkipDir
		}
		if path == basePath {
			return nil
		}
		isDir := info.IsDir()
		if !isDir && !strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			return nil
		}
		relPath, err := filepath.Rel(basePath, path)
		if err != nil {
			return err
		}
		title := info.Name()
		nodeType := "dir"
		if !isDir {
			nodeType = "file"
			title = strings.TrimSuffix(title, filepath.Ext(title))
		}
		parentID := ""
		if parent := filepath.Dir(relPath); parent != "." && parent != "" {
			parentID = parent
		}
		nodes = append(nodes, model.NoteNode{
			ID: relPath, Name: title, Type: nodeType, Path: relPath, ParentID: parentID,
		})
		return nil
	})
	return nodes, err
}

func (s *NoteService) SyncFS() error {
	s.baseMu.Lock()
	defer s.baseMu.Unlock()
	defer s.once.Do(func() { close(s.initialSyncDone) })
	return s.replaceIndexLocked()
}

func (s *NoteService) SwitchBase(basePath string) error {
	s.baseMu.Lock()
	defer s.baseMu.Unlock()

	info, err := os.Stat(basePath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("base path %q is not a directory", basePath)
	}
	nodes, err := s.scan(basePath)
	if err != nil {
		return err
	}
	if err := s.repo.ReplaceAll(nodes); err != nil {
		return err
	}
	s.basePath = filepath.Clean(basePath)
	s.once.Do(func() { close(s.initialSyncDone) })
	return nil
}

func (s *NoteService) GetBasePath() string {
	s.baseMu.RLock()
	defer s.baseMu.RUnlock()
	return s.basePath
}
```

- [ ] **Step 5: Удерживать lock во всех note operations**

Добавить `s.baseMu.RLock()`/`defer s.baseMu.RUnlock()` в начало `GetTree` после ожидания `initialSyncDone`, а также в `GetNoteContent`, `SaveNoteContent`, `CreateNode`, `DeleteNode`, `SaveAsset`. `RenameNode` должен использовать write-lock и после `os.Rename` вызывать private helper, а не рекурсивно `SyncFS`:

```go
func (s *NoteService) replaceIndexLocked() error {
	nodes, err := s.scan(s.basePath)
	if err != nil {
		return err
	}
	return s.repo.ReplaceAll(nodes)
}

func (s *NoteService) RenameNode(id, newName string) error {
	s.baseMu.Lock()
	defer s.baseMu.Unlock()
	if s.basePath == "" || id == "" || id == "." || newName == "" {
		return os.ErrInvalid
	}
	oldPath := filepath.Join(s.basePath, id)
	info, err := os.Stat(oldPath)
	if err != nil {
		return err
	}
	newName = filepath.Base(newName)
	if !info.IsDir() && !strings.HasSuffix(strings.ToLower(newName), ".md") {
		newName += ".md"
	}
	newPath := filepath.Join(filepath.Dir(oldPath), newName)
	if _, err := os.Stat(newPath); err == nil {
		return ErrAlreadyExists
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}
	return s.replaceIndexLocked()
}
```

`SwitchBase` сканирует переданный target напрямую, потому что `s.basePath` публикуется только после успешного `ReplaceAll`; это сохраняет lock order и старое состояние при любой ошибке.

- [ ] **Step 6: Заменить path-return raw API на locked file**

Удалить `GetAbsoluteFilePath` и добавить:

```go
type LockedFile struct {
	*os.File
	once   sync.Once
	unlock func()
}

func (f *LockedFile) Close() error {
	err := f.File.Close()
	f.once.Do(f.unlock)
	return err
}

func (s *NoteService) OpenRawFile(relPath string) (*LockedFile, os.FileInfo, error) {
	s.baseMu.RLock()
	cleanPath := filepath.Clean(relPath)
	if cleanPath == "." || filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		s.baseMu.RUnlock()
		return nil, nil, os.ErrPermission
	}
	fullPath := filepath.Join(s.basePath, cleanPath)
	file, err := os.Open(fullPath)
	if err != nil {
		s.baseMu.RUnlock()
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		s.baseMu.RUnlock()
		return nil, nil, err
	}
	return &LockedFile{File: file, unlock: s.baseMu.RUnlock}, info, nil
}
```

- [ ] **Step 7: Добавить concurrency test старого и ожидающего запросов**

```go
func TestNoteServiceSwitchWaitsForOldRequest(t *testing.T) {
	oldBase := t.TempDir()
	newBase := t.TempDir()
	for path, content := range map[string]string{
		filepath.Join(oldBase, "note.md"): "old",
		filepath.Join(newBase, "note.md"): "new",
	} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewNoteService(&fakeNoteRepository{}, oldBase)
	file, _, err := svc.OpenRawFile("note.md")
	if err != nil {
		t.Fatal(err)
	}

	switchDone := make(chan error, 1)
	go func() { switchDone <- svc.SwitchBase(newBase) }()
	select {
	case err := <-switchDone:
		t.Fatalf("SwitchBase completed while old request holds read lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	oldData, err := io.ReadAll(file)
	if err != nil || string(oldData) != "old" {
		t.Fatalf("old request = %q, %v", oldData, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-switchDone; err != nil {
		t.Fatal(err)
	}
	content, err := svc.GetNoteContent("note.md")
	if err != nil || content != "new" {
		t.Fatalf("new request = %q, %v, want new", content, err)
	}
}

type blockingReplaceRepository struct {
	*fakeNoteRepository
	started chan struct{}
	release chan struct{}
}

func (r *blockingReplaceRepository) ReplaceAll(nodes []model.NoteNode) error {
	close(r.started)
	<-r.release
	return r.fakeNoteRepository.ReplaceAll(nodes)
}

func TestNoteServiceNewRequestWaitsForSwitch(t *testing.T) {
	oldBase := t.TempDir()
	newBase := t.TempDir()
	for path, content := range map[string]string{
		filepath.Join(oldBase, "note.md"): "old",
		filepath.Join(newBase, "note.md"): "new",
	} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	repo := &blockingReplaceRepository{
		fakeNoteRepository: &fakeNoteRepository{},
		started:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	svc := NewNoteService(repo, oldBase)
	switchDone := make(chan error, 1)
	go func() { switchDone <- svc.SwitchBase(newBase) }()
	<-repo.started // SwitchBase owns the write lock while ReplaceAll is blocked.

	type readResult struct {
		content string
		err     error
	}
	readDone := make(chan readResult, 1)
	go func() {
		content, err := svc.GetNoteContent("note.md")
		readDone <- readResult{content: content, err: err}
	}()
	select {
	case result := <-readDone:
		t.Fatalf("new request completed during switch: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}

	close(repo.release)
	if err := <-switchDone; err != nil {
		t.Fatal(err)
	}
	result := <-readDone
	if result.err != nil || result.content != "new" {
		t.Fatalf("new request = %q, %v, want new", result.content, result.err)
	}
}
```

- [ ] **Step 8: Отформатировать и проверить GREEN с race detector**

Run: `gofmt -w internal/service/note_service.go internal/service/note_service_test.go`

Run: `go test -race ./internal/service -run 'TestNoteService' -v`

Expected: PASS без race reports; old locked file задерживает switch, запрос во время удержания write-lock ждёт и после switch читает новую базу.

- [ ] **Step 9: Зафиксировать runtime locking**

```bash
git add internal/service/note_service.go internal/service/note_service_test.go
git commit -m "feat: switch note bases transactionally"
```

### Task 5: Создать `SettingsService`, ошибки и миграцию legacy config

**Files:**
- Create: `internal/service/settings_errors.go`
- Create: `internal/service/settings_service.go`
- Create: `internal/service/settings_service_test.go`

- [ ] **Step 1: Написать RED-тесты миграции nil и сохранения explicit false**

В тесте определить fakes с копированием config:

```go
type fakeConfigStore struct {
	config   model.Config
	saveErr  error
	saveCall int
}

func (f *fakeConfigStore) Load() (*model.Config, error) {
	copy := cloneConfig(f.config)
	return &copy, nil
}
func (f *fakeConfigStore) Save(config *model.Config) error {
	f.saveCall++
	if f.saveErr != nil {
		return f.saveErr
	}
	f.config = cloneConfig(*config)
	return nil
}

type fakeBaseRuntime struct {
	path       string
	switchErr  map[string]error
	switchCall []string
}

func (f *fakeBaseRuntime) GetBasePath() string { return f.path }
func (f *fakeBaseRuntime) SwitchBase(path string) error {
	f.switchCall = append(f.switchCall, path)
	if err := f.switchErr[path]; err != nil {
		return err
	}
	f.path = path
	return nil
}
```

```go
func TestNewSettingsServiceMigratesLegacyConfig(t *testing.T) {
	base := t.TempDir()
	store := &fakeConfigStore{config: model.Config{
		Bases: []model.Base{{Name: "work", Path: base}}, CurrentBase: "work",
	}}
	svc, err := NewSettingsService(store, &fakeBaseRuntime{path: base}, "", log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if !svc.SetupCompleted() || store.config.SetupCompleted == nil || !*store.config.SetupCompleted {
		t.Fatalf("migration result = %#v, want true", store.config)
	}
	if store.saveCall != 1 {
		t.Fatalf("Save calls = %d, want 1", store.saveCall)
	}
}

func TestNewSettingsServiceKeepsExplicitIncompleteSetup(t *testing.T) {
	completed := false
	base := t.TempDir()
	store := &fakeConfigStore{config: model.Config{
		Bases: []model.Base{{Name: "default", Path: base}}, CurrentBase: "default", SetupCompleted: &completed,
	}}
	svc, err := NewSettingsService(store, &fakeBaseRuntime{path: base}, "", log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if svc.SetupCompleted() || store.saveCall != 0 {
		t.Fatalf("SetupCompleted/save calls = %v/%d, want false/0", svc.SetupCompleted(), store.saveCall)
	}
}
```

Добавить `TestNewSettingsServiceUsesCLIBaseInMemoryWithoutStartupSave`: config содержит `personal` и `work`, persisted `current_base=personal`, `activeBaseName="work"`, а runtime path равен пути `work`. Проверить `svc.GetConfig().CurrentBase == "work"`, `store.config.CurrentBase == "personal"` и `saveCall == 0`. Это сохраняет прежнее правило: один только `--base` не переписывает config, но API показывает реально открытую базу; следующая явная settings mutation уже сохраняет актуальный runtime snapshot.

- [ ] **Step 2: Запустить RED-тест migration**

Run: `go test ./internal/service -run 'TestNewSettingsService' -v`

Expected: FAIL при компиляции с undefined `cloneConfig` и `NewSettingsService`.

- [ ] **Step 3: Определить sentinels и typed field error**

Создать `settings_errors.go`:

```go
package service

import "errors"

var (
	ErrSetupRequired         = errors.New("setup required")
	ErrSetupAlreadyCompleted = errors.New("setup already completed")
	ErrSetupCannotReopen     = errors.New("setup cannot be reopened")
	ErrInvalidConfig         = errors.New("invalid config")
	ErrInvalidMode      = errors.New("invalid base mode")
	ErrInvalidName      = errors.New("invalid base name")
	ErrInvalidPath      = errors.New("invalid base path")
	ErrBaseNotFound     = errors.New("base not found")
	ErrBaseNameConflict = errors.New("base name conflict")
	ErrBasePathConflict = errors.New("base path conflict")
	ErrActiveBase       = errors.New("active base cannot be forgotten")
	ErrLastBase         = errors.New("last base cannot be forgotten")
	ErrRollbackFailed   = errors.New("rollback failed")
)

type FieldError struct {
	Kind    error
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Message }
func (e *FieldError) Unwrap() error { return e.Kind }
```

- [ ] **Step 4: Реализовать constructor, migration и safe snapshots**

Создать начало `settings_service.go`:

```go
package service

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"IGoNotes/internal/model"
)

type ConfigStore interface {
	Load() (*model.Config, error)
	Save(*model.Config) error
}

type BaseRuntime interface {
	GetBasePath() string
	SwitchBase(string) error
}

type SettingsService struct {
	mu     sync.RWMutex
	store  ConfigStore
	notes  BaseRuntime
	logger *log.Logger
	config model.Config
}

func NewSettingsService(store ConfigStore, notes BaseRuntime, activeBaseName string, logger *log.Logger) (*SettingsService, error) {
	config, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	if logger == nil {
		logger = log.Default()
	}
	if config.SetupCompleted == nil {
		completed := true
		config.SetupCompleted = &completed
		if err := store.Save(config); err != nil {
			return nil, fmt.Errorf("migrate setup state: %w", err)
		}
	}
	if activeBaseName != "" {
		index := baseIndex(*config, activeBaseName)
		if index < 0 {
			return nil, fmt.Errorf("active CLI base %q: %w", activeBaseName, ErrBaseNotFound)
		}
		if filepath.Clean(config.Bases[index].Path) != filepath.Clean(notes.GetBasePath()) {
			return nil, &FieldError{Kind: ErrInvalidConfig, Field: "current_base", Message: "Активная CLI-база не совпадает с runtime path"}
		}
		config.CurrentBase = activeBaseName
	}
	return &SettingsService{
		store: store, notes: notes, logger: logger, config: cloneConfig(*config),
	}, nil
}

func cloneConfig(config model.Config) model.Config {
	clone := config
	clone.Bases = append([]model.Base(nil), config.Bases...)
	if config.SetupCompleted != nil {
		completed := *config.SetupCompleted
		clone.SetupCompleted = &completed
	}
	return clone
}

func baseIndex(config model.Config, name string) int {
	for index := range config.Bases {
		if config.Bases[index].Name == name {
			return index
		}
	}
	return -1
}

func (s *SettingsService) GetConfig() model.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneConfig(s.config)
}

func (s *SettingsService) SetupCompleted() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.SetupCompleted != nil && *s.config.SetupCompleted
}

func (s *SettingsService) responseLocked() model.SettingsResponse {
	return model.SettingsResponse{Config: cloneConfig(s.config), BasePath: s.notes.GetBasePath()}
}
```

- [ ] **Step 5: Запустить GREEN migration tests**

Run: `gofmt -w internal/service/settings_errors.go internal/service/settings_service.go internal/service/settings_service_test.go`

Run: `go test ./internal/service -run 'TestNewSettingsService' -v`

Expected: PASS; nil мигрируется и атомарно сохраняется как true, explicit false не перезаписывается.

- [ ] **Step 6: Зафиксировать service ownership и migration**

```bash
git add internal/service/settings_errors.go internal/service/settings_service.go internal/service/settings_service_test.go
git commit -m "feat: migrate setup state in settings service"
```

### Task 6: Реализовать setup и добавление баз

**Files:**
- Modify: `internal/service/settings_service.go`
- Modify: `internal/service/settings_service_test.go`

- [ ] **Step 1: Написать RED table tests validation create/connect**

Добавить тесты, каждый с отдельным `t.TempDir()`:

```go
func TestSettingsServiceCompleteSetupCreatesAndConnectsBase(t *testing.T) {
	for _, mode := range []string{"create", "connect"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			defaultPath := filepath.Join(root, "default")
			if err := os.Mkdir(defaultPath, 0755); err != nil { t.Fatal(err) }
			path := root
			wantPath := filepath.Join(root, "work")
			if mode == "connect" {
				if err := os.Mkdir(wantPath, 0755); err != nil { t.Fatal(err) }
				path = wantPath
			}
			completed := false
			store := &fakeConfigStore{config: model.Config{
				BaseDir: root, Bases: []model.Base{{Name: "default", Path: defaultPath}},
				CurrentBase: "default", SetupCompleted: &completed,
			}}
			runtime := &fakeBaseRuntime{path: defaultPath, switchErr: map[string]error{}}
			svc, err := NewSettingsService(store, runtime, "", log.New(io.Discard, "", 0))
			if err != nil { t.Fatal(err) }
			got, err := svc.CompleteSetup(model.BaseMutationRequest{Mode: mode, Name: " work ", Path: path})
			if err != nil { t.Fatalf("CompleteSetup() error = %v", err) }
			if got.BasePath != wantPath || got.Config.CurrentBase != "work" || len(got.Config.Bases) != 1 {
				t.Fatalf("CompleteSetup() = %#v, want one active work base", got)
			}
			if got.Config.SetupCompleted == nil || !*got.Config.SetupCompleted {
				t.Fatal("setup_completed = false/nil, want true")
			}
		})
	}
}
```

Table `TestSettingsServiceCompleteSetupRejectsInvalidInput` должен вызвать `CompleteSetup` для: повторного setup (`ErrSetupAlreadyCompleted`), mode `other` (`ErrInvalidMode`), whitespace name (`ErrInvalidName`), `.`/`..`/`a/b`/`a\\b` create names (`ErrInvalidName`), отсутствующего parent create (`ErrInvalidPath`), существующего create target (`ErrBasePathConflict`), отсутствующего connect path и regular file (`ErrInvalidPath`). Для каждого case проверить `errors.Is`, неизменность store/runtime и отсутствие нового каталога.

- [ ] **Step 2: Запустить RED setup tests**

Run: `go test ./internal/service -run 'TestSettingsServiceCompleteSetup' -v`

Expected: FAIL при компиляции с `svc.CompleteSetup undefined`.

- [ ] **Step 3: Реализовать нормализацию и validation без создания каталогов**

Добавить private types/functions:

```go
type preparedBase struct {
	base    model.Base
	created bool
}

func prepareBase(request model.BaseMutationRequest) (preparedBase, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return preparedBase{}, &FieldError{Kind: ErrInvalidName, Field: "name", Message: "Имя базы не может быть пустым"}
	}
	if request.Mode != "create" && request.Mode != "connect" {
		return preparedBase{}, &FieldError{Kind: ErrInvalidMode, Field: "mode", Message: "Режим должен быть create или connect"}
	}
	if request.Mode == "create" && (name == "." || name == ".." || strings.ContainsAny(name, `/\\`)) {
		return preparedBase{}, &FieldError{Kind: ErrInvalidName, Field: "name", Message: "Имя новой базы не может содержать разделители пути"}
	}
	if strings.TrimSpace(request.Path) == "" {
		return preparedBase{}, &FieldError{Kind: ErrInvalidPath, Field: "path", Message: "Путь базы не может быть пустым"}
	}
	absolute, err := filepath.Abs(strings.TrimSpace(request.Path))
	if err != nil {
		return preparedBase{}, &FieldError{Kind: ErrInvalidPath, Field: "path", Message: "Не удалось определить абсолютный путь"}
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return preparedBase{}, &FieldError{Kind: ErrInvalidPath, Field: "path", Message: "Каталог не существует или недоступен"}
	}
	if request.Mode == "connect" {
		return preparedBase{base: model.Base{Name: name, Path: absolute}}, nil
	}
	target := filepath.Join(absolute, name)
	if _, err := os.Stat(target); err == nil {
		return preparedBase{}, &FieldError{Kind: ErrBasePathConflict, Field: "path", Message: "Каталог новой базы уже существует"}
	} else if !os.IsNotExist(err) {
		return preparedBase{}, &FieldError{Kind: ErrInvalidPath, Field: "path", Message: "Не удалось проверить каталог новой базы"}
	}
	return preparedBase{base: model.Base{Name: name, Path: target}, created: true}, nil
}

func ensureUniqueName(config model.Config, name, except string) error {
	for _, base := range config.Bases {
		if base.Name == name && base.Name != except {
			return &FieldError{Kind: ErrBaseNameConflict, Field: "name", Message: "База с таким именем уже существует"}
		}
	}
	return nil
}
```

- [ ] **Step 4: Реализовать transactional apply и setup**

```go
func (s *SettingsService) applyConfigLocked(next model.Config, targetPath string) error {
	oldPath := s.notes.GetBasePath()
	switched := targetPath != "" && filepath.Clean(targetPath) != filepath.Clean(oldPath)
	if switched {
		if err := s.notes.SwitchBase(targetPath); err != nil {
			return fmt.Errorf("switch note base: %w", err)
		}
	}
	if err := s.store.Save(&next); err != nil {
		if switched {
			if rollbackErr := s.notes.SwitchBase(oldPath); rollbackErr != nil {
				s.logger.Printf("Не удалось откатить базу с %q на %q: %v", targetPath, oldPath, rollbackErr)
				return fmt.Errorf("%w: save config: %v; switch rollback: %v", ErrRollbackFailed, err, rollbackErr)
			}
		}
		return fmt.Errorf("save config: %w", err)
	}
	s.config = cloneConfig(next)
	return nil
}

func (s *SettingsService) CompleteSetup(request model.BaseMutationRequest) (model.SettingsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if *s.config.SetupCompleted {
		return model.SettingsResponse{}, ErrSetupAlreadyCompleted
	}
	prepared, err := prepareBase(request)
	if err != nil {
		return model.SettingsResponse{}, err
	}
	if prepared.created {
		if err := os.Mkdir(prepared.base.Path, 0755); err != nil {
			return model.SettingsResponse{}, fmt.Errorf("create base directory: %w", err)
		}
	}
	completed := true
	next := model.Config{
		BaseDir: filepath.Dir(prepared.base.Path), Bases: []model.Base{prepared.base},
		CurrentBase: prepared.base.Name, SetupCompleted: &completed,
	}
	if err := s.applyConfigLocked(next, prepared.base.Path); err != nil {
		if prepared.created && filepath.Clean(s.notes.GetBasePath()) != filepath.Clean(prepared.base.Path) {
			if removeErr := os.Remove(prepared.base.Path); removeErr != nil {
				return model.SettingsResponse{}, fmt.Errorf("%v; remove new base rollback: %w", err, removeErr)
			}
		}
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}
```

- [ ] **Step 5: Написать RED/GREEN tests `AddBase` и cleanup**

Тесты должны проверить create и connect append без switch, case-sensitive duplicate name, сохранение `current_base`, cleanup только нового пустого create target при Save failure и сохранность подключённого каталога при той же ошибке.

Реализация:

```go
func (s *SettingsService) AddBase(request model.BaseMutationRequest) (model.SettingsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prepared, err := prepareBase(request)
	if err != nil {
		return model.SettingsResponse{}, err
	}
	if err := ensureUniqueName(s.config, prepared.base.Name, ""); err != nil {
		return model.SettingsResponse{}, err
	}
	if prepared.created {
		if err := os.Mkdir(prepared.base.Path, 0755); err != nil {
			return model.SettingsResponse{}, fmt.Errorf("create base directory: %w", err)
		}
	}
	next := cloneConfig(s.config)
	next.Bases = append(next.Bases, prepared.base)
	if err := s.applyConfigLocked(next, ""); err != nil {
		if prepared.created {
			if removeErr := os.Remove(prepared.base.Path); removeErr != nil {
				return model.SettingsResponse{}, fmt.Errorf("%v; remove new base rollback: %w", err, removeErr)
			}
		}
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}
```

Run: `go test ./internal/service -run 'TestSettingsService(CompleteSetup|AddBase)' -v`

Expected: PASS для create/connect, validation, no-switch и cleanup scenarios.

- [ ] **Step 6: Зафиксировать setup и add**

```bash
git add internal/service/settings_service.go internal/service/settings_service_test.go
git commit -m "feat: create and connect note bases"
```

### Task 7: Реализовать update, forget, switch и полный PUT config

**Files:**
- Modify: `internal/service/settings_service.go`
- Modify: `internal/service/settings_service_test.go`

- [ ] **Step 1: Написать RED-тесты update/forget/switch**

Добавить table tests со следующими точными assertions:

- rename inactive base меняет только config и не вызывает runtime switch;
- rename active base обновляет `current_base` и не вызывает switch при неизменном пути;
- path active base вызывает switch, сохраняет config, возвращает новый `base_path`;
- path inactive base не вызывает switch;
- missing update/switch/forget даёт `errors.Is(err, ErrBaseNotFound)`;
- duplicate update name даёт `ErrBaseNameConflict`;
- forget active даёт `ErrActiveBase`, forget при одной базе даёт `ErrLastBase`;
- successful forget не удаляет каталог и файлы внутри него;
- switch Save failure вызывает switch old path после target path и оставляет старый config;
- switch failure не вызывает Save.

- [ ] **Step 2: Запустить RED CRUD tests**

Run: `go test ./internal/service -run 'TestSettingsService(UpdateBase|ForgetBase|SwitchBase)' -v`

Expected: FAIL при компиляции: три метода отсутствуют.

- [ ] **Step 3: Реализовать exact-path validation и lookup**

```go
func normalizeExistingPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", &FieldError{Kind: ErrInvalidPath, Field: "path", Message: "Путь базы не может быть пустым"}
	}
	absolute, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", &FieldError{Kind: ErrInvalidPath, Field: "path", Message: "Не удалось определить абсолютный путь"}
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", &FieldError{Kind: ErrInvalidPath, Field: "path", Message: "Каталог не существует или недоступен"}
	}
	return absolute, nil
}

```

Для lookup использовать `baseIndex`, уже добавленный в Task 5; повторно его не объявлять.

- [ ] **Step 4: Реализовать update/forget/switch**

```go
func (s *SettingsService) UpdateBase(oldName string, request model.BaseUpdateRequest) (model.SettingsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := baseIndex(s.config, oldName)
	if index < 0 {
		return model.SettingsResponse{}, ErrBaseNotFound
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return model.SettingsResponse{}, &FieldError{Kind: ErrInvalidName, Field: "name", Message: "Имя базы не может быть пустым"}
	}
	if err := ensureUniqueName(s.config, name, oldName); err != nil {
		return model.SettingsResponse{}, err
	}
	path, err := normalizeExistingPath(request.Path)
	if err != nil {
		return model.SettingsResponse{}, err
	}
	next := cloneConfig(s.config)
	wasActive := next.CurrentBase == oldName
	next.Bases[index].Name = name
	next.Bases[index].Path = path
	if wasActive {
		next.CurrentBase = name
	}
	targetPath := ""
	if wasActive && filepath.Clean(path) != filepath.Clean(s.notes.GetBasePath()) {
		targetPath = path
	}
	if err := s.applyConfigLocked(next, targetPath); err != nil {
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}

func (s *SettingsService) ForgetBase(name string) (model.SettingsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := baseIndex(s.config, name)
	if index < 0 {
		return model.SettingsResponse{}, ErrBaseNotFound
	}
	if len(s.config.Bases) == 1 {
		return model.SettingsResponse{}, ErrLastBase
	}
	if s.config.CurrentBase == name {
		return model.SettingsResponse{}, ErrActiveBase
	}
	next := cloneConfig(s.config)
	next.Bases = append(next.Bases[:index], next.Bases[index+1:]...)
	if err := s.applyConfigLocked(next, ""); err != nil {
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}

func (s *SettingsService) SwitchBase(name string) (model.SettingsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := baseIndex(s.config, name)
	if index < 0 {
		return model.SettingsResponse{}, ErrBaseNotFound
	}
	next := cloneConfig(s.config)
	next.CurrentBase = name
	if err := s.applyConfigLocked(next, next.Bases[index].Path); err != nil {
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}
```

- [ ] **Step 5: Написать RED-тесты полного PUT**

`TestSettingsServiceReplaceConfig` должен покрыть: nil setup сохраняет false/true; explicit false после true -> `ErrSetupCannotReopen`; empty bases -> `ErrInvalidConfig`; missing current -> `ErrBaseNotFound`; duplicate names -> `ErrBaseNameConflict`; все base paths нормализуются absolute+clean и должны существовать; изменение active name/path переключает runtime; unchanged active path не переключает; Save failure откатывает runtime и in-memory config; rollback failure возвращает `ErrRollbackFailed` и содержит обе исходные ошибки в тексте.

- [ ] **Step 6: Реализовать full-config validation и replace**

```go
func normalizeConfig(input model.Config, currentSetup bool) (model.Config, error) {
	next := cloneConfig(input)
	if next.SetupCompleted == nil {
		completed := currentSetup
		next.SetupCompleted = &completed
	} else if currentSetup && !*next.SetupCompleted {
		return model.Config{}, &FieldError{Kind: ErrSetupCannotReopen, Field: "setup_completed", Message: "Завершённую настройку нельзя вернуть в false"}
	}
	if len(next.Bases) == 0 {
		return model.Config{}, &FieldError{Kind: ErrInvalidConfig, Field: "bases", Message: "Config должен содержать хотя бы одну базу"}
	}
	seen := make(map[string]struct{}, len(next.Bases))
	for index := range next.Bases {
		name := strings.TrimSpace(next.Bases[index].Name)
		if name == "" {
			return model.Config{}, &FieldError{Kind: ErrInvalidName, Field: "bases.name", Message: "Имя базы не может быть пустым"}
		}
		if _, exists := seen[name]; exists {
			return model.Config{}, &FieldError{Kind: ErrBaseNameConflict, Field: "bases.name", Message: "Имена баз должны быть уникальными"}
		}
		seen[name] = struct{}{}
		path, err := normalizeExistingPath(next.Bases[index].Path)
		if err != nil {
			return model.Config{}, &FieldError{Kind: ErrInvalidPath, Field: "bases.path", Message: "Путь каждой базы должен указывать на существующий каталог"}
		}
		next.Bases[index].Name = name
		next.Bases[index].Path = path
	}
	next.CurrentBase = strings.TrimSpace(next.CurrentBase)
	if _, exists := seen[next.CurrentBase]; !exists {
		return model.Config{}, &FieldError{Kind: ErrBaseNotFound, Field: "current_base", Message: "current_base должен ссылаться на настроенную базу"}
	}
	if strings.TrimSpace(next.BaseDir) != "" {
		absolute, err := filepath.Abs(strings.TrimSpace(next.BaseDir))
		if err != nil {
			return model.Config{}, &FieldError{Kind: ErrInvalidPath, Field: "base_dir", Message: "Не удалось определить абсолютный base_dir"}
		}
		next.BaseDir = filepath.Clean(absolute)
	}
	return next, nil
}

func (s *SettingsService) ReplaceConfig(input model.Config) (model.SettingsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := normalizeConfig(input, *s.config.SetupCompleted)
	if err != nil {
		return model.SettingsResponse{}, err
	}
	index := baseIndex(next, next.CurrentBase)
	targetPath := ""
	if filepath.Clean(next.Bases[index].Path) != filepath.Clean(s.notes.GetBasePath()) {
		targetPath = next.Bases[index].Path
	}
	if err := s.applyConfigLocked(next, targetPath); err != nil {
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}
```

- [ ] **Step 7: Запустить GREEN service suite с race detector**

Run: `gofmt -w internal/service/settings_service.go internal/service/settings_service_test.go`

Run: `go test -race ./internal/service -run 'TestSettingsService' -v`

Expected: PASS без race reports; rollback calls имеют порядок target, old; пользовательские каталоги forget/update не удаляются.

- [ ] **Step 8: Зафиксировать CRUD/switch/PUT**

```bash
git add internal/service/settings_service.go internal/service/settings_service_test.go
git commit -m "feat: manage note bases at runtime"
```

### Task 8: Ввести единые structured API errors

**Files:**
- Create: `internal/handlers/errors.go`
- Create: `internal/handlers/errors_test.go`
- Modify: `internal/handlers/note_handler.go:29-243`

- [ ] **Step 1: Написать RED table test status/code mapping**

```go
func TestWriteServiceError(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
		field  string
	}{
		{"setup required", service.ErrSetupRequired, 428, "setup_required", ""},
		{"setup repeated", service.ErrSetupAlreadyCompleted, 409, "setup_already_completed", ""},
		{"setup reopen", service.ErrSetupCannotReopen, 409, "setup_cannot_reopen", ""},
		{"invalid config", &service.FieldError{Kind: service.ErrInvalidConfig, Field: "bases", Message: "bad config"}, 422, "invalid_config", "bases"},
		{"invalid mode", &service.FieldError{Kind: service.ErrInvalidMode, Field: "mode", Message: "bad mode"}, 400, "invalid_mode", "mode"},
		{"invalid name", &service.FieldError{Kind: service.ErrInvalidName, Field: "name", Message: "bad name"}, 422, "invalid_base_name", "name"},
		{"invalid path", &service.FieldError{Kind: service.ErrInvalidPath, Field: "path", Message: "bad path"}, 422, "invalid_base_path", "path"},
		{"not found", service.ErrBaseNotFound, 404, "base_not_found", ""},
		{"name conflict", service.ErrBaseNameConflict, 409, "base_name_conflict", ""},
		{"path conflict", service.ErrBasePathConflict, 409, "base_path_conflict", ""},
		{"active", service.ErrActiveBase, 409, "active_base", ""},
		{"last", service.ErrLastBase, 409, "last_base", ""},
		{"rollback", service.ErrRollbackFailed, 500, "rollback_failed", ""},
		{"internal", errors.New("disk failed"), 500, "internal_error", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeServiceError(recorder, test.err)
			if recorder.Code != test.status { t.Fatalf("status = %d, want %d", recorder.Code, test.status) }
			var got model.APIError
			if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil { t.Fatal(err) }
			if got.Code != test.code || got.Field != test.field { t.Fatalf("error = %#v", got) }
			if got.Code == "internal_error" && got.Message == "disk failed" {
				t.Fatal("internal response leaked underlying error")
			}
		})
	}
}
```

- [ ] **Step 2: Запустить RED error test**

Run: `go test ./internal/handlers -run TestWriteServiceError -v`

Expected: FAIL при компиляции с `undefined: writeServiceError`.

- [ ] **Step 3: Реализовать JSON writer и mapping**

Создать `errors.go`:

```go
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

func WriteAPIError(w http.ResponseWriter, status int, code, message, field string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(model.APIError{Code: code, Message: message, Field: field})
}

func writeServiceError(w http.ResponseWriter, err error) {
	field := ""
	message := err.Error()
	var fieldErr *service.FieldError
	if errors.As(err, &fieldErr) {
		field = fieldErr.Field
		message = fieldErr.Message
	}
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, service.ErrSetupRequired): status, code = http.StatusPreconditionRequired, "setup_required"
	case errors.Is(err, service.ErrSetupAlreadyCompleted): status, code = http.StatusConflict, "setup_already_completed"
	case errors.Is(err, service.ErrSetupCannotReopen): status, code = http.StatusConflict, "setup_cannot_reopen"
	case errors.Is(err, service.ErrInvalidConfig): status, code = http.StatusUnprocessableEntity, "invalid_config"
	case errors.Is(err, service.ErrInvalidMode): status, code = http.StatusBadRequest, "invalid_mode"
	case errors.Is(err, service.ErrInvalidName): status, code = http.StatusUnprocessableEntity, "invalid_base_name"
	case errors.Is(err, service.ErrInvalidPath): status, code = http.StatusUnprocessableEntity, "invalid_base_path"
	case errors.Is(err, service.ErrBaseNotFound): status, code = http.StatusNotFound, "base_not_found"
	case errors.Is(err, service.ErrBaseNameConflict): status, code = http.StatusConflict, "base_name_conflict"
	case errors.Is(err, service.ErrBasePathConflict): status, code = http.StatusConflict, "base_path_conflict"
	case errors.Is(err, service.ErrActiveBase): status, code = http.StatusConflict, "active_base"
	case errors.Is(err, service.ErrLastBase): status, code = http.StatusConflict, "last_base"
	case errors.Is(err, service.ErrRollbackFailed): status, code, message = http.StatusInternalServerError, "rollback_failed", "Не удалось откатить переключение базы"
	default: message = "Внутренняя ошибка сервера"
	}
	WriteAPIError(w, status, code, message, field)
}
```

- [ ] **Step 4: Перевести существующий NoteHandler на JSON errors**

Каждый `http.Error` заменить конкретным вызовом `WriteAPIError`. Использовать коды: `method_not_allowed`/405, `bad_json`/400, `missing_field`/400 с field, `invalid_request`/400, `note_not_found`/404 для `os.IsNotExist`, `note_conflict`/409 для `ErrAlreadyExists`, `invalid_path`/400 для traversal, `file_too_large`/400, `missing_file`/400, `internal_error`/500 без текста underlying error.

`GetRawFile` заменить на descriptor-based serving:

```go
func (h *NoteHandler) GetRawFile(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		WriteAPIError(w, http.StatusBadRequest, "missing_field", "Не указан path", "path")
		return
	}
	file, info, err := h.NoteService.OpenRawFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			WriteAPIError(w, http.StatusBadRequest, "invalid_path", "Недопустимый путь", "path")
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			WriteAPIError(w, http.StatusNotFound, "file_not_found", "Файл не найден", "path")
			return
		}
		WriteAPIError(w, http.StatusInternalServerError, "internal_error", "Не удалось открыть файл", "")
		return
	}
	defer file.Close()
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
```

- [ ] **Step 5: Запустить GREEN handlers error tests и service tests**

Run: `gofmt -w internal/handlers/errors.go internal/handlers/errors_test.go internal/handlers/note_handler.go`

Run: `go test ./internal/handlers ./internal/service -v`

Expected: PASS; все явно формируемые API errors являются JSON, internal details не утекли.

- [ ] **Step 6: Зафиксировать structured errors**

```bash
git add internal/handlers/errors.go internal/handlers/errors_test.go internal/handlers/note_handler.go
git commit -m "feat: return structured api errors"
```

### Task 9: Добавить settings handlers

**Files:**
- Delete: `internal/handlers/config_handler.go`
- Create: `internal/handlers/settings_handler.go`
- Create: `internal/handlers/settings_handler_test.go`

- [ ] **Step 1: Написать RED handler integration tests**

Создать real service fixture с temp config, temp SQLite repository, `NoteService`, initial `SyncFS`, `SettingsService` и `SettingsHandler`. Покрыть:

- `GetConfig` -> 200 и direct config JSON boolean;
- malformed JSON -> 400 `bad_json`;
- missing `mode`, `name`, `path` setup/add -> 400 `missing_field` с точным field;
- successful setup -> 200 `SettingsResponse` и live base path;
- repeated setup -> 409 `setup_already_completed`;
- add/update/delete/switch success через соответствующий handler;
- missing `?name=` update/delete -> 400 `missing_field` field `name`;
- unknown switch -> 404 `base_not_found`;
- `SaveConfig` с отсутствующим `setup_completed` сохраняет текущее состояние.

RED-команда:

Run: `go test ./internal/handlers -run TestSettingsHandler -v`

Expected: FAIL при компиляции с `undefined: NewSettingsHandler`.

- [ ] **Step 2: Реализовать strict single-value decoder и response helper**

Начало `settings_handler.go`:

```go
type SettingsHandler struct {
	settings *service.SettingsService
}

func NewSettingsHandler(settings *service.SettingsService) *SettingsHandler {
	return &SettingsHandler{settings: settings}
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("body must contain one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func validateBaseMutationRequest(w http.ResponseWriter, request model.BaseMutationRequest) bool {
	for _, field := range []struct{ name, value string }{{"mode", request.Mode}, {"name", request.Name}, {"path", request.Path}} {
		if field.value == "" {
			WriteAPIError(w, http.StatusBadRequest, "missing_field", "Обязательное поле отсутствует", field.name)
			return false
		}
	}
	return true
}
```

- [ ] **Step 3: Реализовать семь handler methods**

```go
func (h *SettingsHandler) GetConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.settings.GetConfig())
}

func (h *SettingsHandler) CompleteSetup(w http.ResponseWriter, r *http.Request) {
	var request model.BaseMutationRequest
	if err := decodeJSON(r, &request); err != nil { WriteAPIError(w, 400, "bad_json", "Некорректный JSON", ""); return }
	if !validateBaseMutationRequest(w, request) { return }
	response, err := h.settings.CompleteSetup(request)
	if err != nil { writeServiceError(w, err); return }
	writeJSON(w, http.StatusOK, response)
}

func (h *SettingsHandler) AddBase(w http.ResponseWriter, r *http.Request) {
	var request model.BaseMutationRequest
	if err := decodeJSON(r, &request); err != nil { WriteAPIError(w, 400, "bad_json", "Некорректный JSON", ""); return }
	if !validateBaseMutationRequest(w, request) { return }
	response, err := h.settings.AddBase(request)
	if err != nil { writeServiceError(w, err); return }
	writeJSON(w, http.StatusOK, response)
}

func (h *SettingsHandler) UpdateBase(w http.ResponseWriter, r *http.Request) {
	oldName := r.URL.Query().Get("name")
	if oldName == "" { WriteAPIError(w, 400, "missing_field", "Не указано имя базы", "name"); return }
	var request model.BaseUpdateRequest
	if err := decodeJSON(r, &request); err != nil { WriteAPIError(w, 400, "bad_json", "Некорректный JSON", ""); return }
	if request.Name == "" || request.Path == "" { WriteAPIError(w, 400, "missing_field", "Обязательное поле отсутствует", map[bool]string{true: "name", false: "path"}[request.Name == ""]); return }
	response, err := h.settings.UpdateBase(oldName, request)
	if err != nil { writeServiceError(w, err); return }
	writeJSON(w, http.StatusOK, response)
}

func (h *SettingsHandler) ForgetBase(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" { WriteAPIError(w, 400, "missing_field", "Не указано имя базы", "name"); return }
	response, err := h.settings.ForgetBase(name)
	if err != nil { writeServiceError(w, err); return }
	writeJSON(w, http.StatusOK, response)
}

func (h *SettingsHandler) SwitchBase(w http.ResponseWriter, r *http.Request) {
	var request model.BaseSwitchRequest
	if err := decodeJSON(r, &request); err != nil { WriteAPIError(w, 400, "bad_json", "Некорректный JSON", ""); return }
	if request.Name == "" { WriteAPIError(w, 400, "missing_field", "Не указано имя базы", "name"); return }
	response, err := h.settings.SwitchBase(request.Name)
	if err != nil { writeServiceError(w, err); return }
	writeJSON(w, http.StatusOK, response)
}

func (h *SettingsHandler) SaveConfig(w http.ResponseWriter, r *http.Request) {
	var config model.Config
	if err := decodeJSON(r, &config); err != nil { WriteAPIError(w, 400, "bad_json", "Некорректный JSON", ""); return }
	response, err := h.settings.ReplaceConfig(config)
	if err != nil { writeServiceError(w, err); return }
	writeJSON(w, http.StatusOK, response)
}
```

- [ ] **Step 4: Удалить прямой ConfigHandler и проверить отсутствие bypass**

Удалить `internal/handlers/config_handler.go`.

Run: `git grep -n 'ConfigService' -- internal/handlers`

Expected: no output; HTTP layer знает только `SettingsService`.

- [ ] **Step 5: Запустить GREEN handler tests**

Run: `gofmt -w internal/handlers/settings_handler.go internal/handlers/settings_handler_test.go`

Run: `go test ./internal/handlers -run TestSettingsHandler -v`

Expected: PASS для GET/setup/base CRUD/switch/PUT и status/code assertions.

- [ ] **Step 6: Зафиксировать settings API handlers**

```bash
git add internal/handlers/config_handler.go internal/handlers/settings_handler.go internal/handlers/settings_handler_test.go
git commit -m "feat: expose setup and bases api"
```

### Task 10: Добавить dynamic 428 guard и тестируемый router

**Files:**
- Create: `internal/handlers/setup_guard.go`
- Create: `internal/handlers/setup_guard_test.go`
- Create: `internal/handlers/routes.go`
- Create: `internal/handlers/routes_test.go`

- [ ] **Step 1: Написать RED-тест guard, читающего актуальное состояние**

```go
type mutableSetupState struct {
	mu        sync.RWMutex
	completed bool
}
func (s *mutableSetupState) SetupCompleted() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.completed }
func (s *mutableSetupState) set(value bool) { s.mu.Lock(); s.completed = value; s.mu.Unlock() }

func TestRequireSetupReadsCurrentStateForEveryRequest(t *testing.T) {
	state := &mutableSetupState{}
	called := 0
	handler := RequireSetup(state, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called++; w.WriteHeader(204) }))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/notes", nil))
	if first.Code != 428 { t.Fatalf("first status = %d, want 428", first.Code) }
	var apiErr model.APIError
	if err := json.NewDecoder(first.Body).Decode(&apiErr); err != nil { t.Fatal(err) }
	if apiErr.Code != "setup_required" { t.Fatalf("code = %q", apiErr.Code) }

	state.set(true)
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/notes", nil))
	if second.Code != 204 || called != 1 { t.Fatalf("second status/calls = %d/%d", second.Code, called) }
}
```

- [ ] **Step 2: Запустить RED guard test**

Run: `go test ./internal/handlers -run TestRequireSetupReadsCurrentStateForEveryRequest -v`

Expected: FAIL при компиляции с `undefined: RequireSetup`.

- [ ] **Step 3: Реализовать guard contract**

```go
package handlers

import "net/http"

type SetupState interface {
	SetupCompleted() bool
}

func RequireSetup(state SetupState, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !state.SetupCompleted() {
			WriteAPIError(w, http.StatusPreconditionRequired, "setup_required", "Завершите первоначальную настройку", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 4: Написать RED route matrix test**

Создать fixture с handlers и SPA sentinel. До setup для каждого запроса проверить 428/code:

```go
guarded := []struct{ method, path string }{
	{http.MethodGet, "/api/notes"}, {http.MethodPost, "/api/notes"},
	{http.MethodGet, "/api/note?id=a.md"}, {http.MethodDelete, "/api/note?id=a.md"},
	{http.MethodPost, "/api/sync"}, {http.MethodGet, "/api/raw?path=a.png"},
	{http.MethodPost, "/api/save"}, {http.MethodPut, "/api/rename"},
	{http.MethodPost, "/api/assets"},
}
```

Доступные до setup: `GET/PUT /api/config`, `POST /api/setup`, `POST /api/bases`, `PUT/DELETE /api/bases`, `POST /api/bases/switch`, `GET /api/info`. Для method mismatch проверить 405 JSON `method_not_allowed`. После успешного setup повторить по одному корректному note request и убедиться, что ответ уже не 428.

- [ ] **Step 5: Реализовать router с method dispatcher**

```go
func methods(routes map[string]http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handler, ok := routes[r.Method]; ok {
			handler.ServeHTTP(w, r)
			return
		}
		WriteAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Метод не поддерживается", "")
	})
}

func NewRouter(note *NoteHandler, settings *SettingsHandler, state SetupState, spa http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	guard := func(handler http.HandlerFunc) http.Handler { return RequireSetup(state, handler) }

	mux.Handle("/api/info", methods(map[string]http.Handler{http.MethodGet: http.HandlerFunc(note.GetInfo)}))
	mux.Handle("/api/notes", methods(map[string]http.Handler{
		http.MethodGet: guard(note.GetNotes), http.MethodPost: guard(note.CreateNote),
	}))
	mux.Handle("/api/note", methods(map[string]http.Handler{
		http.MethodGet: guard(note.GetNote), http.MethodDelete: guard(note.DeleteNote),
	}))
	mux.Handle("/api/sync", methods(map[string]http.Handler{http.MethodPost: guard(note.SyncNotes)}))
	mux.Handle("/api/raw", methods(map[string]http.Handler{http.MethodGet: guard(note.GetRawFile)}))
	mux.Handle("/api/save", methods(map[string]http.Handler{http.MethodPost: guard(note.SaveNote)}))
	mux.Handle("/api/rename", methods(map[string]http.Handler{http.MethodPut: guard(note.RenameNote)}))
	mux.Handle("/api/assets", methods(map[string]http.Handler{http.MethodPost: guard(note.UploadAsset)}))

	mux.Handle("/api/config", methods(map[string]http.Handler{
		http.MethodGet: http.HandlerFunc(settings.GetConfig), http.MethodPut: http.HandlerFunc(settings.SaveConfig),
	}))
	mux.Handle("/api/setup", methods(map[string]http.Handler{http.MethodPost: http.HandlerFunc(settings.CompleteSetup)}))
	mux.Handle("/api/bases", methods(map[string]http.Handler{
		http.MethodPost: http.HandlerFunc(settings.AddBase),
		http.MethodPut: http.HandlerFunc(settings.UpdateBase),
		http.MethodDelete: http.HandlerFunc(settings.ForgetBase),
	}))
	mux.Handle("/api/bases/switch", methods(map[string]http.Handler{http.MethodPost: http.HandlerFunc(settings.SwitchBase)}))
	mux.Handle("/", spa)
	return mux
}
```

Не регистрировать `/api/system/select-directory`: второй plan получит возвращённый `*http.ServeMux` и вызовет `registerSystemRoutes(router, systemHandler)` до `ListenAndServe`. Экспортированный `WriteAPIError` является единым JSON helper для `SystemHandler` второго плана.

- [ ] **Step 6: Запустить GREEN guard/router tests**

Run: `gofmt -w internal/handlers/setup_guard.go internal/handlers/setup_guard_test.go internal/handlers/routes.go internal/handlers/routes_test.go`

Run: `go test ./internal/handlers -run 'Test(RequireSetup|Router)' -v`

Expected: PASS; все девять method+path combinations note API возвращают 428 до setup, settings/info доступны, guard открывается без restart.

- [ ] **Step 7: Зафиксировать routes и guard**

```bash
git add internal/handlers/setup_guard.go internal/handlers/setup_guard_test.go internal/handlers/routes.go internal/handlers/routes_test.go
git commit -m "feat: guard notes api until setup"
```

### Task 11: Подключить SettingsService и router в `main`

**Files:**
- Modify: `cmd/api/main.go:43-145`

- [ ] **Step 1: Зафиксировать RED compile после удаления ConfigHandler**

Run: `go test ./cmd/api -run '^$'`

Expected: FAIL при компиляции: `undefined: handlers.NewConfigHandler` и старый route wiring ссылается на удалённый handler.

- [ ] **Step 2: Создать SettingsService до запуска initial sync**

После `noteService := service.NewNoteService(noteRepo, basePath)` добавить:

```go
	settingsService, err := service.NewSettingsService(configService, noteService, *base, log.Default())
	if err != nil {
		log.Fatal("Ошибка инициализации настроек: ", err)
	}
```

Legacy config мигрируется до HTTP startup. Initial sync goroutine оставить: его write-lock сериализуется с будущим setup/switch.

- [ ] **Step 3: Заменить handlers/routes wiring**

Заменить создание handlers и весь блок `http.HandleFunc`:

```go
	noteHandler := handlers.NewNoteHandler(noteService)
	settingsHandler := handlers.NewSettingsHandler(settingsService)

	distFS, err := web.GetDistFS()
	if err != nil {
		log.Fatal("Ошибка инициализации статических файлов фронтенда:", err)
	}
	spaHandler := handlers.NewSPAHandler(distFS)
	router := handlers.NewRouter(noteHandler, settingsHandler, settingsService, spaHandler)
```

И заменить server call:

```go
	if err := http.ListenAndServe(address, router); err != nil {
		log.Fatal(err)
	}
```

Не использовать package-global `http.DefaultServeMux`.

- [ ] **Step 4: Проверить compile и package tests**

Run: `gofmt -w cmd/api/main.go`

Run: `go test ./cmd/api ./internal/handlers ./internal/service ./internal/repository`

Expected: PASS во всех четырёх packages.

- [ ] **Step 5: Проверить отсутствие mutation bypass и старого mux**

Run: `git grep -n -E 'NewConfigHandler|ConfigService \*service.ConfigService|http\.Handle(Func)?\(' -- cmd internal`

Expected: no output.

- [ ] **Step 6: Зафиксировать main wiring**

```bash
git add cmd/api/main.go
git commit -m "feat: wire runtime settings backend"
```

### Task 12: Полная race/build/smoke проверка backend

**Files:**
- Verify: `internal/model/config.go`
- Verify: `internal/model/api.go`
- Verify: `internal/repository/db.go`
- Verify: `internal/repository/note_repo.go`
- Verify: `internal/service/config_service.go`
- Verify: `internal/service/config_replace_unix.go`
- Verify: `internal/service/config_replace_windows.go`
- Verify: `internal/service/startup_service.go`
- Verify: `internal/service/note_service.go`
- Verify: `internal/service/settings_errors.go`
- Verify: `internal/service/settings_service.go`
- Verify: `internal/handlers/errors.go`
- Verify: `internal/handlers/settings_handler.go`
- Verify: `internal/handlers/setup_guard.go`
- Verify: `internal/handlers/routes.go`
- Verify: `cmd/api/main.go`

- [ ] **Step 1: Запустить полный race suite**

Run: `go test -race ./...`

Expected: PASS для всех packages без `DATA RACE`; `internal/handlers`, `internal/repository` и `internal/service` выполняют новые тесты.

- [ ] **Step 2: Запустить vet и cross-platform compile checks**

Run: `go vet ./...`

Expected: exit 0 без diagnostics.

Run: `GOOS=windows GOARCH=amd64 go test -c -o /tmp/opencode/service-windows.test.exe ./internal/service`

Expected: exit 0.

Run: `GOOS=darwin GOARCH=arm64 go test -c -o /tmp/opencode/service-darwin.test ./internal/service`

Expected: exit 0.

- [ ] **Step 3: Выполнить полную сборку**

Run: `make all`

Expected: npm/Vite и Go build завершаются с exit 0; `builds/igonotes` создан.

- [ ] **Step 4: Выполнить first-run smoke с изолированным окружением**

Run in a shell from repository root:

```bash
SMOKE_ROOT=$(mktemp -d /tmp/igonotes-setup-smoke.XXXXXX)
mkdir -p "$SMOKE_ROOT/home" "$SMOKE_ROOT/config"
HOME="$SMOKE_ROOT/home" XDG_CONFIG_HOME="$SMOKE_ROOT/config" ./builds/igonotes --port 18080 --no-browser >"$SMOKE_ROOT/server.log" 2>&1 &
SMOKE_PID=$!
for attempt in $(seq 1 50); do curl -fsS http://localhost:18080/api/config >"$SMOKE_ROOT/config.json" && break; sleep 0.1; done
grep -q '"setup_completed":false' "$SMOKE_ROOT/config.json"
test "$(curl -sS -o "$SMOKE_ROOT/notes-before.json" -w '%{http_code}' http://localhost:18080/api/notes)" = 428
grep -q '"code":"setup_required"' "$SMOKE_ROOT/notes-before.json"
test "$(curl -sS -o "$SMOKE_ROOT/setup.json" -w '%{http_code}' -H 'Content-Type: application/json' -d "{\"mode\":\"create\",\"name\":\"work\",\"path\":\"$SMOKE_ROOT\"}" http://localhost:18080/api/setup)" = 200
grep -q '"current_base":"work"' "$SMOKE_ROOT/setup.json"
grep -q '"setup_completed":true' "$SMOKE_ROOT/setup.json"
test "$(curl -sS -o "$SMOKE_ROOT/notes-after.json" -w '%{http_code}' http://localhost:18080/api/notes)" = 200
grep -q '^\[\]$' "$SMOKE_ROOT/notes-after.json"
test -d "$SMOKE_ROOT/work"
kill "$SMOKE_PID"
wait "$SMOKE_PID" || true
```

Expected: initial config has `"setup_completed":false`; notes-before body has code `setup_required`; setup response has current base `work` and `"setup_completed":true`; notes-after is `[]`; process terminates after `kill`.

- [ ] **Step 5: Проверить live switch smoke без restart**

Run:

```bash
SMOKE_SWITCH_ROOT=$(mktemp -d /tmp/igonotes-switch-smoke.XXXXXX)
mkdir -p "$SMOKE_SWITCH_ROOT/home" "$SMOKE_SWITCH_ROOT/config" "$SMOKE_SWITCH_ROOT/base-a" "$SMOKE_SWITCH_ROOT/base-b"
printf '%s' 'content-a' >"$SMOKE_SWITCH_ROOT/base-a/note.md"
printf '%s' 'content-b' >"$SMOKE_SWITCH_ROOT/base-b/note.md"
HOME="$SMOKE_SWITCH_ROOT/home" XDG_CONFIG_HOME="$SMOKE_SWITCH_ROOT/config" ./builds/igonotes --port 18081 --no-browser >"$SMOKE_SWITCH_ROOT/server.log" 2>&1 &
SMOKE_SWITCH_PID=$!
for attempt in $(seq 1 50); do curl -fsS http://localhost:18081/api/config >"$SMOKE_SWITCH_ROOT/config-before.json" && break; sleep 0.1; done
test "$(curl -sS -o "$SMOKE_SWITCH_ROOT/setup.json" -w '%{http_code}' -H 'Content-Type: application/json' -d "{\"mode\":\"connect\",\"name\":\"base-a\",\"path\":\"$SMOKE_SWITCH_ROOT/base-a\"}" http://localhost:18081/api/setup)" = 200
test "$(curl -sS -o "$SMOKE_SWITCH_ROOT/add.json" -w '%{http_code}' -H 'Content-Type: application/json' -d "{\"mode\":\"connect\",\"name\":\"base-b\",\"path\":\"$SMOKE_SWITCH_ROOT/base-b\"}" http://localhost:18081/api/bases)" = 200
test "$(curl -sS -o "$SMOKE_SWITCH_ROOT/switch.json" -w '%{http_code}' -H 'Content-Type: application/json' -d '{"name":"base-b"}' http://localhost:18081/api/bases/switch)" = 200
curl -fsS 'http://localhost:18081/api/note?id=note.md' >"$SMOKE_SWITCH_ROOT/note.json"
curl -fsS http://localhost:18081/api/info >"$SMOKE_SWITCH_ROOT/info.json"
grep -q 'content-b' "$SMOKE_SWITCH_ROOT/note.json"
grep -q 'base-b' "$SMOKE_SWITCH_ROOT/info.json"
grep -q '"current_base": "base-b"' "$SMOKE_SWITCH_ROOT/config/igonotes/config.json"
kill "$SMOKE_SWITCH_PID"
wait "$SMOKE_SWITCH_PID" || true
```

Expected: switch response, note content и `/api/info` указывают на `base-b` в том же process; config file содержит `"current_base": "base-b"`.

- [ ] **Step 6: Проверить formatting, scope и plan-independent picker absence**

Run: `gofmt -l cmd internal`

Expected: no output.

Run: `git diff --check`

Expected: exit 0 без output.

Run: `git grep -n 'api/system/select-directory\|DirectoryPicker\|zenity\|kdialog\|osascript\|FolderBrowserDialog' -- cmd internal`

Expected: no output; picker backend остаётся во втором плане.

- [ ] **Step 7: Сверить HTTP matrix и transactional invariants**

Run: `go test ./internal/handlers -run 'Test(SettingsHandler|Router|RequireSetup)' -count=1 -v`

Expected: PASS с кодами 400/404/409/422/428 и JSON `code/message/field`.

Run: `go test -race ./internal/service -run 'Test(NoteService|SettingsService)' -count=20`

Expected: PASS всех 20 повторов без race/deadlock; old-reader/switch test стабилен.

- [ ] **Step 8: Проверить итоговое состояние рабочей ветки**

Run: `git status --short`

Expected: только намеренные backend-файлы этой реализации; frontend, spec, другие планы и picker-файлы не изменены.
