# Git Synchronization Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the non-mutating and persistence foundation for optional per-base Git integration: backward-compatible contracts, a safe system-Git runner and porcelain client, validation and read-only probing, ordered SQLite migrations and persisted status, dedicated SettingsService Git ownership, REST routes, and startup wiring that never invokes Git unless a Git API or Git-settings command is called.

**Architecture:** `internal/git` is the only package allowed to launch and parse system Git commands; it receives argument slices, never shell strings, and returns bounded, redacted structured errors. `internal/service` owns orchestration: probe combines immutable settings with read-only porcelain calls, while `SettingsService` exclusively mutates Git config and coordinates persisted status. `internal/repository` applies ordered SQLite migrations and stores operational status by canonical repository path; HTTP handlers depend on narrow service interfaces and remain behind the existing local-origin and setup guards.

**Tech Stack:** Go 1.26, `os/exec`, `context`, `net/url`, `database/sql`, `modernc.org/sqlite`, `net/http`, standard `testing`/`httptest`, system Git 2.28+ for integration tests.

---

## Scope And Dependencies

This plan implements Plan 1 of `docs/superpowers/specs/2026-09-01-git-synchronization-design.md`:

- Backward-compatible `model.Base` fields and probe/config/status DTOs.
- A dedicated shell-free runner under `internal/git`.
- Non-interactive execution, local/network timeouts, bounded output, redaction, and safe structured errors.
- Git version, URL, branch, autosync interval, and commit-template validation.
- Read-only local/remote probe.
- Ordered SQLite migrations and persisted Git status.
- `SettingsService.ConfigureGit` and `SettingsService.DisableGit` as the only Git config mutation path.
- Safe status reconciliation when a base is renamed, moved, or forgotten.
- Probe/config/status HTTP routes with existing setup and local-origin guards.
- Production wiring that constructs Git dependencies but runs no Git during ordinary startup.

Explicitly excluded:

- Coordinator, NoteService filesystem transactions, and note revisions.
- Initialize/connect, fetch that writes objects, commit, merge, push, and backup refs.
- Manual sync, scheduler, circuit breaker, resume, and operation deduplication.
- Conflict inspection/resolution and recovery.
- Frontend work.

Dependencies and staged behavior:

- Existing `internal/service.SettingsService` remains the owner of `model.Config` and its lock ordering remains `SettingsService.mu -> BaseRuntime -> ConfigStore`.
- Existing `internal/repository.InitDB` is upgraded in place; legacy `notes` and `tags` data must survive.
- Existing `handlers.NewRouter` remains source-compatible. Git routes are registered separately, like system routes, to avoid changing every current router fixture.
- Because connect is excluded, successful `PUT /api/git/config` returns `200 OK`, persists validated settings, and records `needs_reconnect`. The connect plan will later enqueue initialization and return `202 Accepted` plus an operation ID.
- `GitConfirmations` is accepted in the DTO for forward compatibility but is neither persisted nor acted upon in this plan. Connect must re-probe and re-check confirmations against current state.
- A read-only probe cannot discover an unseen remote commit graph without fetching objects. It reports `history_relation: "unknown"` and conservatively sets `merge_histories: true` when both local and selected remote histories exist but the remote tip is unavailable locally. Connect must determine the exact relationship after fetch.
- `POST /api/git/probe` accepts an empty `git_branch` for the wizard's URL/authentication and remote-branch discovery pass. After the user selects or enters a branch, the wizard must probe again with that branch before review; `PUT /api/git/config` still requires and validates a nonempty branch. Frontend implementation remains excluded from this plan, but the backend contract and tests must support this two-pass flow.
- Remote inputs are limited to explicit `https://`, `http://`, `ssh://`, `git://`, `file://`, scp-like, and local-path forms. Git remote-helper syntax such as `ext::<address>` or any arbitrary `<helper>::<address>` is never accepted, and every runner invocation overrides `GIT_ALLOW_PROTOCOL` with the matching fixed protocol allowlist.

## Final Public Contracts

### Config Compatibility

```go
// internal/model/config.go
type Base struct {
	Name                     string `json:"name"`
	Path                     string `json:"path"`
	GitURL                   string `json:"git_url,omitempty"`
	GitBranch                string `json:"git_branch,omitempty"`
	AutoSync                 bool   `json:"auto_sync"`
	AutoSyncIntervalMinutes  int    `json:"auto_sync_interval_minutes,omitempty"`
	GitCommitMessageTemplate string `json:"git_commit_message_template,omitempty"`
}

func (b Base) GitConfigured() bool
```

A legacy base containing only `git_url` remains unconfigured because `git_branch` is empty. Existing `git_url` and `auto_sync` JSON names remain readable and unchanged.

### Service Contracts

```go
// internal/service/git_probe_service.go
func NewGitProbeService(SettingsSnapshot, GitPorcelain) *GitProbeService
func (s *GitProbeService) Probe(context.Context, model.GitProbeRequest) (model.GitProbeResponse, error)

// internal/service/settings_service.go
func NewSettingsServiceWithGit(
	ConfigStore,
	BaseRuntime,
	string,
	*log.Logger,
	GitConfigValidator,
	GitStatusStore,
) (*SettingsService, error)
func (s *SettingsService) ConfigureGit(context.Context, string, model.GitConfigRequest) (model.GitConfigResponse, error)
func (s *SettingsService) DisableGit(context.Context, string) (model.GitConfigResponse, error)

// internal/service/git_status_service.go
func NewGitStatusService(SettingsSnapshot, GitStatusReader) *GitStatusService
func (s *GitStatusService) Status(context.Context, string) (model.GitStatusResponse, error)
```

### REST Contracts

- `POST /api/git/probe`: explicit read-only Git and remote inspection.
- Probe requires `base` and `git_url`; `git_branch` is optional for discovery. A branchless response contains remote branches but no selected-branch mutation/history conclusions and cannot yet be configured.
- `PUT /api/git/config?base=<name>`: validate and persist Git settings; return `200` in this plan.
- `DELETE /api/git/config?base=<name>`: clear application settings/status without touching `.git`.
- `GET /api/git/status`: return all bases.
- `GET /api/git/status?base=<name>`: return one status or `404 base_not_found`.
- Every route uses `RequireLocalOrigin` and `RequireSetup`.
- API responses never include raw Git stderr, command lines, credentials, or internal causes.

## File Map

- Modify `internal/model/config.go:1-17`: backward-compatible Git fields and `GitConfigured`.
- Modify `internal/model/api.go:1-43`: Git request/response/status DTOs.
- Create `internal/model/git_test.go`: JSON compatibility and state contract tests.
- Create `internal/git/errors.go`: safe errors, classification, and redaction.
- Create `internal/git/runner.go`: shell-free process runner.
- Create `internal/git/runner_test.go`: argv, protocol/environment, timeout, limits, and redaction tests.
- Create `internal/git/porcelain.go`: Git 2.28+ read-only command facade and parsers.
- Create `internal/git/porcelain_test.go`: command and parser tests.
- Create `internal/service/git_config.go`: strict remote URL/interval/template validation and rendering.
- Create `internal/service/git_config_test.go`: config validation tests.
- Modify `internal/repository/db.go:12-48`: ordered migrations.
- Create `internal/repository/db_migrations_test.go`: fresh/legacy/rerun/rollback tests.
- Create `internal/repository/git_status_repo.go`: persisted status CRUD.
- Create `internal/repository/git_status_repo_test.go`: status round-trip tests.
- Create `internal/service/git_probe_service.go`: read-only probe orchestration.
- Create `internal/service/git_probe_service_test.go`: probe matrix tests.
- Create `internal/service/git_probe_integration_test.go`: real-Git immutability test.
- Modify `internal/service/settings_service.go:15-39,41-112,320-470,513-540`: Git dependencies, ownership, and status compensation.
- Modify `internal/service/settings_errors.go:5-20`: Git settings sentinels.
- Modify `internal/service/settings_service_test.go:16-177,1125-1477,1580-1979`: ownership/path/rollback tests.
- Create `internal/service/git_status_service.go`: config/status aggregation.
- Create `internal/service/git_status_service_test.go`: fallback/filter tests.
- Create `internal/handlers/git_handler.go`: probe/config/status handlers.
- Create `internal/handlers/git_handler_test.go`: handler tests.
- Create `internal/handlers/git_routes.go`: guarded route registration.
- Create `internal/handlers/git_routes_test.go`: method and guard matrix.
- Modify `internal/handlers/errors.go:3-62`: safe Git error mapping.
- Modify `internal/handlers/errors_test.go:18-58`: mapping and leak tests.
- Modify `cmd/api/main.go:17-20,73-118`: construct and register Git dependencies.
- Create `cmd/api/git_wiring_test.go`: prove construction runs no Git.

