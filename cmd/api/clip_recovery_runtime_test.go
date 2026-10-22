package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
	"IGoNotes/internal/service"
)

// Fail only the receipt, after the real publisher has durably created the note.
// Reopening the database below exercises actual journals and native identities,
// without duplicating their private serialization in this package.
type startupReceiptFailure struct{ service.ClipImportStore }

func (s startupReceiptFailure) Transition(ctx context.Context, id string, v repository.ClipImportVersion, next repository.ClipImportState, result *model.ClipperImportResult, failure *model.APIError) error {
	if next == repository.ClipImportSucceeded {
		return errors.New("private receipt failure")
	}
	return s.ClipImportStore.Transition(ctx, id, v, next, result, failure)
}

type startupClaimFailure struct{ service.ClipImportStore }

func (s startupClaimFailure) Claim(ctx context.Context, row repository.ClipImportRecord) (repository.ClipImportRecord, bool, error) {
	row, created, err := s.ClipImportStore.Claim(ctx, row)
	if err == nil {
		err = errors.New("lost private claim acknowledgement")
	}
	return row, created, err
}

type clipStartupLogWriter func([]byte) (int, error)

func (write clipStartupLogWriter) Write(data []byte) (int, error) { return write(data) }

func TestRunServerClipRecoveryBeforeHTTP(t *testing.T) {
	for _, scenario := range []string{"prepared", "roll-forward", "manual", "database-failure", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			manual := scenario == "manual"
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			configDir, base := t.TempDir(), t.TempDir()
			dbPath := filepath.Join(home, ".igonotes", "metadata.db")
			policy, err := service.NewPrivateStoragePolicy(configDir, dbPath, filepath.Join(home, ".igonotes", "clipper-staging"))
			if err != nil {
				t.Fatal(err)
			}
			config := service.NewConfigService(filepath.Join(configDir, "config.json"), policy)
			done := true
			if err := config.Save(&model.Config{Bases: []model.Base{{Name: "work", Path: base}}, CurrentBase: "work", SetupCompleted: &done}); err != nil {
				t.Fatal(err)
			}
			db, err := repository.InitDB(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			coordinator := service.NewBaseOperationCoordinator()
			notes := service.NewNoteService(repository.NewNoteRepository(db), base, coordinator)
			t.Cleanup(func() { _ = notes.Close() })
			settings, err := service.NewSettingsService(config, notes, coordinator, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			dest := service.NewClipperDestinationService(settings)
			revision, err := dest.Issue("work")
			if err != nil {
				t.Fatal(err)
			}
			store := repository.NewClipImportRepository(db)
			var seedStore service.ClipImportStore = startupReceiptFailure{store}
			if scenario == "prepared" {
				seedStore = startupClaimFailure{store}
			}
			imports, err := service.NewClipImportService(settings, dest, notes, seedStore)
			if err != nil {
				t.Fatal(err)
			}
			manifest := model.ClipperManifest{ImportID: "12345678-1234-4567-8901-123456789abc", Base: "work", DestinationRevision: revision, Filename: "recovered.md", Markdown: "# Recovered\n", SourceURL: "https://example.invalid/page", CapturedAt: "2026-10-22T00:00:00Z", Assets: []model.ClipperAsset{}, ExternalImages: []model.ClipperExternalImage{}}
			status, _, err := imports.Import(t.Context(), manifest)
			if scenario == "prepared" {
				var clipErr *model.ClipperError
				if !errors.As(err, &clipErr) || clipErr.Code != model.ClipperRecoveryRequired {
					t.Fatalf("claim seed: %+v %v", status, err)
				}
				if _, err := os.Stat(filepath.Join(base, manifest.Filename)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("prepared seed already published", err)
				}
			} else if err != nil || status.State != model.ClipperImportRecoveryRequired {
				t.Fatalf("seed: %+v %v", status, err)
			}
			if manual {
				if err := os.WriteFile(filepath.Join(base, manifest.Filename), []byte("user edit"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			connection, err := service.NewClipperConnectionService(repository.NewClipperConnectionRepository(db)).Create(t.Context(), "Startup")
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "database-failure" {
				if _, err := db.Exec(`CREATE TRIGGER fail_clip_recovery BEFORE UPDATE ON clip_imports BEGIN SELECT RAISE(FAIL, 'private database diagnostic'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if err := notes.Close(); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			previousLogOutput := log.Writer()
			log.SetOutput(clipStartupLogWriter(func(data []byte) (int, error) {
				text := string(data)
				for _, forbidden := range []string{connection.Secret, "private receipt failure", "private database diagnostic", "Открываем браузер"} {
					if strings.Contains(text, forbidden) {
						t.Error("startup logged private diagnostics/secret or attempted browser launch")
					}
				}
				return len(data), nil
			}))
			t.Cleanup(func() { log.SetOutput(previousLogOutput) })

			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			if scenario == "canceled" {
				cancel()
			}
			if scenario == "database-failure" || scenario == "canceled" {
				defer cancel()
				err := runServer(ctx, []string{"--config", configDir, "--no-browser", "--port", strconv.Itoa(port)})
				var safe *safeGitLifecycleError
				message := "восстановить импорт Clipper"
				// Already-canceled startup stops at the preceding Git recovery.
				if scenario == "canceled" {
					message = "восстановить локальные репозитории Git"
				}
				if !errors.As(err, &safe) || err.Error() != message || strings.Contains(err.Error(), "private") {
					t.Fatalf("unsafe/missing fatal recovery error: %v", err)
				}
				probe, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				if err != nil {
					t.Fatal("fatal recovery admitted HTTP", err)
				}
				probe.Close()
				return
			}
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- runServer(ctx, []string{"--config", configDir, "--no-browser", "--port", strconv.Itoa(port)})
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-serverDone:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(15 * time.Second):
					t.Error("server shutdown timed out")
				}
			})
			client := &http.Client{Timeout: time.Second}
			endpoint := "http://127.0.0.1:" + strconv.Itoa(port)
			request := func(path string) ([]byte, int, error) {
				r, _ := http.NewRequestWithContext(t.Context(), "GET", endpoint+path, nil)
				r.Header.Set("Authorization", "Bearer "+connection.Secret)
				r.Header.Set("Origin", "chrome-extension://"+strings.Repeat("a", 32))
				response, err := client.Do(r)
				if err != nil {
					return nil, 0, err
				}
				defer response.Body.Close()
				data, err := io.ReadAll(response.Body)
				return data, response.StatusCode, err
			}
			var data []byte
			var code int
			deadline := time.Now().Add(10 * time.Second)
			for {
				data, code, err = request("/api/clipper/v1/imports?id=" + manifest.ImportID)
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("HTTP did not start", err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			var recovered model.ClipperImportStatusResponse
			if err := json.Unmarshal(data, &recovered); err != nil || code != 200 {
				t.Fatalf("status: %d %s %v", code, data, err)
			}
			want := model.ClipperImportSucceeded
			if manual {
				want = model.ClipperImportRecoveryRequired
			}
			if recovered.State != want {
				t.Fatalf("first HTTP observed %s, want %s after recovery", recovered.State, want)
			}
			if strings.Contains(string(data), home) || strings.Contains(string(data), connection.Secret) {
				t.Fatal("private status disclosure")
			}
			response, err := client.Get(endpoint + "/api/notes")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			tree, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != 200 || !strings.Contains(string(tree), manifest.Filename) {
				t.Fatalf("tree after recovery: %s %v", tree, err)
			}
			content, err := os.ReadFile(filepath.Join(base, manifest.Filename))
			wantContent := manifest.Markdown
			if manual {
				wantContent = "user edit"
			}
			if err != nil || string(content) != wantContent {
				t.Fatal("startup changed public bytes", err)
			}
		})
	}
}
