package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"IGoNotes/internal/model"
)

type ConfigStore interface {
	Load() (*model.Config, error)
	// Save only persists the supplied snapshot. It must not call BaseRuntime:
	// runtime persistence invokes Save while holding the runtime base lock.
	Save(*model.Config) error
}

type GitStatusStore interface {
	Upsert(context.Context, model.GitStatus) error
	Get(context.Context, string) (model.GitStatus, bool, error)
	List(context.Context) ([]model.GitStatus, error)
	Delete(context.Context, string) error
}

type GitStatusReader interface {
	Get(context.Context, string) (model.GitStatus, bool, error)
	List(context.Context) ([]model.GitStatus, error)
}

type BaseRuntime interface {
	GetBasePath() string
	SwitchBase(string) error
	baseMatches(string) (bool, error)
	persistConfig(string, ConfigStore, *model.Config) (matches bool, err error)
	switchBaseTransaction(string, ConfigStore, *model.Config, *model.Config) (operationErr, rollbackErr error)
}

type SettingsService struct {
	// Lock ordering: coordinator -> SettingsService.mu -> future NoteService.baseMu
	// -> repository/SQLite. Dependencies must not call back into an earlier layer.
	mu           sync.RWMutex
	store        ConfigStore
	notes        BaseRuntime
	coordinator  *BaseOperationCoordinator
	logger       *log.Logger
	gitValidator GitConfigValidator
	gitStatuses  GitStatusStore
	config       model.Config
	degraded     error
}

func NewSettingsService(
	store ConfigStore,
	notes BaseRuntime,
	coordinator *BaseOperationCoordinator,
	activeBaseName string,
	logger *log.Logger,
) (*SettingsService, error) {
	return NewSettingsServiceWithGit(store, notes, coordinator, activeBaseName, logger, nil, nil)
}

// NewSettingsServiceWithGit may call BaseRuntime without holding coordinator
// only during this constructor, before the returned service can be published.
func NewSettingsServiceWithGit(
	store ConfigStore,
	notes BaseRuntime,
	coordinator *BaseOperationCoordinator,
	activeBaseName string,
	logger *log.Logger,
	gitValidator GitConfigValidator,
	gitStatuses GitStatusStore,
) (*SettingsService, error) {
	if store == nil {
		return nil, fmt.Errorf("config store: %w", ErrInvalidConfig)
	}
	if notes == nil {
		return nil, fmt.Errorf("base runtime: %w", ErrInvalidConfig)
	}
	if coordinator == nil {
		return nil, fmt.Errorf("base operation coordinator: %w", ErrInvalidConfig)
	}

	loadedConfig, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	if loadedConfig == nil {
		return nil, fmt.Errorf("load settings: %w: store returned nil config", ErrInvalidConfig)
	}
	config := cloneConfig(*loadedConfig)
	effectiveBaseName := config.CurrentBase
	if activeBaseName != "" {
		effectiveBaseName = activeBaseName
	}
	expectedPath, err := configuredBasePath(config, effectiveBaseName)
	if err != nil {
		return nil, fmt.Errorf("current base %q: %w", effectiveBaseName, err)
	}

	migratedSetup := config.SetupCompleted == nil
	if migratedSetup {
		completed := config.BaseDir != "" || len(config.Bases) != 0 || config.CurrentBase != ""
		config.SetupCompleted = &completed
		matches, err := notes.persistConfig(expectedPath, store, &config)
		if err != nil {
			return nil, fmt.Errorf("migrate setup state: %w", err)
		}
		if !matches {
			return nil, fmt.Errorf("migrate setup state: %w", ErrRuntimePathChanged)
		}
	}

	if !migratedSetup {
		matches, err := notes.baseMatches(expectedPath)
		if err != nil {
			return nil, fmt.Errorf("inspect current runtime base: %w", err)
		}
		if !matches {
			return nil, &FieldError{
				Kind:    ErrInvalidConfig,
				Field:   "current_base",
				Message: fmt.Sprintf("current base %q does not match the runtime base path", effectiveBaseName),
			}
		}
	}

	if activeBaseName != "" {
		config.CurrentBase = activeBaseName
	}

	if logger == nil {
		logger = log.Default()
	}

	return &SettingsService{
		store:        store,
		notes:        notes,
		coordinator:  coordinator,
		logger:       logger,
		gitValidator: gitValidator,
		gitStatuses:  gitStatuses,
		config:       cloneConfig(config),
	}, nil
}

func (s *SettingsService) lockMutation() {
	s.coordinator.Lock()
	s.mu.Lock()
}

func (s *SettingsService) unlockMutation() {
	s.mu.Unlock()
	s.coordinator.Unlock()
}

func (s *SettingsService) GetConfig() model.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneConfig(s.config)
}

func (s *SettingsService) ReadConfigSnapshot(read func(model.Config) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return read(cloneConfig(s.config))
}

func (s *SettingsService) SetupCompleted() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.SetupCompleted != nil && *s.config.SetupCompleted
}

func (s *SettingsService) responseLocked() model.SettingsResponse {
	return model.SettingsResponse{
		Config:   cloneConfig(s.config),
		BasePath: s.notes.GetBasePath(),
	}
}