## Parallel Dispatch

Workers start from the same commit containing Task 1 and must edit only their exclusive files.

### Wave 0: Central Model Contract

- Worker 0 executes Task 1.
- Exclusive files: `internal/model/config.go`, `internal/model/api.go`, `internal/model/git_test.go`.
- Merge and run `go test ./internal/model` before dispatching Wave 1.

### Wave 1: Independent Foundations

Run concurrently after Wave 0:

- Worker A executes Task 2 and owns `internal/git/errors.go`, `internal/git/runner.go`, `internal/git/runner_test.go`.
- Worker B executes Task 4 and owns `internal/repository/db.go`, `internal/repository/db_migrations_test.go`, `internal/repository/git_status_repo.go`, `internal/repository/git_status_repo_test.go`.
- Worker C executes Task 5 and owns `internal/handlers/git_handler.go`, `internal/handlers/git_handler_test.go`, `internal/handlers/git_routes.go`, `internal/handlers/git_routes_test.go`.

Worker C uses narrow local interfaces and must not edit `internal/service`.

### Wave 2: Porcelain And Services

- After Worker A merges, Worker D executes Task 3 and owns `internal/git/porcelain.go`, `internal/git/porcelain_test.go`, `internal/service/git_config.go`, `internal/service/git_config_test.go`.
- After Workers A and D merge, Worker E executes Task 6 and owns the three `git_probe_*` service files.
- After Workers B and D merge, Worker F executes Task 7 and owns the listed SettingsService and GitStatusService files.
- Workers E and F may run concurrently because their file ownership is exclusive.

### Wave 3: Integration

- After Tasks 2-7 merge, Worker G executes Task 8.
- Exclusive files: `internal/handlers/errors.go`, `internal/handlers/errors_test.go`, `cmd/api/main.go`, `cmd/api/git_wiring_test.go`.
- The coordinator then executes Task 9.

Integration rules:

- Every worker runs focused package tests before returning.
- The coordinator reviews every diff before merging it.
- Workers do not reset, rebase, or edit another worker's files.
- Run `go test ./...` after every wave.
- Run race and cross-platform checks only after Wave 3 is integrated.

### Task 1: Define The Central Git Model Contract

**Files:**
- Modify: `internal/model/config.go:1-17`
- Modify: `internal/model/api.go:1-43`
- Create: `internal/model/git_test.go`

- [ ] **Step 1: Write legacy JSON and state tests**

Create `internal/model/git_test.go`:

```go
package model

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestBaseLegacyGitJSONRemainsReadableButUnconfigured(t *testing.T) {
	var base Base
	err := json.Unmarshal([]byte(`{
		"name":"work",
		"path":"/notes/work",
		"git_url":"git@example.com:notes.git",
		"auto_sync":true
	}`), &base)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if base.GitURL != "git@example.com:notes.git" || !base.AutoSync {
		t.Fatalf("legacy fields = %#v, want preserved values", base)
	}
	if base.GitBranch != "" || base.GitConfigured() {
		t.Fatalf("legacy base configured = %t with branch %q, want false", base.GitConfigured(), base.GitBranch)
	}
}

func TestBaseGitConfiguredRequiresURLAndBranch(t *testing.T) {
	tests := []struct {
		name string
		base Base
		want bool
	}{
		{name: "empty", base: Base{}, want: false},
		{name: "URL only", base: Base{GitURL: "origin"}, want: false},
		{name: "branch only", base: Base{GitBranch: "main"}, want: false},
		{name: "both", base: Base{GitURL: "origin", GitBranch: "main"}, want: true},
		{name: "whitespace URL", base: Base{GitURL: " ", GitBranch: "main"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.base.GitConfigured(); got != tt.want {
				t.Fatalf("GitConfigured() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestGitStatusJSONRoundTrip(t *testing.T) {
	attempt := time.Date(2026, 9, 1, 12, 30, 0, 0, time.UTC)
	status := GitStatus{
		Base: "work", State: GitStateNeedsReconnect, LastAttempt: &attempt,
		ChangedPaths: []string{"notes/one.md"},
		Error: &APIError{Code: "origin_mismatch", Message: "origin does not match configured remote"},
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded GitStatus
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, status) {
		t.Fatalf("round trip = %#v, want %#v", decoded, status)
	}
}
```

- [ ] **Step 2: Run RED model tests**

Run: `go test ./internal/model -run 'Test(Base|GitStatus)' -v`

Expected: FAIL to compile with `base.GitConfigured undefined` and `undefined: GitStatus`.

- [ ] **Step 3: Extend `model.Base` without renaming old JSON fields**

Replace `internal/model/config.go` with:

```go
package model

import "strings"

// Config представляет конфигурацию приложения
type Config struct {
	BaseDir        string `json:"base_dir"`
	Bases          []Base `json:"bases"`
	CurrentBase    string `json:"current_base"`
	SetupCompleted *bool  `json:"setup_completed"`
}

// Base представляет базу заметок
type Base struct {
	Name                     string `json:"name"`
	Path                     string `json:"path"`
	GitURL                   string `json:"git_url,omitempty"`
	GitBranch                string `json:"git_branch,omitempty"`
	AutoSync                 bool   `json:"auto_sync"`
	AutoSyncIntervalMinutes  int    `json:"auto_sync_interval_minutes,omitempty"`
	GitCommitMessageTemplate string `json:"git_commit_message_template,omitempty"`
}

func (b Base) GitConfigured() bool {
	return strings.TrimSpace(b.GitURL) != "" && strings.TrimSpace(b.GitBranch) != ""
}
```

- [ ] **Step 4: Add complete Git DTOs**

Add `import "time"` to `internal/model/api.go`, then append:

```go
type GitState string

const (
	GitStateUnconfigured   GitState = "unconfigured"
	GitStateInitializing   GitState = "initializing"
	GitStateReady          GitState = "ready"
	GitStateSyncing        GitState = "syncing"
	GitStateError          GitState = "error"
	GitStatePaused         GitState = "paused"
	GitStateConflict       GitState = "conflict"
	GitStateNeedsReconnect GitState = "needs_reconnect"
)

type GitProbeRequest struct {
	Base      string `json:"base"`
	GitURL    string `json:"git_url"`
	GitBranch string `json:"git_branch,omitempty"`
}

type GitConfirmations struct {
	CreateRepository bool `json:"create_repository"`
	ReplaceOrigin    bool `json:"replace_origin"`
	CreateBranch     bool `json:"create_branch"`
	MergeHistories   bool `json:"merge_histories"`
}

type GitConfigRequest struct {
	GitURL                   string           `json:"git_url"`
	GitBranch                string           `json:"git_branch"`
	AutoSync                 bool             `json:"auto_sync"`
	AutoSyncIntervalMinutes  int              `json:"auto_sync_interval_minutes"`
	GitCommitMessageTemplate string           `json:"git_commit_message_template"`
	Confirmations            GitConfirmations `json:"confirmations"`
}

type GitRequiredMutations struct {
	CreateRepository bool `json:"create_repository"`
	AddOrigin        bool `json:"add_origin"`
	ReplaceOrigin    bool `json:"replace_origin"`
	CreateBranch     bool `json:"create_branch"`
	MergeHistories   bool `json:"merge_histories"`
}

type GitProbeResponse struct {
	Base                  string               `json:"base"`
	GitVersion            string               `json:"git_version"`
	HasRepository         bool                 `json:"has_repository"`
	RepositoryRoot        string               `json:"repository_root,omitempty"`
	RepositoryRootMatches bool                 `json:"repository_root_matches"`
	CurrentBranch         string               `json:"current_branch,omitempty"`
	DetachedHead          bool                 `json:"detached_head"`
	WorkingTreeClean      bool                 `json:"working_tree_clean"`
	ExistingOriginURL     string               `json:"existing_origin_url,omitempty"`
	RemoteBranches        []string             `json:"remote_branches"`
	EmptyRemote           bool                 `json:"empty_remote"`
	PendingOperation      string               `json:"pending_operation,omitempty"`
	IdentityConfigured    bool                 `json:"identity_configured"`
	HistoryRelation       string               `json:"history_relation"`
	CanConfigure          bool                 `json:"can_configure"`
	RequiredMutations     GitRequiredMutations `json:"required_mutations"`
	Warnings              []string             `json:"warnings"`
	BlockingError         *APIError            `json:"blocking_error,omitempty"`
}

type GitStatus struct {
	Base                string     `json:"base"`
	RepositoryPath      string     `json:"repository_path,omitempty"`
	State               GitState   `json:"state"`
	OperationID         string     `json:"operation_id,omitempty"`
	Stage               string     `json:"stage,omitempty"`
	Ahead               int        `json:"ahead"`
	Behind              int        `json:"behind"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastAttempt         *time.Time `json:"last_attempt,omitempty"`
	LastSuccess         *time.Time `json:"last_success,omitempty"`
	ChangedPaths        []string   `json:"changed_paths"`
	RemoteOID           string     `json:"remote_oid,omitempty"`
	Error               *APIError  `json:"error,omitempty"`
}

