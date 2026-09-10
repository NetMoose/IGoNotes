package main

import (
	"os"
	"strings"
	"testing"
)

func TestGitRecoveryCompletesBeforeInitialIndexAndServe(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	runServerSource := sourceFunction(t, string(source), "func runServer(", "func runMain(")

	ordered := []string{
		"gitOperations := repository.NewGitOperationRepository(db)",
		"gitService := gitcmd.NewService(gitRunner, gitClient)",
		"gitManager := service.NewGitManager(gitService, gitStatusRepo, gitOperations, gitProbeService, settingsService.GitSnapshot, noteService, coordinator)",
		"gitManager.RecoverLocal(ctx, configuredGitSnapshots(settingsService))",
		"gitManager.Start()",
		"go func() {",
		"noteService.SyncFS()",
		"router := handlers.NewRouter(noteHandler, settingsHandler, settingsService, spaHandler)",
		"return serveLocal(ctx, address, newHTTPServer(router)",
	}
	remaining := runServerSource
	for _, snippet := range ordered {
		index := strings.Index(remaining, snippet)
		if index < 0 {
			t.Fatalf("runServer does not contain %q in startup order", snippet)
		}
		remaining = remaining[index+len(snippet):]
	}
}

func TestGitManagerClosesDuringShutdown(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	runServerSource := sourceFunction(t, string(source), "func runServer(", "func runMain(")
	manager := strings.Index(runServerSource, "gitManager := service.NewGitManager(")
	deferClose := strings.Index(runServerSource, "gitManager.Close()")
	databaseClose := strings.Index(runServerSource, "db.Close()")
	if manager < 0 || deferClose < manager || databaseClose < 0 || deferClose < databaseClose {
		t.Fatalf("Git manager shutdown ordering is missing or closes after DB: manager=%d close=%d db=%d", manager, deferClose, databaseClose)
	}
}

func TestUnconfiguredBasesDoNotInvokeGitDuringRecovery(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(source), "if !base.GitConfigured() {") {
		t.Fatal("configuredGitSnapshots must skip unconfigured bases")
	}
}

func TestRecoveryUsesNoNetworkCommand(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	runServerSource := sourceFunction(t, string(source), "func runServer(", "func runMain(")
	recovery := strings.Index(runServerSource, "gitManager.RecoverLocal(")
	if recovery < 0 {
		t.Fatal("runServer does not recover Git repositories")
	}
	if strings.Contains(runServerSource[:recovery], ".Sync(") || strings.Contains(runServerSource[:recovery], ".Initialize(") {
		t.Fatal("startup runs network Git work before local recovery")
	}
}

func TestConflictRecoveryReusesLocalRecoveryBeforeWorkerAndInitialIndex(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	runServerSource := sourceFunction(t, string(source), "func runServer(", "func runMain(")

	if got := strings.Count(runServerSource, "gitManager.RecoverLocal("); got != 1 {
		t.Fatalf("RecoverLocal calls = %d, want exactly 1", got)
	}
	for _, forbidden := range []string{".Sync(", ".Initialize(", ".Probe("} {
		if strings.Contains(runServerSource[:strings.Index(runServerSource, "gitManager.RecoverLocal(")], forbidden) {
			t.Fatalf("startup runs network Git work before local recovery: %s", forbidden)
		}
	}
	for _, required := range []string{
		"gitManager.RecoverLocal(ctx, configuredGitSnapshots(settingsService))",
		"gitManager.Start()",
		"noteService.SyncFS()",
		"gitConflictHandler := handlers.NewGitConflictHandler(gitManager)",
		"handlers.RegisterGitConflictRoutes(router, gitConflictHandler, settingsService)",
	} {
		if strings.Index(runServerSource, required) < 0 {
			t.Fatalf("runServer does not contain %q", required)
		}
	}
}