type preparedBase struct {
	base   model.Base
	create bool
}

func prepareBase(request model.BaseMutationRequest) (preparedBase, error) {
	mode := request.Mode
	name := strings.TrimSpace(request.Name)
	path := strings.TrimSpace(request.Path)
	if mode != "create" && mode != "connect" {
		return preparedBase{}, fieldError(ErrInvalidMode, "mode", "mode must be create or connect")
	}
	if name == "" {
		return preparedBase{}, fieldError(ErrInvalidName, "name", "base name is required")
	}
	if mode == "create" && (name == "." || name == ".." || strings.ContainsAny(name, `/\`)) {
		return preparedBase{}, fieldError(ErrInvalidName, "name", "base name cannot contain path separators")
	}
	if path == "" {
		return preparedBase{}, fieldError(ErrInvalidPath, "path", "base path is required")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return preparedBase{}, fieldErrorWithCause(ErrInvalidPath, err, "path", "resolve base path")
	}
	absPath = filepath.Clean(absPath)
	canonicalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return preparedBase{}, fieldErrorWithCause(ErrInvalidPath, err, "path", "resolve base path symlinks")
	}
	canonicalPath = filepath.Clean(canonicalPath)
	info, err := os.Stat(canonicalPath)
	if err != nil {
		return preparedBase{}, fieldErrorWithCause(ErrInvalidPath, err, "path", "inspect base path")
	}
	if !info.IsDir() {
		return preparedBase{}, fieldError(ErrInvalidPath, "path", "base path must be an existing directory")
	}
	if mode == "connect" {
		return preparedBase{base: model.Base{Name: name, Path: canonicalPath}}, nil
	}

	targetPath := filepath.Join(canonicalPath, name)
	_, err = os.Stat(targetPath)
	switch {
	case err == nil:
		return preparedBase{}, fieldError(ErrBasePathConflict, "path", "base path already exists")
	case !errors.Is(err, os.ErrNotExist):
		return preparedBase{}, fieldErrorWithCause(ErrInvalidPath, err, "path", "inspect base path")
	default:
		return preparedBase{base: model.Base{Name: name, Path: targetPath}, create: true}, nil
	}
}

func createBaseDirectory(prepared preparedBase) error {
	root, err := os.OpenRoot(filepath.Dir(prepared.base.Path))
	if err != nil {
		return fieldErrorWithCause(ErrInvalidPath, err, "path", "open base parent")
	}
	mkdirErr := root.Mkdir(prepared.base.Name, 0o755)
	closeErr := root.Close()
	if mkdirErr != nil {
		cause := errors.Join(mkdirErr, closeErr)
		if errors.Is(mkdirErr, os.ErrExist) {
			return fieldErrorWithCause(ErrBasePathConflict, cause, "path", "base path already exists")
		}
		return fieldErrorWithCause(ErrInvalidPath, cause, "path", "create base path")
	}
	if closeErr != nil {
		return fieldErrorWithCause(ErrInvalidPath, closeErr, "path", "close base parent after creating base")
	}
	return nil
}

func (s *SettingsService) applyConfigLocked(next model.Config, targetPath string) error {
	conflicts := captureConflictReconciliationSnapshot(s.config, next, nil)
	return s.applyConfigWithConflictSnapshotLocked(next, targetPath, conflicts)
}

func (s *SettingsService) applyConfigWithConflictSnapshotLocked(
	next model.Config,
	targetPath string,
	conflicts conflictReconciliationSnapshot,
) error {
	expectedPath, err := configuredBasePath(next, next.CurrentBase)
	if err != nil {
		return err
	}
	matches, err := s.notes.persistConfig(expectedPath, s.store, &next)
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	if matches {
		s.publishConfigLocked(next, conflicts)
		return nil
	}
	if targetPath == "" {
		return ErrRuntimePathChanged
	}

	canonicalTarget, err := canonicalExistingDirectory(targetPath)
	if err != nil {
		return fmt.Errorf("resolve target runtime base %q: %w", targetPath, err)
	}

	previous := cloneConfig(s.config)
	operationErr, rollbackErr := s.notes.switchBaseTransaction(canonicalTarget, s.store, &next, &previous)
	if rollbackErr != nil {
		s.degraded = errors.Join(
			ErrRollbackFailed,
			operationErr,
			fmt.Errorf("restore runtime base: %w", rollbackErr),
		)
		s.logger.Printf("settings runtime rollback failed: %v", s.degraded)
		return s.degraded
	}
	if operationErr != nil {
		return operationErr
	}

	s.publishConfigLocked(next, conflicts)
	return nil
}

type conflictReconciliationSnapshot struct {
	entriesToClear []conflictPathResolution
}

type conflictPathResolution struct {
	path      string
	info      os.FileInfo
	stable    bool
	preferred bool
}

func captureConflictReconciliationSnapshot(current, next model.Config, stableGitIdentities map[string]string) conflictReconciliationSnapshot {
	identities := make(map[string]conflictPathResolution, len(current.Bases)+len(next.Bases))
	for _, base := range current.Bases {
		identity, ok := stableGitIdentities[base.Name]
		if !ok || identity == "" || base.Path == "" || !base.GitConfigured() {
			continue
		}
		stablePath := filepath.Clean(identity)
		resolution := resolveConflictPathIdentity(stablePath)
		resolution.path = stablePath
		resolution.stable = true
		resolution.preferred = true
		identities[filepath.Clean(base.Path)] = resolution
	}
	resolveIdentity := func(path string) conflictPathResolution {
		cleanedPath := filepath.Clean(path)
		if identity, ok := identities[cleanedPath]; ok {
			return identity
		}
		resolution := resolveConflictPathIdentity(cleanedPath)
		identities[cleanedPath] = resolution
		return resolution
	}
	retainedPaths := make([]conflictPathResolution, 0, len(next.Bases))
	unknownRetainedPath := false
	for _, base := range next.Bases {
		if base.Path != "" {
			identity := resolveIdentity(base.Path)
			retainedPaths = append(retainedPaths, identity)
			unknownRetainedPath = unknownRetainedPath || !identity.stable || filepath.Clean(base.Path) != identity.path
		}
	}
	// A broken retained alias may resolve to a removed identity when restored.
	// Keep the in-memory conflict gate until restart rather than clear it unsafely.
	if unknownRetainedPath {
		return conflictReconciliationSnapshot{}
	}
	clearIdentities := make([]conflictPathResolution, 0, len(current.Bases))
	for _, base := range current.Bases {
		if base.Path == "" {
			continue
		}
		identity := resolveIdentity(base.Path)
		if !identity.stable || containsConflictIdentity(retainedPaths, identity) {
			continue
		}
		duplicate := conflictIdentityIndex(clearIdentities, identity)
		if duplicate < 0 {
			clearIdentities = append(clearIdentities, identity)
			continue
		}
		if identity.preferred && !clearIdentities[duplicate].preferred {
			clearIdentities[duplicate] = identity
		}
	}
	return conflictReconciliationSnapshot{entriesToClear: clearIdentities}
}

func containsConflictIdentity(identities []conflictPathResolution, target conflictPathResolution) bool {
	return conflictIdentityIndex(identities, target) >= 0
}

func conflictIdentityIndex(identities []conflictPathResolution, target conflictPathResolution) int {
	for index, identity := range identities {
		if sameConflictIdentity(identity, target) {
			return index
		}
	}
	return -1
}

func sameConflictIdentity(left, right conflictPathResolution) bool {
	if left.info != nil && right.info != nil {
		return samePhysicalFile(left.info, right.info)
	}
	return left.stable && right.stable && filepath.Clean(left.path) == filepath.Clean(right.path)
}

func (s *SettingsService) publishConfigLocked(next model.Config, conflicts conflictReconciliationSnapshot) {
	s.config = cloneConfig(next)
	for _, entry := range conflicts.entriesToClear {
		s.coordinator.clearConflictForIdentity(entry.path, entry.info)
	}
}

func resolveConflictPathIdentity(path string) conflictPathResolution {
	cleanedPath := filepath.Clean(path)
	absPath, err := filepath.Abs(cleanedPath)
	if err != nil {
		return conflictPathResolution{path: cleanedPath}
	}
	canonicalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return conflictPathResolution{path: cleanedPath}
	}
	canonicalPath = filepath.Clean(canonicalPath)
	info, err := os.Stat(canonicalPath)
	if err != nil {
		return conflictPathResolution{path: cleanedPath}
	}
	return conflictPathResolution{path: canonicalPath, info: info, stable: true}
}

func samePhysicalFile(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right)
}

type gitStatusChange struct {
	path   string
	before model.GitStatus
	exists bool
	after  *model.GitStatus
}

var errAmbiguousGitStatusIdentity = errors.New("ambiguous Git status identity")

func (s *SettingsService) prepareGitStatusChanges(ctx context.Context, paths ...string) ([]gitStatusChange, error) {
	if s.gitStatuses == nil {
		return nil, nil
	}
	changes := make([]gitStatusChange, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		status, exists, err := s.gitStatuses.Get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("read Git status: %w", err)
		}
		changes = append(changes, gitStatusChange{path: path, before: cloneGitStatus(status), exists: exists})
	}
	return changes, nil
}

func gitStatusChangeAt(changes []gitStatusChange, path string) *gitStatusChange {
	for index := range changes {
		if changes[index].path == path {
			return &changes[index]
		}
	}
	return nil
}

func (s *SettingsService) applyConfigWithGitStatusesLocked(
	ctx context.Context,
	next model.Config,
	targetPath string,
	changes []gitStatusChange,
) error {
	if s.gitStatuses == nil || len(changes) == 0 {
		return s.applyConfigLocked(next, targetPath)
	}
	conflicts := captureConflictReconciliationSnapshot(s.config, next, stableGitConflictIdentities(s.config, changes))
	for index := range changes {
		if err := s.writeGitStatusChange(ctx, changes[index]); err != nil {
			operationErr := fmt.Errorf("update Git status: %w", err)
			return s.restoreGitStatusesLocked(ctx, changes, operationErr)
		}
	}
	if err := s.applyConfigWithConflictSnapshotLocked(next, targetPath, conflicts); err != nil {
		return s.restoreGitStatusesLocked(ctx, changes, err)
	}
	return nil
}

func stableGitConflictIdentities(current model.Config, changes []gitStatusChange) map[string]string {
	identities := make(map[string]string)
	for _, change := range changes {
		status := change.before
		if !change.exists || status.Base == "" || status.RepositoryPath == "" {
			continue
		}
		index := baseIndex(current.Bases, status.Base)
		if index < 0 || !current.Bases[index].GitConfigured() {
			continue
		}
		if _, exists := identities[status.Base]; !exists {
			identities[status.Base] = filepath.Clean(status.RepositoryPath)
		}
	}
	return identities
}

func (s *SettingsService) writeGitStatusChange(ctx context.Context, change gitStatusChange) error {
	if change.after == nil {
		return s.gitStatuses.Delete(ctx, change.path)
	}
	return s.gitStatuses.Upsert(ctx, cloneGitStatus(*change.after))
}

func (s *SettingsService) restoreGitStatusesLocked(ctx context.Context, changes []gitStatusChange, operationErr error) error {
	var restoreErr error
	originalCtx := ctx
	if originalCtx == nil {
		originalCtx = context.Background()
	}
	for index := len(changes) - 1; index >= 0; index-- {
		change := changes[index]
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(originalCtx), 5*time.Second)
		var err error
		if change.exists {
			err = s.gitStatuses.Upsert(restoreCtx, cloneGitStatus(change.before))
		} else {
			err = s.gitStatuses.Delete(restoreCtx, change.path)
		}
		cancel()
		if err != nil {
			restoreErr = errors.Join(restoreErr, err)
		}
	}
	if restoreErr == nil {
		return operationErr
	}
	s.degraded = errors.Join(ErrRollbackFailed, operationErr, fmt.Errorf("restore Git status: %w", restoreErr))
	s.logger.Print("settings Git status rollback failed")
	return s.degraded
}

func cloneGitStatus(status model.GitStatus) model.GitStatus {
	if status.ChangedPaths != nil {
		status.ChangedPaths = append([]string{}, status.ChangedPaths...)
	}
	if status.Error != nil {
		cloned := *status.Error
		status.Error = &cloned
	}
	return status
}

func needsReconnectGitStatus(name, path string) model.GitStatus {
	return model.GitStatus{
		Base:           name,
		RepositoryPath: path,
		State:          model.GitStateNeedsReconnect,
		ChangedPaths:   []string{},
	}
}

func unconfiguredGitStatus(name, path string) model.GitStatus {
	return model.GitStatus{
		Base:           name,
		RepositoryPath: path,
		State:          model.GitStateUnconfigured,
		ChangedPaths:   []string{},
	}
}

func canonicalGitStatusPath(path string) (string, error) {
	canonicalPath, err := canonicalExistingDirectory(path)
	if err != nil {
		return "", fmt.Errorf("resolve Git status path: %w", err)
	}
	return canonicalPath, nil
}

func storedGitStatusPath(path string) string {
	return filepath.Clean(path)
}

func validateUniqueGitRepositoryPaths(bases []model.Base) error {
	type repositoryIdentity struct {
		base string
		path string
		info os.FileInfo
	}
	identities := make([]repositoryIdentity, 0, len(bases))
	for _, base := range bases {
		if !base.GitConfigured() {
			continue
		}
		path, err := canonicalGitStatusPath(base.Path)
		if err != nil {
			return fmt.Errorf("resolve Git repository path for base %q: %w", base.Name, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("inspect Git repository path for base %q: %w", base.Name, err)
		}
		for _, existing := range identities {
			if existing.path == path || samePhysicalFile(existing.info, info) {
				return fmt.Errorf("Git repository for bases %q and %q: %w", existing.base, base.Name, ErrGitRepositoryInUse)
			}
		}
		identities = append(identities, repositoryIdentity{base: base.Name, path: path, info: info})
	}
	return nil
}

func (s *SettingsService) existingGitStatusPath(ctx context.Context, base model.Base) (string, bool, error) {
	statuses, err := s.gitStatuses.List(ctx)
	if err != nil {
		return "", false, fmt.Errorf("list Git statuses: %w", err)
	}
	matchedPath := ""
	matchedCount := 0
	for _, status := range statuses {
		if status.Base != base.Name {
			continue
		}
		matchedCount++
		if matchedCount > 1 {
			return "", false, errAmbiguousGitStatusIdentity
		}
		matchedPath = status.RepositoryPath
	}
	if matchedCount == 1 {
		return matchedPath, true, nil
	}
	storedPath := storedGitStatusPath(base.Path)
	status, found, err := s.gitStatuses.Get(ctx, storedPath)
	if err != nil {
		return "", false, fmt.Errorf("read Git status: %w", err)
	}
	return storedPath, found && status.Base == base.Name, nil
}

func validLiteralGitBranch(branch string) bool {
	if branch == "" || strings.TrimSpace(branch) != branch || strings.HasPrefix(branch, "-") ||
		strings.HasPrefix(branch, "refs/") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") ||
		strings.Contains(branch, "//") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") ||
		strings.ContainsAny(branch, "~^:?*[\\ ") || containsControl(branch) {
		return false
	}
	switch branch {
	case "@", "HEAD", "FETCH_HEAD", "ORIG_HEAD", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "AUTO_MERGE", "BISECT_HEAD":
		return false
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || component == "." || component == ".." || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func (s *SettingsService) ConfigureGit(ctx context.Context, name string, request model.GitConfigRequest) (model.GitConfigResponse, error) {
	s.lockMutation()
	defer s.unlockMutation()

	if s.degraded != nil {
		return model.GitConfigResponse{}, s.degraded
	}
	index := baseIndex(s.config.Bases, name)
	if index < 0 {
		return model.GitConfigResponse{}, ErrBaseNotFound
	}
	if s.gitValidator == nil || s.gitStatuses == nil {
		return model.GitConfigResponse{}, fmt.Errorf("Git settings dependencies: %w", ErrInvalidConfig)
	}
	path, err := canonicalGitStatusPath(s.config.Bases[index].Path)
	if err != nil {
		return model.GitConfigResponse{}, err
	}
	normalized, err := s.gitValidator.Validate(ctx, path, request)
	if err != nil {
		return model.GitConfigResponse{}, err
	}
	if !validLiteralGitBranch(normalized.GitBranch) {
		return model.GitConfigResponse{}, fieldError(ErrInvalidGitBranch, "git_branch", "Git branch is required and must be a literal branch name")
	}
	next := cloneConfig(s.config)
	base := &next.Bases[index]
	base.GitURL = normalized.GitURL
	base.GitBranch = normalized.GitBranch
	base.AutoSync = normalized.AutoSync
	base.AutoSyncIntervalMinutes = normalized.AutoSyncIntervalMinutes
	base.GitCommitMessageTemplate = normalized.GitCommitMessageTemplate
	if err := validateUniqueGitRepositoryPaths(next.Bases); err != nil {
		return model.GitConfigResponse{}, err
	}
	status := needsReconnectGitStatus(base.Name, path)
	changes, err := s.prepareGitStatusChanges(ctx, path)
	if err != nil {
		return model.GitConfigResponse{}, err
	}
	changes[0].after = &status
	if err := s.applyConfigWithGitStatusesLocked(ctx, next, "", changes); err != nil {
		return model.GitConfigResponse{}, err
	}
	return model.GitConfigResponse{Base: next.Bases[index], Status: status}, nil
}

func (s *SettingsService) DisableGit(ctx context.Context, name string) (model.GitConfigResponse, error) {
	s.lockMutation()
	defer s.unlockMutation()

	if s.degraded != nil {
		return model.GitConfigResponse{}, s.degraded
	}
	index := baseIndex(s.config.Bases, name)
	if index < 0 {
		return model.GitConfigResponse{}, ErrBaseNotFound
	}
	if s.gitStatuses == nil {
		return model.GitConfigResponse{}, fmt.Errorf("Git status dependency: %w", ErrInvalidConfig)
	}
	path, owned, err := s.existingGitStatusPath(ctx, s.config.Bases[index])
	if err != nil {
		return model.GitConfigResponse{}, err
	}
	next := cloneConfig(s.config)
	base := &next.Bases[index]
	base.GitURL = ""
	base.GitBranch = ""
	base.AutoSync = false
	base.AutoSyncIntervalMinutes = 0
	base.GitCommitMessageTemplate = ""
	var changes []gitStatusChange
	if owned {
		changes, err = s.prepareGitStatusChanges(ctx, path)
		if err != nil {
			return model.GitConfigResponse{}, err
		}
	}
	if err := s.applyConfigWithGitStatusesLocked(ctx, next, "", changes); err != nil {
		return model.GitConfigResponse{}, err
	}
	return model.GitConfigResponse{Base: next.Bases[index], Status: unconfiguredGitStatus(base.Name, path)}, nil
}

func (s *SettingsService) CompleteSetup(request model.BaseMutationRequest) (model.SettingsResponse, error) {
	s.lockMutation()
	defer s.unlockMutation()

	if s.degraded != nil {
		return model.SettingsResponse{}, s.degraded
	}
	if s.config.SetupCompleted != nil && *s.config.SetupCompleted {
		return model.SettingsResponse{}, ErrSetupAlreadyCompleted
	}
	prepared, err := prepareBase(request)
	if err != nil {
		return model.SettingsResponse{}, err
	}

	if prepared.create {
		if err := createBaseDirectory(prepared); err != nil {
			return model.SettingsResponse{}, err
		}
	}

	completed := true
	next := model.Config{
		BaseDir:        filepath.Dir(prepared.base.Path),
		Bases:          []model.Base{prepared.base},
		CurrentBase:    prepared.base.Name,
		SetupCompleted: &completed,
	}
	if err := s.applyConfigLocked(next, prepared.base.Path); err != nil {
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}

func ensureUniqueName(config model.Config, name, except string) error {
	for _, base := range config.Bases {
		if base.Name == name && base.Name != except {
			return fieldError(ErrBaseNameConflict, "name", "base name already exists")
		}
	}
	return nil
}

func (s *SettingsService) AddBase(request model.BaseMutationRequest) (model.SettingsResponse, error) {
	s.lockMutation()
	defer s.unlockMutation()

	if s.degraded != nil {
		return model.SettingsResponse{}, s.degraded
	}
	prepared, err := prepareBase(request)
	if err != nil {
		return model.SettingsResponse{}, err
	}
	if err := ensureUniqueName(s.config, prepared.base.Name, ""); err != nil {
		return model.SettingsResponse{}, err
	}

	if prepared.create {
		if err := createBaseDirectory(prepared); err != nil {
			return model.SettingsResponse{}, err
		}
	}

	next := cloneConfig(s.config)
	next.Bases = append(next.Bases, prepared.base)
	if err := s.applyConfigLocked(next, ""); err != nil {
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}

func (s *SettingsService) UpdateBase(oldName string, request model.BaseUpdateRequest) (model.SettingsResponse, error) {
	s.lockMutation()
	defer s.unlockMutation()

	if s.degraded != nil {
		return model.SettingsResponse{}, s.degraded
	}
	index := baseIndex(s.config.Bases, oldName)
	if index < 0 {
		return model.SettingsResponse{}, ErrBaseNotFound
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return model.SettingsResponse{}, fieldError(ErrInvalidName, "name", "base name is required")
	}
	if err := ensureUniqueName(s.config, name, oldName); err != nil {
		return model.SettingsResponse{}, err
	}
	path, err := normalizeExistingBasePath(request.Path, "path")
	if err != nil {
		return model.SettingsResponse{}, err
	}

	next := cloneConfig(s.config)
	next.Bases[index].Name = name
	next.Bases[index].Path = path
	if next.Bases[index].GitConfigured() {
		if err := validateUniqueGitRepositoryPaths(next.Bases); err != nil {
			return model.SettingsResponse{}, err
		}
	}
	targetPath := ""
	if s.config.CurrentBase == oldName {
		next.CurrentBase = name
		targetPath = path
	}
	changes, err := s.prepareUpdateBaseGitStatuses(context.Background(), s.config.Bases[index], next.Bases[index])
	if err != nil {
		return model.SettingsResponse{}, err
	}
	if err := s.applyConfigWithGitStatusesLocked(context.Background(), next, targetPath, changes); err != nil {
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}

func (s *SettingsService) ForgetBase(name string) (model.SettingsResponse, error) {
	s.lockMutation()
	defer s.unlockMutation()

	if s.degraded != nil {
		return model.SettingsResponse{}, s.degraded
	}
	index := baseIndex(s.config.Bases, name)
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
	changes, err := s.prepareForgottenBaseGitStatus(context.Background(), s.config.Bases[index])
	if err != nil {
		return model.SettingsResponse{}, err
	}
	if err := s.applyConfigWithGitStatusesLocked(context.Background(), next, "", changes); err != nil {
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}

func (s *SettingsService) SwitchBase(name string) (model.SettingsResponse, error) {
	s.lockMutation()
	defer s.unlockMutation()

	if s.degraded != nil {
		return model.SettingsResponse{}, s.degraded
	}
	index := baseIndex(s.config.Bases, name)
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

func (s *SettingsService) ReplaceConfig(input model.Config) (model.SettingsResponse, error) {
	s.lockMutation()
	defer s.unlockMutation()

	if s.degraded != nil {
		return model.SettingsResponse{}, s.degraded
	}
	currentSetup := s.config.SetupCompleted != nil && *s.config.SetupCompleted
	next, err := normalizeConfig(input, currentSetup)
	if err != nil {
		return model.SettingsResponse{}, err
	}
	matches, err := protectReplaceConfigGitFields(s.config, next)
	if err != nil {
		return model.SettingsResponse{}, err
	}
	if err := validateUniqueGitRepositoryPaths(next.Bases); err != nil {
		return model.SettingsResponse{}, err
	}
	changes, err := s.prepareReplaceConfigGitStatuses(context.Background(), s.config, next, matches)
	if err != nil {
		return model.SettingsResponse{}, err
	}
	index := baseIndex(next.Bases, next.CurrentBase)
	if err := s.applyConfigWithGitStatusesLocked(context.Background(), next, next.Bases[index].Path, changes); err != nil {
		return model.SettingsResponse{}, err
	}
	return s.responseLocked(), nil
}

func (s *SettingsService) prepareUpdateBaseGitStatuses(ctx context.Context, current, next model.Base) ([]gitStatusChange, error) {
	if s.gitStatuses == nil || current.Name == next.Name && current.Path == next.Path {
		return nil, nil
	}
	oldPath, oldOwned, err := s.existingGitStatusPath(ctx, current)
	if err != nil {
		return nil, err
	}
	newPath, err := canonicalGitStatusPath(next.Path)
	if err != nil {
		return nil, err
	}
	if oldPath == newPath {
		if current.Name == next.Name || !oldOwned {
			return nil, nil
		}
		changes, err := s.prepareGitStatusChanges(ctx, oldPath)
		if err != nil {
			return nil, err
		}
		if !changes[0].exists {
			return nil, nil
		}
		status := cloneGitStatus(changes[0].before)
		status.Base = next.Name
		changes[0].after = &status
		return changes, nil
	}

	paths := make([]string, 0, 2)
	if oldOwned {
		paths = append(paths, oldPath)
	}
	if next.GitConfigured() {
		paths = append(paths, newPath)
	}
	if len(paths) == 0 {
		return nil, nil
	}
	changes, err := s.prepareGitStatusChanges(ctx, paths...)
	if err != nil {
		return nil, err
	}
	if next.GitConfigured() {
		status := needsReconnectGitStatus(next.Name, newPath)
		gitStatusChangeAt(changes, newPath).after = &status
	}
	return changes, nil
}

func (s *SettingsService) prepareForgottenBaseGitStatus(ctx context.Context, base model.Base) ([]gitStatusChange, error) {
	if s.gitStatuses == nil {
		return nil, nil
	}
	path, owned, err := s.existingGitStatusPath(ctx, base)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, nil
	}
	return s.prepareGitStatusChanges(ctx, path)
}

func protectReplaceConfigGitFields(current, next model.Config) ([]int, error) {
	matches, err := matchReplaceConfigBases(current.Bases, next.Bases)
	if err != nil {
		return nil, err
	}
	for index, match := range matches {
		var previous model.Base
		if match >= 0 {
			previous = current.Bases[match]
		}
		field := firstGitFieldDifference(previous, next.Bases[index])
		if field != "" {
			return nil, fieldError(
				ErrInvalidConfig,
				fmt.Sprintf("bases[%d].%s", index, field),
				"Git settings must be changed through /api/git/config",
			)
		}
	}
	return matches, nil
}

func matchReplaceConfigBases(current, next []model.Base) ([]int, error) {
	matches := make([]int, len(next))
	for index := range matches {
		matches[index] = -1
	}
	reserved := make([]bool, len(current))
	for index := range next {
		exact := baseIndex(current, next[index].Name)
		if exact >= 0 {
			matches[index] = exact
			reserved[exact] = true
		}
	}
	candidates := make([]int, len(next))
	for index := range candidates {
		candidates[index] = -1
	}
	claims := make([]int, len(current))
	for index := range next {
		if matches[index] >= 0 {
			continue
		}
		nextPath, err := canonicalGitStatusPath(next[index].Path)
		if err != nil {
			return nil, err
		}
		unique := -1
		for currentIndex := range current {
			if reserved[currentIndex] {
				continue
			}
			currentPath, pathErr := canonicalGitStatusPath(current[currentIndex].Path)
			if pathErr != nil {
				return nil, pathErr
			}
			if currentPath != nextPath {
				continue
			}
			if unique >= 0 {
				unique = -1
				break
			}
			unique = currentIndex
		}
		candidates[index] = unique
		if unique >= 0 {
			claims[unique]++
		}
	}
	for index, candidate := range candidates {
		if candidate >= 0 && claims[candidate] == 1 {
			matches[index] = candidate
		}
	}
	return matches, nil
}

func firstGitFieldDifference(current, next model.Base) string {
	switch {
	case current.GitURL != next.GitURL:
		return "git_url"
	case current.GitBranch != next.GitBranch:
		return "git_branch"
	case current.AutoSync != next.AutoSync:
		return "auto_sync"
	case current.AutoSyncIntervalMinutes != next.AutoSyncIntervalMinutes:
		return "auto_sync_interval_minutes"
	case current.GitCommitMessageTemplate != next.GitCommitMessageTemplate:
		return "git_commit_message_template"
	default:
		return ""
	}
}

func (s *SettingsService) prepareReplaceConfigGitStatuses(
	ctx context.Context,
	current model.Config,
	next model.Config,
	matches []int,
) ([]gitStatusChange, error) {
	if s.gitStatuses == nil {
		return nil, nil
	}
	matchedCurrent := make(map[int]struct{}, len(matches))
	paths := make([]string, 0)
	oldPaths := make([]string, len(matches))
	oldOwned := make([]bool, len(matches))
	newPaths := make([]string, len(matches))
	for index, match := range matches {
		if match < 0 {
			continue
		}
		matchedCurrent[match] = struct{}{}
		if current.Bases[match].Name == next.Bases[index].Name && storedGitStatusPath(current.Bases[match].Path) == storedGitStatusPath(next.Bases[index].Path) {
			continue
		}
		oldPath, owned, err := s.existingGitStatusPath(ctx, current.Bases[match])
		if err != nil {
			return nil, err
		}
		newPath, err := canonicalGitStatusPath(next.Bases[index].Path)
		if err != nil {
			return nil, err
		}
		oldPaths[index], oldOwned[index], newPaths[index] = oldPath, owned, newPath
		if oldPath == newPath {
			if owned && current.Bases[match].Name != next.Bases[index].Name {
				paths = append(paths, oldPath)
			}
			continue
		}
		if owned {
			paths = append(paths, oldPath)
		}
		if next.Bases[index].GitConfigured() {
			paths = append(paths, newPath)
		}
	}
	removedPaths := make([]string, 0)
	for index := range current.Bases {
		if _, ok := matchedCurrent[index]; ok {
			continue
		}
		path, owned, err := s.existingGitStatusPath(ctx, current.Bases[index])
		if err != nil {
			return nil, err
		}
		if !owned {
			continue
		}
		removedPaths = append(removedPaths, path)
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil, nil
	}
	changes, err := s.prepareGitStatusChanges(ctx, paths...)
	if err != nil {
		return nil, err
	}
	for _, path := range removedPaths {
		gitStatusChangeAt(changes, path).after = nil
	}
	for index, match := range matches {
		if match < 0 || oldPaths[index] == "" {
			continue
		}
		if oldPaths[index] == newPaths[index] {
			if !oldOwned[index] {
				continue
			}
			change := gitStatusChangeAt(changes, oldPaths[index])
			if current.Bases[match].Name != next.Bases[index].Name && change.exists {
				status := cloneGitStatus(change.before)
				status.Base = next.Bases[index].Name
				change.after = &status
			}
			continue
		}
		if next.Bases[index].GitConfigured() {
			status := needsReconnectGitStatus(next.Bases[index].Name, newPaths[index])
			gitStatusChangeAt(changes, newPaths[index]).after = &status
		}
	}
	return changes, nil
}

func normalizeConfig(input model.Config, currentSetup bool) (model.Config, error) {
	normalized := cloneConfig(input)
	setupCompleted := currentSetup
	if normalized.SetupCompleted != nil {
		if currentSetup && !*normalized.SetupCompleted {
			return model.Config{}, fieldError(ErrSetupCannotReopen, "setup_completed", "completed setup cannot be reopened")
		}
		setupCompleted = *normalized.SetupCompleted
	}
	normalized.SetupCompleted = &setupCompleted

	if len(normalized.Bases) == 0 {
		return model.Config{}, fieldError(ErrInvalidConfig, "bases", "at least one base is required")
	}
	names := make(map[string]struct{}, len(normalized.Bases))
	for index := range normalized.Bases {
		nameField := fmt.Sprintf("bases[%d].name", index)
		name := strings.TrimSpace(normalized.Bases[index].Name)
		if name == "" {
			return model.Config{}, fieldError(ErrInvalidName, nameField, "base name is required")
		}
		if _, exists := names[name]; exists {
			return model.Config{}, fieldError(ErrBaseNameConflict, nameField, "base name already exists")
		}
		names[name] = struct{}{}
		normalized.Bases[index].Name = name

		pathField := fmt.Sprintf("bases[%d].path", index)
		path, err := normalizeExistingBasePath(normalized.Bases[index].Path, pathField)
		if err != nil {
			return model.Config{}, err
		}
		normalized.Bases[index].Path = path
	}

	normalized.CurrentBase = strings.TrimSpace(normalized.CurrentBase)
	if baseIndex(normalized.Bases, normalized.CurrentBase) < 0 {
		return model.Config{}, fieldError(ErrBaseNotFound, "current_base", "current base is not configured")
	}

	normalized.BaseDir = strings.TrimSpace(normalized.BaseDir)
	if normalized.BaseDir != "" {
		baseDir, err := filepath.Abs(normalized.BaseDir)
		if err != nil {
			return model.Config{}, fieldErrorWithCause(ErrInvalidPath, err, "base_dir", "resolve base directory")
		}
		normalized.BaseDir = filepath.Clean(baseDir)
	}
	return normalized, nil
}

func normalizeExistingBasePath(path, field string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fieldError(ErrInvalidPath, field, "base path is required")
	}
	canonicalPath, err := canonicalExistingDirectory(path)
	if err != nil {
		return "", fieldErrorWithCause(ErrInvalidPath, err, field, "resolve base path")
	}
	return canonicalPath, nil
}

func canonicalExistingDirectory(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	canonicalPath, err := filepath.EvalSymlinks(filepath.Clean(absPath))
	if err != nil {
		return "", fmt.Errorf("resolve path symlinks: %w", err)
	}
	canonicalPath = filepath.Clean(canonicalPath)
	info, err := os.Stat(canonicalPath)
	if err != nil {
		return "", fmt.Errorf("inspect path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path %q is not a directory", canonicalPath)
	}
	return canonicalPath, nil
}

func fieldError(kind error, field, message string) error {
	return &FieldError{Kind: kind, Field: field, Message: message}
}

func fieldErrorWithCause(kind, cause error, field, message string) error {
	return &FieldError{Kind: errors.Join(kind, cause), Field: field, Message: message}
}

func cloneConfig(config model.Config) model.Config {
	cloned := config
	cloned.Bases = append([]model.Base(nil), config.Bases...)
	if config.SetupCompleted != nil {
		completed := *config.SetupCompleted
		cloned.SetupCompleted = &completed
	}
	return cloned
}

func baseIndex(bases []model.Base, name string) int {
	for index := range bases {
		if bases[index].Name == name {
			return index
		}
	}
	return -1
}

func configuredBasePath(config model.Config, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	index := baseIndex(config.Bases, name)
	if index < 0 {
		return "", ErrBaseNotFound
	}
	return config.Bases[index].Path, nil
}
