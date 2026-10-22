package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
	"IGoNotes/internal/service"
)

func TestPrivateStorageSettingsHTTP(t *testing.T) {
	for _, operation := range []string{"setup-create", "setup-connect", "add-create", "add-connect", "update", "replace", "switch"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			configDir := filepath.Join(root, "config")
			if err := os.Mkdir(configDir, 0700); err != nil {
				t.Fatal(err)
			}
			db, err := repository.InitDB(filepath.Join(root, "metadata.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			coordinator := service.NewBaseOperationCoordinator()
			notes := service.NewNoteService(repository.NewNoteRepository(db), "", coordinator)
			defer notes.Close()
			store := service.NewConfigService(filepath.Join(configDir, "config.json"))
			settings, err := service.NewSettingsService(store, notes, coordinator, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			h := NewSettingsHandler(settings)
			if !strings.HasPrefix(operation, "setup") {
				if _, err := settings.CompleteSetup(model.BaseMutationRequest{Mode: "connect", Name: "safe", Path: t.TempDir()}); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "switch" {
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(notes.GetBasePath(), alias); err != nil {
					t.Skip(err)
				}
				config := settings.GetConfig()
				config.Bases = append(config.Bases, model.Base{Name: "alias", Path: alias})
				if err := store.Save(&config); err != nil {
					t.Fatal(err)
				}
				settings, err = service.NewSettingsService(store, notes, coordinator, "", nil)
				if err != nil {
					t.Fatal(err)
				}
				h = NewSettingsHandler(settings)
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(configDir, alias); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(filepath.Join(configDir, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			var payload any
			var handler http.HandlerFunc
			url := "/api/setup"
			switch operation {
			case "setup-create":
				payload = model.BaseMutationRequest{Mode: "create", Name: "new", Path: configDir}
				handler = h.CompleteSetup
			case "setup-connect":
				payload = model.BaseMutationRequest{Mode: "connect", Name: "bad", Path: root}
				handler = h.CompleteSetup
			case "add-create":
				payload = model.BaseMutationRequest{Mode: "create", Name: "new", Path: configDir}
				handler = h.AddBase
			case "add-connect":
				payload = model.BaseMutationRequest{Mode: "connect", Name: "bad", Path: root}
				handler = h.AddBase
			case "update":
				payload = model.BaseUpdateRequest{Name: "safe", Path: configDir}
				handler = h.UpdateBase
				url = "/api/bases?name=safe"
			case "replace":
				next := settings.GetConfig()
				next.Bases = append(next.Bases, model.Base{Name: "bad", Path: root})
				payload = next
				handler = h.SaveConfig
			case "switch":
				payload = model.BaseSwitchRequest{Name: "alias"}
				handler = h.SwitchBase
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			handler(recorder, httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body)))
			if recorder.Code != 422 || !strings.Contains(recorder.Body.String(), `"code":"invalid_base_path"`) {
				t.Fatalf("response: %d %s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), root) {
				t.Fatalf("private path leaked: %s", recorder.Body.String())
			}
			after, err := os.ReadFile(filepath.Join(configDir, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("config changed on rejection")
			}
			if _, err := os.Stat(filepath.Join(configDir, "new")); !os.IsNotExist(err) {
				t.Fatal("created private directory")
			}
		})
	}
}