type GitConfigResponse struct {
	Base   Base      `json:"base"`
	Status GitStatus `json:"status"`
}

type GitStatusResponse struct {
	Statuses []GitStatus `json:"statuses"`
}
```

- [ ] **Step 5: Format and run GREEN model tests**

Run:

```bash
gofmt -w internal/model/config.go internal/model/api.go internal/model/git_test.go
go test ./internal/model -v
```

Expected: PASS; legacy URL-only config remains readable and unconfigured.

- [ ] **Step 6: Commit the model contract**

```bash
git add internal/model/config.go internal/model/api.go internal/model/git_test.go
git commit -m "feat: define git synchronization contracts"
```

### Task 2: Add The Shell-Free Safe Git Runner

**Files:**
- Create: `internal/git/errors.go`
- Create: `internal/git/runner.go`
- Create: `internal/git/runner_test.go`

- [ ] **Step 1: Write RED runner tests**

Create an injected `commandFactory` and cover exact argv, environment replacement, local/network timeout selection, context cancellation, bounded stdout/stderr, executable-not-found, classification, and redaction. Assert every command, read-only or mutating, receives exactly one `GIT_TERMINAL_PROMPT=0`, `LC_ALL=C`, and `GIT_ALLOW_PROTOCOL=file:http:https:ssh:git`, replacing hostile inherited values; assert only read-only commands receive `GIT_OPTIONAL_LOCKS=0`. The shell-safety test must include:

```go
func TestCommandRunnerPassesArgumentsWithoutShell(t *testing.T) {
	var gotName string
	var gotArgs []string
	factory := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestGitRunnerHelper", "--", "success")
	}
	runner := newCommandRunner("git", time.Second, time.Second, 1024, factory)
	argument := `origin; touch /tmp/igonotes-must-not-exist`
	_, err := runner.Run(context.Background(), Command{
		Dir: t.TempDir(), Args: []string{"remote", "get-url", argument},
		Scope: LocalOperation, ReadOnly: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gotName != "git" {
		t.Fatalf("command name = %q, want git", gotName)
	}
	want := []string{"remote", "get-url", argument}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %#v, want %#v", gotArgs, want)
	}
}
```

Add `TestCommandRunnerRedactsBeforeDiagnosticTruncation` with a secret beginning just before the final diagnostic limit; the complete secret must not appear and no secret prefix may remain at the end.

Add `TestCommandRunnerOverridesProtocolEnvironmentForEveryCommand` with table cases for `ReadOnly:true` and `ReadOnly:false`. Seed `cmd.Env` through the factory with duplicate `GIT_ALLOW_PROTOCOL=ext:file` and `GIT_TERMINAL_PROMPT=1` entries, run the command, and assert the child environment contains one fixed allowlist and one disabled-prompt value in both cases.

- [ ] **Step 2: Run RED runner tests**

Run: `go test ./internal/git -run 'Test(CommandRunner|SafeError)' -v`

Expected: FAIL because package `internal/git` and its runner contracts do not exist.

- [ ] **Step 3: Define safe errors and deterministic classification**

Create `internal/git/errors.go` with:

```go
package git

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

type ErrorCode string

const (
	CodeUnavailable        ErrorCode = "git_unavailable"
	CodeUnsupportedVersion ErrorCode = "git_version_unsupported"
	CodeAuthentication     ErrorCode = "auth_failed"
	CodeRemoteUnreachable  ErrorCode = "remote_unreachable"
	CodeIdentityMissing    ErrorCode = "identity_missing"
	CodeInvalidBranch      ErrorCode = "invalid_branch"
	CodeRepositoryRoot     ErrorCode = "repository_root_mismatch"
	CodeRepositoryLocked   ErrorCode = "repository_locked"
	CodeCommandFailed      ErrorCode = "git_command_failed"
	CodeTimedOut           ErrorCode = "git_timeout"
	CodeCanceled           ErrorCode = "git_canceled"
	CodeNotRepository      ErrorCode = "not_a_git_repository"
)

type SafeError struct {
	Code       ErrorCode
	Message    string
	Field      string
	ExitCode   int
	diagnostic string
	cause      error
}

func (e *SafeError) Error() string      { return e.Message }
func (e *SafeError) Unwrap() error      { return e.cause }
func (e *SafeError) Diagnostic() string { return e.diagnostic }

var httpUserinfoPattern = regexp.MustCompile(`(?i)(https?://)[^/\s@]+@`)

func redact(text string, secrets []string) string {
	redacted := text
	for _, secret := range secrets {
		if secret != "" {
			redacted = strings.ReplaceAll(redacted, secret, "[REDACTED_REMOTE]")
		}
	}
	return httpUserinfoPattern.ReplaceAllString(redacted, `${1}[REDACTED]@`)
}

func classifyFailure(err error, diagnostic string) *SafeError {
	lower := strings.ToLower(diagnostic)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &SafeError{Code: CodeTimedOut, Message: "Git command timed out", cause: context.DeadlineExceeded}
	case errors.Is(err, context.Canceled):
		return &SafeError{Code: CodeCanceled, Message: "Git command was canceled", cause: context.Canceled}
	case strings.Contains(lower, "authentication failed"),
		strings.Contains(lower, "permission denied (publickey)"),
		strings.Contains(lower, "could not read username"),
		strings.Contains(lower, "terminal prompts disabled"):
		return &SafeError{Code: CodeAuthentication, Message: "Git authentication failed"}
	case strings.Contains(lower, "could not resolve host"),
		strings.Contains(lower, "unable to access"),
		strings.Contains(lower, "repository not found"),
		strings.Contains(lower, "does not appear to be a git repository"):
		return &SafeError{Code: CodeRemoteUnreachable, Message: "Git remote is unreachable"}
	case strings.Contains(lower, "index.lock"):
		return &SafeError{Code: CodeRepositoryLocked, Message: "Git repository is locked"}
	case strings.Contains(lower, "not a git repository"):
		return &SafeError{Code: CodeNotRepository, Message: "Directory is not a Git repository"}
	default:
		return &SafeError{Code: CodeCommandFailed, Message: "Git command failed"}
	}
}
```

`SafeError.Error()` must never include diagnostics. `LC_ALL=C` in the runner makes classification independent of host locale.

- [ ] **Step 4: Implement the runner contracts**

Create `internal/git/runner.go` around these complete types:

```go
const (
	DefaultLocalTimeout   = 15 * time.Second
	DefaultNetworkTimeout = 60 * time.Second
	DefaultOutputLimit    = 64 * 1024
	AllowedGitProtocols   = "file:http:https:ssh:git"
)

type OperationScope uint8

const (
	LocalOperation OperationScope = iota
	NetworkOperation
)

type Command struct {
	Dir      string
	Args     []string
	Scope    OperationScope
	ReadOnly bool
	Secrets  []string
}

type Result struct {
	Stdout          string
	Stderr          string
	StdoutTruncated bool
	StderrTruncated bool
}

type Runner interface {
	Run(context.Context, Command) (Result, error)
}

type commandFactory func(context.Context, string, ...string) *exec.Cmd

type CommandRunner struct {
	executable     string
	localTimeout   time.Duration
	networkTimeout time.Duration
	outputLimit    int
	command        commandFactory
}

