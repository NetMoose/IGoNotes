package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/handlers"
	"IGoNotes/internal/model"
	"IGoNotes/internal/service"
)

type countingGitRunner struct {
	calls int
}

func (r *countingGitRunner) Run(context.Context, gitcmd.Command) (gitcmd.Result, error) {
	r.calls++
	return gitcmd.Result{}, errors.New("Git execution must not occur during construction")
}

type gitWiringConfigStore struct {
	config model.Config
}

func (s *gitWiringConfigStore) Load() (*model.Config, error) {
	config := s.config
	return &config, nil
}

func (s *gitWiringConfigStore) Save(config *model.Config) error {
	s.config = *config
	return nil
}

type gitWiringNoteRepository struct{}

func (gitWiringNoteRepository) UpsertNode(string, string, string, *string, string) error {
	return nil
}

func (gitWiringNoteRepository) GetAllNodes() ([]model.NoteNode, error) {
	return nil, nil
}

func (gitWiringNoteRepository) BeginReplaceAll([]model.NoteNode) (func() error, func() error, error, error) {
	return func() error { return nil }, func() error { return nil }, nil, nil
}

func (gitWiringNoteRepository) DeleteNode(string) error { return nil }

type countingGitStatusStore struct {
	calls int
}

func (s *countingGitStatusStore) Upsert(context.Context, model.GitStatus) error {
	s.calls++
	return errors.New("Git status mutation must not occur during construction")
}

func (s *countingGitStatusStore) Get(context.Context, string) (model.GitStatus, bool, error) {
	s.calls++
	return model.GitStatus{}, false, errors.New("Git status read must not occur during construction")
}

func (s *countingGitStatusStore) List(context.Context) ([]model.GitStatus, error) {
	s.calls++
	return nil, errors.New("Git status read must not occur during construction")
}

func (s *countingGitStatusStore) Delete(context.Context, string) error {
	s.calls++
	return errors.New("Git status mutation must not occur during construction")
}

func TestGitFoundationConstructionDoesNotExecuteGitOrAccessStatuses(t *testing.T) {
	setupCompleted := false
	store := &gitWiringConfigStore{config: model.Config{SetupCompleted: &setupCompleted}}
	coordinator := service.NewBaseOperationCoordinator()
	notes := service.NewNoteService(gitWiringNoteRepository{}, "", coordinator)
	runner := &countingGitRunner{}
	client := gitcmd.NewClient(runner)
	validator := service.NewGitConfigValidator(client)
	statuses := &countingGitStatusStore{}

	settings, err := service.NewSettingsServiceWithGit(store, notes, coordinator, "", nil, validator, statuses)
	if err != nil {
		t.Fatalf("NewSettingsServiceWithGit() error = %v", err)
	}
	probe := service.NewGitProbeService(settings, client)
	status := service.NewGitStatusService(settings, statuses)
	handler := handlers.NewGitHandler(probe, settings, status)
	if handler == nil {
		t.Fatal("NewGitHandler() returned nil")
	}
	if runner.calls != 0 {
		t.Errorf("Git runner calls during construction = %d, want 0", runner.calls)
	}
	if statuses.calls != 0 {
		t.Errorf("Git status store calls during construction = %d, want 0", statuses.calls)
	}
}

func TestRunServerWiresGitFoundationBeforeSystemRoutesAndServing(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}

	orderedSnippets := []string{
		"coordinator := service.NewBaseOperationCoordinator()",
		"noteService := service.NewNoteService(noteRepo, basePath, coordinator)",
		"gitRunner := gitcmd.NewCommandRunner()",
		"gitClient := gitcmd.NewClient(gitRunner)",
		"gitStatusRepo := repository.NewGitStatusRepository(db)",
		"gitValidator := service.NewGitConfigValidator(gitClient)",
		"settingsService, err := service.NewSettingsServiceWithGit(configService, noteService, coordinator, options.base, log.Default(), gitValidator, gitStatusRepo)",
		"gitProbeService := service.NewGitProbeService(settingsService, gitClient)",
		"gitStatusService := service.NewGitStatusService(settingsService, gitStatusRepo)",
		"gitHandler := handlers.NewGitHandler(gitProbeService, settingsService, gitStatusService)",
		"router := handlers.NewRouter(noteHandler, settingsHandler, settingsService, spaHandler)",
		"handlers.RegisterGitRoutes(router, gitHandler, settingsService)",
		"registerSystemRoutes(router, systemHandler)",
		"return serveLocal(ctx, address, newHTTPServer(router)",
	}
	remaining := string(source)
	for _, snippet := range orderedSnippets {
		index := strings.Index(remaining, snippet)
		if index < 0 {
			t.Fatalf("main.go does not contain %q in production wiring order", snippet)
		}
		remaining = remaining[index+len(snippet):]
	}
}

func TestRunServerGitWiringHasNoStartupExecution(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	runServerSource := sourceFunction(t, string(source), "func runServer(", "func runMain(")

	for _, forbidden := range []string{
		"exec.LookPath",
		".git",
		".Run(",
		".Probe(",
		".Version(",
		".InspectLocal(",
		".InspectRemote(",
	} {
		if strings.Contains(runServerSource, forbidden) {
			t.Errorf("runServer contains forbidden startup operation %q", forbidden)
		}
	}
	for _, required := range []string{
		"service.NewSettingsServiceWithGit(",
		"repository.NewGitStatusRepository(db)",
	} {
		if !strings.Contains(runServerSource, required) {
			t.Errorf("runServer does not use production Git wiring %q", required)
		}
	}
	if strings.Contains(runServerSource, "service.NewSettingsService(") {
		t.Error("runServer uses plain NewSettingsService instead of NewSettingsServiceWithGit")
	}
	if got := strings.Count(runServerSource, "service.NewBaseOperationCoordinator()"); got != 1 {
		t.Errorf("runServer coordinator constructions = %d, want exactly 1", got)
	}
}

func sourceFunction(t *testing.T, source, start, end string) string {
	t.Helper()
	startIndex := strings.Index(source, start)
	if startIndex < 0 {
		t.Fatalf("source does not contain %q", start)
	}
	endIndex := strings.Index(source[startIndex:], end)
	if endIndex < 0 {
		t.Fatalf("source after %q does not contain %q", start, end)
	}
	return source[startIndex : startIndex+endIndex]
}