func NewCommandRunner() *CommandRunner {
	return newCommandRunner("git", DefaultLocalTimeout, DefaultNetworkTimeout, DefaultOutputLimit, exec.CommandContext)
}
```

`Run` must:

1. Canonicalize `Command.Dir` with `filepath.Abs`, `filepath.EvalSymlinks`, and `os.Stat`; require an existing directory.
2. Select local or network timeout and derive a child context.
3. Call `exec.CommandContext` directly with cloned `Args`; never invoke `sh`, `bash`, `cmd /c`, or construct a command string.
4. Replace duplicate environment entries and always set `GIT_TERMINAL_PROMPT=0`, `LC_ALL=C`, and `GIT_ALLOW_PROTOCOL=AllowedGitProtocols`; set `GIT_OPTIONAL_LOCKS=0` only for read-only commands. Never append without removing inherited copies, because a caller-controlled duplicate must not change which value Git observes.
5. Bound stdout to `outputLimit` while continuing to consume writes and set `StdoutTruncated` when bytes were discarded.
6. Capture stderr to `outputLimit + longestSecretLength`, redact exact `Secrets` and HTTP userinfo, then truncate the redacted diagnostic to `outputLimit`. This ordering prevents a secret crossing the truncation boundary from leaking partially.
7. Return redacted stderr on success, set `StderrTruncated` when either capture or final truncation discarded bytes, and return no raw process output on failure.
8. Preserve `context.Canceled`/`DeadlineExceeded` through `Unwrap`.
9. Map `*exec.Error` to `git_unavailable` and `*exec.ExitError.ExitCode()` into `SafeError.ExitCode`.

Use this consuming bounded writer:

```go
type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if len(p) > remaining {
		b.truncated = true
	}
	if remaining > 0 {
		count := len(p)
		if count > remaining {
			count = remaining
		}
		_, _ = b.buffer.Write(p[:count])
	}
	return len(p), nil
}
```

- [ ] **Step 5: Run GREEN runner tests**

Run:

```bash
gofmt -w internal/git/errors.go internal/git/runner.go internal/git/runner_test.go
go test ./internal/git -run 'Test(CommandRunner|SafeError)' -v
```

Expected: PASS; exact argv is preserved, every command has the fixed protocol allowlist and disabled prompts, timeout errors unwrap correctly, and secrets are absent after redaction/truncation.

- [ ] **Step 6: Commit the runner**

```bash
git add internal/git/errors.go internal/git/runner.go internal/git/runner_test.go
git commit -m "feat: add safe system git runner"
```

### Task 3: Add Validation, Rendering, And Read-Only Porcelain

**Files:**
- Create: `internal/git/porcelain.go`
- Create: `internal/git/porcelain_test.go`
- Create: `internal/service/git_config.go`
- Create: `internal/service/git_config_test.go`

- [ ] **Step 1: Write RED config validation tests**

Cover:

- HTTP(S) userinfo, query, fragment, NUL, CR/LF, leading `-`, unknown URI schemes, malformed recognized URLs, and empty values are rejected.
- Remote-helper forms `ext::sh -c ...`, `hg::<address>`, and arbitrary `<helper>::<address>` are rejected rather than falling through as scp-like or local paths.
- Credential-free HTTPS/HTTP, SSH URL with an optional username but no password, `git://`, `file://`, scp-style `[user@]host:path`, absolute local path, and relative local path not beginning with `-` are accepted.
- Inputs containing `://` must use an allowed scheme; inputs containing remote-helper `::` syntax must be rejected unless the colons are inside the bracketed host of an otherwise valid allowed URL or scp-like form.
- Allowed intervals are exactly `5`, `15`, `30`, `60`.
- Disabled autosync accepts `0` or an allowed saved interval; enabled autosync rejects `0`.
- Empty template becomes the default.
- Whitespace-only, over 200 runes, CR/LF/NUL, incomplete tokens, and unknown variables are rejected.
- Rendering replaces every supported variable.

Representative rendering test:

```go
func TestRenderGitCommitMessage(t *testing.T) {
	at := time.Date(2026, 9, 1, 14, 30, 0, 0, time.FixedZone("UTC+3", 3*60*60))
	got, err := RenderGitCommitMessage(
		"IGoNotes: {{base}} {{branch}} {{date}} {{datetime}} {{count}}",
		GitCommitData{Base: "work", Branch: "main", Count: 3, Time: at},
	)
	if err != nil {
		t.Fatalf("RenderGitCommitMessage() error = %v", err)
	}
	want := "IGoNotes: work main 2026-09-01 2026-09-01T14:30:00+03:00 3"
	if got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}
```

Use a table-driven `TestValidateGitURLPolicy` whose accepted cases include `https://example.com/notes.git`, `http://example.com/notes.git`, `ssh://git@example.com/notes.git`, `git://example.com/notes.git`, `file:///srv/git/notes.git`, `git@example.com:notes.git`, `/srv/git/notes.git`, and `../notes.git`; rejected cases include `ext::sh -c touch /tmp/pwned`, `hg::https://example.com/notes`, `helper::address`, `ftp://example.com/notes.git`, `https://token@example.com/notes.git`, `ssh://user:password@example.com/notes.git`, and `https://example.com/notes.git?token=secret`.

- [ ] **Step 2: Run RED validation tests**

Run: `go test ./internal/service -run 'Test(ValidateGit|NormalizeGit|RenderGit)' -v`

Expected: FAIL with undefined validation/rendering functions.

- [ ] **Step 3: Implement config validation and rendering**

Create `internal/service/git_config.go` with:

```go
const DefaultGitCommitMessageTemplate = "IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)"

var (
	ErrInvalidGitURL      = errors.New("invalid git URL")
	ErrInvalidGitBranch   = errors.New("invalid git branch")
	ErrInvalidGitInterval = errors.New("invalid auto sync interval")
	ErrInvalidGitTemplate = errors.New("invalid git commit message template")
	ErrGitRepositoryInUse = errors.New("git repository in use")
)

type GitBranchValidator interface {
	ValidateBranch(context.Context, string, string) error
}

type GitConfigValidator interface {
	Validate(context.Context, string, model.GitConfigRequest) (model.GitConfigRequest, error)
}

type SettingsSnapshot interface {
	GetConfig() model.Config
}

type GitCommitData struct {
	Base   string
	Branch string
	Count  int
	Time   time.Time
}

func NewGitConfigValidator(GitBranchValidator) GitConfigValidator
func ValidateGitURL(string) error
func ValidateGitInterval(bool, int) error
func NormalizeGitCommitTemplate(string) (string, error)
func RenderGitCommitMessage(string, GitCommitData) (string, error)
```

Validation details:

- Trim URL and branch before storing.
- After rejecting controls, whitespace-only input, and leading `-`, classify the entire remote into exactly one accepted form; do not treat a failed URL or helper parse as a local path.
- For `https://` and `http://`, require a host and reject `url.User`, `RawQuery`, and `Fragment` so tokens cannot enter config.
- For `ssh://`, require a host, allow a username, reject passwords, query, and fragment. For `git://`, require a host and reject userinfo, query, and fragment. For `file://`, reject userinfo, query, and fragment and require a nonempty local path.
- Accept scp-like syntax only when it has a nonempty optional user, host (including a correctly bracketed IPv6 host), and path around the separator colon. Accept remaining values only as absolute or relative local paths after rejecting URL-shaped `://` input and helper-shaped `::` input.
- Reject `ext::<address>` and every arbitrary `<transport>::<address>` before execution. The accepted protocol set is exactly `file`, `http`, `https`, `ssh`, and `git`, matching `gitcmd.AllowedGitProtocols`; custom remote helpers are outside policy even if installed locally.
- Reject URL/branch beginning with `-` to prevent option injection even when a Git subcommand lacks a reliable `--` position.
- Config validation requires a nonempty branch and delegates final branch syntax to `GitBranchValidator`. Probe is intentionally different: it skips branch validation only when the trimmed branch is empty so URL/authentication and remote refs can be discovered first.
- Count template length with `utf8.RuneCountInString`.
- Parse every `{{...}}` token and allow only `base`, `branch`, `date`, `datetime`, `count`.
- Render with `strings.NewReplacer`, `time.RFC3339`, `2006-01-02`, and `strconv.Itoa`.

- [ ] **Step 4: Write RED porcelain tests**

Use a fake `git.Runner` and verify these exact read-only commands:

```text
git --version
git check-ref-format refs/heads/<branch>
git rev-parse --show-toplevel
git rev-parse --absolute-git-dir
git symbolic-ref --quiet --short HEAD
git status --porcelain=v1 -z --untracked-files=all
git remote get-url origin
git config --get user.name
git config --get user.email
git rev-parse --verify HEAD
git cat-file -e <remote-oid>^{commit}
git merge-base --is-ancestor <remote-oid> HEAD
git merge-base HEAD <remote-oid>
git ls-remote --symref <remote>
```

Verify every inspection command sets `ReadOnly:true`, `ls-remote` uses `NetworkOperation`, and the remote is listed in `Secrets`. Add table cases proving `ValidateBranch` accepts literal names such as `main` and `feature/editor`, rejects `@`, `HEAD`, `FETCH_HEAD`, `@{-1}`, `@{-2}`, `main@{1}`, `main..other`, `main~1`, `main^`, `main^{commit}`, `main:path`, `refs/heads/main`, and whitespace-surrounded ` main`, and never passes a rejected value to the runner. For accepted input, assert the exact command arguments are `[]string{"check-ref-format", "refs/heads/" + branch}` and that neither `--branch` nor `--normalize` is used.

- [ ] **Step 5: Implement the porcelain facade**

Create `internal/git/porcelain.go` with:

```go
type Version struct {
	Major int
	Minor int
	Patch int
	Raw   string
}

func (v Version) Supported() bool {
	return v.Major > 2 || v.Major == 2 && v.Minor >= 28
}

type LocalInspection struct {
	HasRepository      bool
	RepositoryRoot     string
	GitDir             string
	CurrentBranch      string
	DetachedHead       bool
	WorkingTreeClean   bool
	ExistingOriginURL  string
	PendingOperation   string
	IdentityConfigured bool
	HasCommits         bool
}

type RemoteInspection struct {
	Branches map[string]string
	Empty    bool
}

type Porcelain interface {
	Version(context.Context, string) (Version, error)
	ValidateBranch(context.Context, string, string) error
	InspectLocal(context.Context, string) (LocalInspection, error)
	InspectRemote(context.Context, string, string) (RemoteInspection, error)
	HistoryRelation(context.Context, string, string) (string, error)
}

type Client struct{ runner Runner }

func NewClient(runner Runner) *Client { return &Client{runner: runner} }
```

Rules:

- Parse `git version 2.28.0` and vendor suffixes such as `2.43.0.windows.1`.
- `ValidateBranch` accepts only a nonempty short branch name already stripped of surrounding whitespace; callers trim before validation, and the porcelain rejects rather than silently normalizes a value that still has surrounding whitespace. Before Git execution, reject leading `-`, fully qualified `refs/...` input, exact revision shorthands/pseudorefs `@`, `HEAD`, `FETCH_HEAD`, `ORIG_HEAD`, `MERGE_HEAD`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`, `REBASE_HEAD`, `AUTO_MERGE`, and `BISECT_HEAD`; matching is exact and case-sensitive because lowercase names are ordinary refs.
- Validate the literal full ref by invoking `git check-ref-format refs/heads/<branch>`, without `--branch` or `--normalize`. This prevents Git from expanding `@{-n}` and lets full-ref rules reject revision syntax including `@{`, `..`, `~`, `^`, `:`, `?`, `*`, `[`, backslash, controls, spaces, and malformed slash components.
- Convert every static or `check-ref-format` rejection to `invalid_branch`, field `git_branch`, without returning Git diagnostics.
- Treat `not_a_git_repository` from `--show-toplevel` as successful no-repository inspection.
- Canonicalize the returned repository root.
- Detect detached HEAD from `symbolic-ref` exit status.
- Treat any NUL-delimited status record as dirty.
- Resolve operation markers relative to `--absolute-git-dir`: `MERGE_HEAD`, `rebase-merge`, `rebase-apply`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`.
- Identity is configured only when both name and email are nonempty.
- Parse all `ls-remote --symref` records. `Empty` is true only when no refs are returned; tags without branches therefore make the remote nonempty. Store `refs/heads/<name>` as branch-to-OID entries and return deterministic sorted names at the service boundary.
- `HistoryRelation` returns `none`, `shared`, `unrelated`, or `unknown`. It may use `cat-file` and `merge-base` only when the remote OID already exists locally; a missing object returns `unknown`, never fetches.
- Reject any truncated `Result` before parsing version, refs, status, config, OIDs, or branch output; return a safe `git_command_failed` error with message `Git output exceeded the configured limit` rather than accepting partial machine data.

- [ ] **Step 6: Run GREEN validation and porcelain tests**

Run:

```bash
gofmt -w internal/git/porcelain.go internal/git/porcelain_test.go internal/service/git_config.go internal/service/git_config_test.go
go test ./internal/git ./internal/service -run 'Test(ValidateGit|ValidateBranch|NormalizeGit|RenderGit|Client|ParseGitVersion)' -v
```

Expected: PASS without requiring installed Git because commands are faked; helper remotes and revision-like branches never reach the runner.

- [ ] **Step 7: Commit validation and porcelain**

```bash
git add internal/git/porcelain.go internal/git/porcelain_test.go internal/service/git_config.go internal/service/git_config_test.go
git commit -m "feat: add git validation and read-only porcelain"
```

### Task 4: Add Ordered Migrations And Persisted Git Status

**Files:**
- Modify: `internal/repository/db.go:12-48`
- Create: `internal/repository/db_migrations_test.go`
- Create: `internal/repository/git_status_repo.go`
- Create: `internal/repository/git_status_repo_test.go`

- [ ] **Step 1: Write RED migration tests**

Test:

1. Fresh DB records versions `[1, 2]`.
2. A manually created current legacy schema plus one note upgrades without data loss.
3. Reopening does not duplicate versions.
4. An injected failing third migration rolls back both its schema and version row.
5. A manually seeded `schema_migrations` containing only `[2]` is rejected instead of being treated as fully migrated by `MAX(version)`.
6. Manually seeded rows `[1, 3]` are rejected as a non-prefix/unknown migration history.
7. A DB version greater than the highest known migration is rejected and closed.

In the malformed-history tests, create `schema_migrations` directly, insert the listed rows, close the seed connection, call `InitDB`, require an error containing `invalid schema migration history`, require the returned DB to be nil, and reopen the SQLite file separately to prove `InitDB` applied no schema or version changes. Query all successful histories with `SELECT version FROM schema_migrations ORDER BY version` and compare the complete slice, never only its maximum.

Run: `go test ./internal/repository -run 'TestInitDB(Migration|Upgrades|Rerun|Rejects)' -v`

Expected: FAIL because `schema_migrations` and `git_status` do not exist; `TestInitDBRejectsNonPrefixMigrationHistory` specifically guards against accepting `[2]` from a `MAX(version)` check.

- [ ] **Step 2: Replace ad hoc schema creation with ordered migrations**

Use:

```go
type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{version: 1, sql: `
		CREATE TABLE IF NOT EXISTS notes (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			path TEXT NOT NULL,
			parent_id TEXT,
			type TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS tags (
			note_id TEXT,
			tag TEXT,
			FOREIGN KEY(note_id) REFERENCES notes(id) ON DELETE CASCADE,
			UNIQUE(note_id, tag)
		);
	`},
	{version: 2, sql: `
		CREATE TABLE git_status (
			repository_path TEXT PRIMARY KEY,
			base_name TEXT NOT NULL,
			state TEXT NOT NULL CHECK(state IN (
				'unconfigured','initializing','ready','syncing','error','paused','conflict','needs_reconnect'
			)),
			operation_id TEXT NOT NULL DEFAULT '',
			stage TEXT NOT NULL DEFAULT '',
			ahead INTEGER NOT NULL DEFAULT 0 CHECK(ahead >= 0),
			behind INTEGER NOT NULL DEFAULT 0 CHECK(behind >= 0),
			consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK(consecutive_failures >= 0),
			last_attempt_unix_ms INTEGER,
			last_success_unix_ms INTEGER,
			changed_paths_json TEXT NOT NULL DEFAULT '[]',
			remote_oid TEXT NOT NULL DEFAULT '',
			error_code TEXT NOT NULL DEFAULT '',
			error_message TEXT NOT NULL DEFAULT '',
			error_field TEXT NOT NULL DEFAULT '',
			updated_at_unix_ms INTEGER NOT NULL
		);
		CREATE INDEX git_status_base_name_idx ON git_status(base_name);
	`},
}
```

`InitDB` creates `schema_migrations(version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)` and first verifies that the in-code `migrations` slice itself is ordered exactly `1..N`. Query every applied version with `SELECT version FROM schema_migrations ORDER BY version`, check `rows.Scan` and `rows.Err`, and require the complete result to equal exactly `[1, 2, ..., k]` for some `0 <= k <= N`. Reject gaps, histories starting above `1`, nonpositive versions, and versions above `N`; never infer validity from `MAX(version)` or `COUNT(*)`. Starting at index `k`, apply each remaining migration in its own transaction and insert that exact version in the same transaction. Close the DB on every initialization or validation error.

- [ ] **Step 3: Run GREEN migration tests**

Run:

```bash
gofmt -w internal/repository/db.go internal/repository/db_migrations_test.go
go test ./internal/repository -run 'TestInitDB(Migration|Upgrades|Rerun|Rejects)' -v
```

Expected: PASS; legacy note data remains, valid histories are exactly contiguous prefixes, malformed histories remain unchanged and are rejected, and the completed history is exactly `[1, 2]`.

- [ ] **Step 4: Write RED status repository tests**

Use a complete round trip:

```go
func TestGitStatusRepositoryRoundTrip(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil { t.Fatal(err) }
	defer db.Close()
	repo := NewGitStatusRepository(db)
	attempt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	status := model.GitStatus{
		Base: "work", RepositoryPath: filepath.Clean("/notes/work"),
		State: model.GitStatePaused, OperationID: "operation-1", Stage: "fetch",
		Ahead: 2, Behind: 1, ConsecutiveFailures: 5, LastAttempt: &attempt,
		ChangedPaths: []string{"one.md", "assets/image.png"}, RemoteOID: "0123456789abcdef",
		Error: &model.APIError{Code: "remote_unreachable", Message: "Git remote is unreachable"},
	}
	if err := repo.Upsert(context.Background(), status); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	got, found, err := repo.Get(context.Background(), status.RepositoryPath)
	if err != nil || !found || !reflect.DeepEqual(got, status) {
		t.Fatalf("Get() = %#v, %t, %v; want %#v", got, found, err, status)
	}
}
```

Also test replacement, sorted list, idempotent delete, malformed JSON, and closed DB errors.

- [ ] **Step 5: Implement `GitStatusRepository`**

Create:

```go
type GitStatusRepository struct {
	db  *sql.DB
	now func() time.Time
}

func NewGitStatusRepository(db *sql.DB) *GitStatusRepository
func (r *GitStatusRepository) Upsert(context.Context, model.GitStatus) error
func (r *GitStatusRepository) Get(context.Context, string) (model.GitStatus, bool, error)
func (r *GitStatusRepository) List(context.Context) ([]model.GitStatus, error)
func (r *GitStatusRepository) Delete(context.Context, string) error
```

Use one `INSERT ... ON CONFLICT(repository_path) DO UPDATE`, JSON `[]` for nil changed paths, Unix milliseconds for nullable times, UTC reconstruction, and an empty error triplet for nil `Error`.

- [ ] **Step 6: Run repository tests**

Run:

```bash
gofmt -w internal/repository/git_status_repo.go internal/repository/git_status_repo_test.go
go test ./internal/repository -v
```

Expected: PASS for existing note repository, migrations, and status persistence.

- [ ] **Step 7: Commit migrations and repository**

```bash
git add internal/repository/db.go internal/repository/db_migrations_test.go internal/repository/git_status_repo.go internal/repository/git_status_repo_test.go
git commit -m "feat: persist git status with ordered migrations"
```

### Task 5: Add Git HTTP Handlers And Routes

**Files:**
- Create: `internal/handlers/git_handler.go`
- Create: `internal/handlers/git_handler_test.go`
- Create: `internal/handlers/git_routes.go`
- Create: `internal/handlers/git_routes_test.go`

- [ ] **Step 1: Write RED handler tests over narrow fakes**

Test context forwarding, malformed/multiple JSON, missing fields, optional status filter, query validation, success status, and content type. Include a successful probe body with `{"base":"work","git_url":"https://example.com/notes.git"}` and assert the fake receives an empty `GitBranch`; separately assert configure rejects an empty `git_branch`. Define:

```go
type GitProber interface {
	Probe(context.Context, model.GitProbeRequest) (model.GitProbeResponse, error)
}

type GitConfigurer interface {
	ConfigureGit(context.Context, string, model.GitConfigRequest) (model.GitConfigResponse, error)
	DisableGit(context.Context, string) (model.GitConfigResponse, error)
}

type GitStatusReader interface {
	Status(context.Context, string) (model.GitStatusResponse, error)
}
```

- [ ] **Step 2: Run RED handler tests**

Run: `go test ./internal/handlers -run 'TestGitHandler' -v`

Expected: FAIL because `GitHandler` does not exist.

- [ ] **Step 3: Implement `GitHandler`**

Create:

```go
type GitHandler struct {
	prober     GitProber
	configurer GitConfigurer
	statuses   GitStatusReader
}

func NewGitHandler(prober GitProber, configurer GitConfigurer, statuses GitStatusReader) *GitHandler
func (h *GitHandler) Probe(http.ResponseWriter, *http.Request)
func (h *GitHandler) Configure(http.ResponseWriter, *http.Request)
func (h *GitHandler) Disable(http.ResponseWriter, *http.Request)
func (h *GitHandler) Status(http.ResponseWriter, *http.Request)
```

Reuse `decodeSingleJSON`, `writeJSON`, `writeBadJSON`, `writeMissingField`, and `writeServiceError`. Probe requires `base`, then `git_url`, but deliberately permits an omitted or empty `git_branch` for first-pass remote discovery; configure checks query `base` before body, then requires nonempty `git_url` and `git_branch`. Empty template is valid input because service supplies the default. All successful methods return `200` in this plan.

- [ ] **Step 4: Write RED route tests**

Test exact methods and sorted `Allow` headers:

```text
POST        /api/git/probe
PUT, DELETE /api/git/config
GET         /api/git/status
```

Assert `428 setup_required` before setup and `403 forbidden_origin` for cross-origin requests before handler execution.

- [ ] **Step 5: Implement guarded route registration**

Create `internal/handlers/git_routes.go`:

```go
package handlers

import "net/http"

func RegisterGitRoutes(mux *http.ServeMux, handler *GitHandler, state SetupState) {
	guarded := func(next http.Handler) http.Handler {
		return RequireSetup(state, next)
	}
	mux.Handle("/api/git/probe", RequireLocalOrigin(methods(map[string]http.Handler{
		http.MethodPost: guarded(http.HandlerFunc(handler.Probe)),
	})))
	mux.Handle("/api/git/config", RequireLocalOrigin(methods(map[string]http.Handler{
		http.MethodPut: guarded(http.HandlerFunc(handler.Configure)),
		http.MethodDelete: guarded(http.HandlerFunc(handler.Disable)),
	})))
	mux.Handle("/api/git/status", RequireLocalOrigin(methods(map[string]http.Handler{
		http.MethodGet: guarded(http.HandlerFunc(handler.Status)),
	})))
}
```

This ordering matches current `routes.go`: local-origin wraps the route, `methods` returns `405` before setup evaluation for unsupported methods, and `RequireSetup` protects only allowed methods.

- [ ] **Step 6: Run GREEN handler tests**

Run:

```bash
gofmt -w internal/handlers/git_handler.go internal/handlers/git_handler_test.go internal/handlers/git_routes.go internal/handlers/git_routes_test.go
go test ./internal/handlers -run 'TestGit(Handler|Routes)' -v
```

Expected: PASS; all routes use both existing guards.

- [ ] **Step 7: Commit handlers and routes**

```bash
git add internal/handlers/git_handler.go internal/handlers/git_handler_test.go internal/handlers/git_routes.go internal/handlers/git_routes_test.go
git commit -m "feat: add git probe config and status routes"
```

### Task 6: Implement The Read-Only Probe Service

**Files:**
- Create: `internal/service/git_probe_service.go`
- Create: `internal/service/git_probe_service_test.go`
- Create: `internal/service/git_probe_integration_test.go`

- [ ] **Step 1: Write RED probe matrix tests**

Cover unknown base, unsupported Git, exact/parent repo root, pending operations, missing identity, matching/missing/different origin, no repo, empty remote, absent selected branch, detached HEAD, dirty tree, and shared/unrelated/unknown history. Verify the interface exposes no mutating operation.

Model the wizard's two passes explicitly:

1. `TestGitProbeServiceDiscoversRemoteWithEmptyBranch` sends a valid URL and `GitBranch:""`; assert version, local inspection, and remote inspection all run, sorted remote branches are returned, branch validation and history relation do not run, `CreateBranch` and `MergeHistories` are false, `HistoryRelation` is `unknown`, `CanConfigure` is false, and `BlockingError` is nil.
2. `TestGitProbeServiceReprobesSelectedBranch` repeats the request with `GitBranch:"main"`; assert literal branch validation now runs before branch lookup, selected-branch existence/history/mutations are derived, and `CanConfigure` reflects all completed checks.
3. Branchless cases for both an empty remote and a nonempty remote must not claim that a branch exists, is absent, needs creation, has related history, or needs history merging.

- [ ] **Step 2: Run RED probe tests**

Run: `go test ./internal/service -run 'TestGitProbeService' -v`

Expected: FAIL because `GitProbeService` and `GitPorcelain` do not exist.

- [ ] **Step 3: Define immutable dependencies**

Create:

```go
type GitPorcelain interface {
	Version(context.Context, string) (gitcmd.Version, error)
	ValidateBranch(context.Context, string, string) error
	InspectLocal(context.Context, string) (gitcmd.LocalInspection, error)
	InspectRemote(context.Context, string, string) (gitcmd.RemoteInspection, error)
	HistoryRelation(context.Context, string, string) (string, error)
}

type GitProbeService struct {
	settings  SettingsSnapshot
	porcelain GitPorcelain
}

func NewGitProbeService(settings SettingsSnapshot, porcelain GitPorcelain) *GitProbeService
func (s *GitProbeService) Probe(context.Context, model.GitProbeRequest) (model.GitProbeResponse, error)
```

- [ ] **Step 4: Implement probe ordering and derivation**

Order:

1. Trim/validate base and resolve its canonical configured path.
2. Validate candidate URL without Git.
3. Run version check and require 2.28+.
4. Inspect local state.
5. If and only if the trimmed candidate branch is nonempty, validate it as a literal branch through Git.
6. Inspect remote refs even when the candidate branch is empty; this is the URL/authentication and branch-discovery pass.
7. Only for a branch validated in step 5, check it against discovered refs and determine history relation when local commits exist.
8. Derive branch-independent mutations/warnings/blockers, then selected-branch facts only when steps 5 and 7 ran.

Rules:

- No repository counts as `repository_root_matches:true`.
- Parent repository returns a complete `200` probe with `CanConfigure:false` and blocking `repository_root_mismatch`.
- Pending operation blocks with `repository_locked`; missing identity blocks with `identity_missing`.
- Missing origin sets `add_origin`; different origin sets `replace_origin`; no repo sets `create_repository`.
- Empty means no remote refs, not merely no branches. Empty remote sets `create_branch` only after a valid nonempty branch is selected.
- A nonempty remote requires the selected branch to exist only after selection; an empty branch is valid discovery input and must still return all remote branches.
- With no selected branch, do not call `ValidateBranch` or `HistoryRelation`; return `HistoryRelation:"unknown"`, `CreateBranch:false`, `MergeHistories:false`, `CanConfigure:false`, and no branch-related blocking error. Preserve any independent blocker such as unsafe URL, repository-root mismatch, pending operation, or missing identity.
- With a selected branch, validation must use literal `refs/heads/<branch>` semantics from Task 3. Then derive branch existence, `create_branch`, history relation, `merge_histories`, and branch-related warnings; only this second-pass response may set `CanConfigure:true`.
- Validate `ExistingOriginURL` before copying it into the response. If an externally configured HTTP(S) origin contains userinfo, query, or fragment, omit it, set `CanConfigure:false`, and return blocking `invalid_git_url`; never echo the unsafe value.
- Apply the complete Task 3 URL policy to candidate and existing-origin URLs, including rejection of `ext::` and arbitrary helper transports before `InspectRemote`; unsafe candidates must never reach the runner.
- `HistoryRelation` is `none`, `shared`, `unrelated`, or `unknown`; conservative unknown sets `merge_histories:true` and warning `Connecting may merge existing local and remote histories.`
- Always add `Git commits include every non-ignored file in the base directory.`
- Return branch names sorted and non-nil.
- Do not copy `SafeError.Diagnostic()` into payloads.

- [ ] **Step 5: Add a real-Git read-only integration test**

`TestGitProbeServiceIntegrationIsReadOnly` must skip when Git is unavailable or below 2.28, create temporary bare/working repos, configure local test identity, first probe with an empty branch to discover refs, then re-probe with the selected literal branch. Compare before/after bytes for `.git/config`, `git show-ref`, and `git status --porcelain=v1 -z`; assert the bare remote receives no new refs. The test environment must retain the runner's fixed `GIT_ALLOW_PROTOCOL=file:http:https:ssh:git` so the local bare remote works through the allowed `file` protocol.

- [ ] **Step 6: Run GREEN probe tests**

Run:

```bash
gofmt -w internal/service/git_probe_service.go internal/service/git_probe_service_test.go internal/service/git_probe_integration_test.go
go test ./internal/service -run 'TestGitProbeService' -v
```

Expected: PASS; branchless discovery and selected-branch re-probe both remain read-only, and only the real-Git test may SKIP for missing/old Git.

- [ ] **Step 7: Commit the probe**

```bash
git add internal/service/git_probe_service.go internal/service/git_probe_service_test.go internal/service/git_probe_integration_test.go
git commit -m "feat: add read-only git probe service"
```

### Task 7: Give SettingsService Exclusive Git Ownership

**Files:**
- Modify: `internal/service/settings_service.go:15-39,41-112,320-470,513-540`
- Modify: `internal/service/settings_errors.go:5-20`
- Modify: `internal/service/settings_service_test.go:16-177,1125-1477,1580-1979`
- Create: `internal/service/git_status_service.go`
- Create: `internal/service/git_status_service_test.go`

- [ ] **Step 1: Write RED ownership and compensation tests**

Test configure, rejection of empty or revision-like config branches, legacy URL-only activation, disable, no `.git` mutation, generic config protection, repository-path collision, `needs_reconnect`, move, rename, forget, status failure before save, config failure with status restore, restore failure degradation, and zero startup validator calls.

- [ ] **Step 2: Run RED SettingsService tests**

Run: `go test ./internal/service -run 'Test(SettingsService.*Git|NewSettingsServiceWithGit|GitStatusService)' -v`

Expected: FAIL because Git-aware constructor and methods do not exist.

- [ ] **Step 3: Add dependencies while preserving the old constructor**

Add:

```go
type GitStatusStore interface {
	Upsert(context.Context, model.GitStatus) error
	Get(context.Context, string) (model.GitStatus, bool, error)
	Delete(context.Context, string) error
}

type GitStatusReader interface {
	Get(context.Context, string) (model.GitStatus, bool, error)
	List(context.Context) ([]model.GitStatus, error)
}
```

Extend `SettingsService` with `gitValidator` and `gitStatuses`. Keep `NewSettingsService` as a wrapper passing nil Git dependencies to `NewSettingsServiceWithGit`. Constructor logic stores dependencies but never validates Git config or runs Git.

- [ ] **Step 4: Implement status compensation**

Snapshot affected status paths before mutation. Apply status delete/upsert before config persistence. If `applyConfigLocked` fails, restore every snapshot. If restoration fails, latch `ErrRollbackFailed` in `s.degraded`, join operation and rollback errors, and log only safe error values. Nil Git dependencies leave existing non-Git base operations unchanged.

- [ ] **Step 5: Implement `ConfigureGit`**

```go
func (s *SettingsService) ConfigureGit(
	ctx context.Context,
	name string,
	request model.GitConfigRequest,
) (model.GitConfigResponse, error)
```

Under `SettingsService.mu`: reject degraded state, resolve exact base, require validator/status store, require and validate/normalize a nonempty literal branch, reject a differently configured base at the same canonical path, clone config, assign all Git fields, upsert `needs_reconnect`, and persist. Do not probe, initialize, fetch, or enqueue. Do not persist confirmations. The frontend wizard is responsible for calling probe again after branch selection and before this command; the permissive branchless discovery contract must never make branch optional in persisted config.

- [ ] **Step 6: Implement `DisableGit`**

Clear exactly:

```go
base.GitURL = ""
base.GitBranch = ""
base.AutoSync = false
base.AutoSyncIntervalMinutes = 0
base.GitCommitMessageTemplate = ""
```

Delete persisted status with compensation, save config, return synthesized `unconfigured`, and never inspect/delete `.git` or user files.

- [ ] **Step 7: Protect dedicated fields from generic `ReplaceConfig`**

Match each incoming base to the current config by exact name first, then by unique canonical path. This permits a same-name path change and a same-path rename while rejecting an ambiguous simultaneous rename-and-move of a configured base. An unchanged Git field set is accepted for compatibility with GET-then-PUT clients. Any first difference returns `ErrInvalidConfig`, field path in this order: `git_url`, `git_branch`, `auto_sync`, `auto_sync_interval_minutes`, `git_commit_message_template`, with message `Git settings must be changed through /api/git/config`. A genuinely new base through generic config must have zero Git fields.

Update existing expectations around `settings_service_test.go:1580-1625` and `1731-1779`, which currently permit generic GitURL/AutoSync changes.

- [ ] **Step 8: Reconcile rename, path change, and forget**

- Same path rename: preserve status and update its `Base`.
- Configured path change: delete old status, create new `needs_reconnect`, preserve saved Git settings and autosync preference.
- Unconfigured path change: remove stale old status and create no row.
- Forget: delete status for the forgotten canonical path.
- `ReplaceConfig`: apply the same reconciliation to moved/renamed bases and delete status rows for removed bases.
- Every status mutation participates in compensation when config persistence fails.

- [ ] **Step 9: Implement status aggregation**

Create:

```go
type GitStatusService struct {
	settings SettingsSnapshot
	statuses GitStatusReader
}

func NewGitStatusService(settings SettingsSnapshot, statuses GitStatusReader) *GitStatusService
func (s *GitStatusService) Status(context.Context, string) (model.GitStatusResponse, error)
```

Return config-order statuses; optional exact-name filter returns `ErrBaseNotFound` when absent. Unconfigured bases synthesize `unconfigured` and ignore stale rows. Configured bases without a row synthesize `needs_reconnect`. Always return non-nil `ChangedPaths`. Never run Git.

- [ ] **Step 10: Run GREEN service tests**

Run:

```bash
gofmt -w internal/service/settings_service.go internal/service/settings_errors.go internal/service/settings_service_test.go internal/service/git_status_service.go internal/service/git_status_service_test.go
go test ./internal/service -run 'Test(SettingsService.*Git|NewSettingsServiceWithGit|GitStatusService)' -v
go test ./internal/service -v
```

Expected: PASS; all current non-Git settings tests remain green.

- [ ] **Step 11: Commit SettingsService ownership**

```bash
git add internal/service/settings_service.go internal/service/settings_errors.go internal/service/settings_service_test.go internal/service/git_status_service.go internal/service/git_status_service_test.go
git commit -m "feat: make settings service own git configuration"
```

### Task 8: Integrate Safe Errors And Production Wiring

**Files:**
- Modify: `internal/handlers/errors.go:3-62`
- Modify: `internal/handlers/errors_test.go:18-58`
- Modify: `cmd/api/main.go:17-20,73-118`
- Create: `cmd/api/git_wiring_test.go`

- [ ] **Step 1: Write RED error mapping and leak tests**

Add mappings:

| Error | HTTP | API code |
|---|---:|---|
| `git_unavailable` | 503 | `git_unavailable` |
| `git_version_unsupported` | 422 | `git_version_unsupported` |
| `auth_failed` | 401 | `auth_failed` |
| `remote_unreachable` | 502 | `remote_unreachable` |
| `identity_missing` | 422 | `identity_missing` |
| `invalid_branch` | 422 | `invalid_branch` |
| `repository_root_mismatch` | 409 | `repository_root_mismatch` |
| `repository_locked` | 409 | `repository_locked` |
| `git_timeout` | 504 | `git_timeout` |
| `git_canceled` | 408 | `git_canceled` |
| `ErrInvalidGitURL` | 422 | `invalid_git_url` |
| `ErrInvalidGitInterval` | 422 | `invalid_auto_sync_interval` |
| `ErrInvalidGitTemplate` | 422 | `invalid_commit_template` |
| `ErrGitRepositoryInUse` | 409 | `git_repository_in_use` |

Wrap a public-field `git.SafeError` in an outer error containing a secret URL; assert the body contains neither the wrapper text nor URL. Runner tests separately prove that private diagnostics are redacted. Preserve `ErrRollbackFailed` precedence.

- [ ] **Step 2: Run RED error tests**

Run: `go test ./internal/handlers -run TestWriteServiceError -v`

Expected: FAIL because Git errors currently map to `500 internal_error`.

- [ ] **Step 3: Extend `writeServiceError` safely**

Map service sentinels in `serviceErrorMappings`, then `errors.As` to `*git.SafeError`. Use only `Code`, `Message`, and `Field`; never call `Diagnostic()` for HTTP. Unknown Git codes and unknown errors remain `500 internal_error`.

- [ ] **Step 4: Write RED zero-startup-execution tests**

Create a counting runner:

```go
type countingGitRunner struct{ calls int }

func (r *countingGitRunner) Run(context.Context, gitcmd.Command) (gitcmd.Result, error) {
	r.calls++
	return gitcmd.Result{}, errors.New("unexpected Git execution")
}
```

Construct client, validator, probe service, status service, and handler; assert `calls == 0`. Add a source-order test that Git components and routes are constructed before `serveLocal`, and that `runServer` contains no direct `.Run(`, `.Probe(`, `.Version(`, `.InspectLocal(`, or `.InspectRemote(` call.

- [ ] **Step 5: Wire production dependencies without running Git**

In `cmd/api/main.go`, import `IGoNotes/internal/git` as `gitcmd`. After DB and NoteService construction:

```go
gitRunner := gitcmd.NewCommandRunner()
gitClient := gitcmd.NewClient(gitRunner)
gitStatusRepo := repository.NewGitStatusRepository(db)
gitValidator := service.NewGitConfigValidator(gitClient)

settingsService, err := service.NewSettingsServiceWithGit(
	configService,
	noteService,
	options.base,
	log.Default(),
	gitValidator,
	gitStatusRepo,
)
if err != nil {
	return fmt.Errorf("инициализировать сервис настроек: %w", err)
}

gitProbeService := service.NewGitProbeService(settingsService, gitClient)
gitStatusService := service.NewGitStatusService(settingsService, gitStatusRepo)
gitHandler := handlers.NewGitHandler(gitProbeService, settingsService, gitStatusService)
```

After `NewRouter` and before `registerSystemRoutes`:

```go
handlers.RegisterGitRoutes(router, gitHandler, settingsService)
```

Construction must not use `exec.LookPath`, inspect `.git`, contact a remote, or mutate status rows.

- [ ] **Step 6: Run GREEN integration tests**

Run:

```bash
gofmt -w internal/handlers/errors.go internal/handlers/errors_test.go cmd/api/main.go cmd/api/git_wiring_test.go
go test ./internal/handlers ./cmd/api -run 'Test(WriteServiceError|Git|MainWires)' -v
```

Expected: PASS; safe errors are structured and production construction records zero Git calls.

- [ ] **Step 7: Commit integration wiring**

```bash
git add internal/handlers/errors.go internal/handlers/errors_test.go cmd/api/main.go cmd/api/git_wiring_test.go
git commit -m "feat: wire git foundation without startup execution"
```

### Task 9: Verify Plan 1 End To End

**Files:**
- Verify only the files listed in this plan.

- [ ] **Step 1: Verify formatting**

Run: `test -z "$(gofmt -l internal/model internal/git internal/repository internal/service internal/handlers cmd/api)"`

Expected: exit 0 with no output.

- [ ] **Step 2: Run focused packages**

Run: `go test ./internal/model ./internal/git ./internal/repository ./internal/service ./internal/handlers ./cmd/api -v`

Expected: PASS; only the real-Git integration test may SKIP for missing Git or Git below 2.28.

- [ ] **Step 3: Run the full backend suite**

Run: `go test ./...`

Expected: PASS for all Go packages.

- [ ] **Step 4: Run race detection**

Run: `go test -race ./...`

Expected: PASS with no race reports.

- [ ] **Step 5: Verify Windows compilation**

Run:

```bash
GOOS=windows GOARCH=amd64 go test -c -o /tmp/opencode/git-windows.test.exe ./internal/git
GOOS=windows GOARCH=amd64 go test -c -o /tmp/opencode/api-windows.test.exe ./cmd/api
```

Expected: both commands exit 0; no shell-script or Unix-only runner dependency exists.

- [ ] **Step 6: Verify ordinary startup remains Git-optional**

Run:

```bash
go build -o /tmp/opencode/igonotes-no-git ./cmd/api
tmp_home="$(mktemp -d)"
HOME="$tmp_home" XDG_CONFIG_HOME="$tmp_home/config" PATH="/nonexistent" \
	/tmp/opencode/igonotes-no-git --no-browser --port 18081
```

Expected: server reaches `Сервер запущен` without `git_unavailable`; stop with `Ctrl-C`. No `.git` directory is created.

- [ ] **Step 7: Verify an explicit probe is the first Git execution point**

After completing setup and running the server on port `18081`:

```bash
curl -sS -X POST \
	-H 'Content-Type: application/json' \
	-H 'Origin: http://127.0.0.1:18081' \
	--data '{"base":"default","git_url":"/tmp/nonexistent.git"}' \
	http://127.0.0.1:18081/api/git/probe
```

Expected: structured probe failure such as `remote_unreachable`; no raw stderr or supplied remote appears in the error message. Before setup, the same route returns `428 setup_required` without invoking Git.

- [ ] **Step 8: Inspect scope and diff quality**

Run:

```bash
git status --short
git diff --stat
git diff --check
```

Expected: only listed backend files changed, no frontend files, no coordinator/sync/conflict/revision/scheduler/connect implementation, and `git diff --check` exits 0.

- [ ] **Step 9: Commit verification fixes only when needed**

If verification required code changes:

```bash
git add internal/model internal/git internal/repository internal/service internal/handlers cmd/api
git commit -m "test: verify git foundation"
```

If verification required no changes, do not create an empty commit.
