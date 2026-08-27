package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
	"IGoNotes/internal/service"
)

type settingsHandlerFixture struct {
	handler *SettingsHandler
	config  *service.ConfigService
	notes   *service.NoteService
}

func newSettingsHandlerFixture(t *testing.T) settingsHandlerFixture {
	t.Helper()

	root := t.TempDir()
	db, err := repository.InitDB(filepath.Join(root, "metadata.db"))
	if err != nil {
		t.Fatalf("repository.InitDB() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close() error = %v", err)
		}
	})

	notes := service.NewNoteService(repository.NewNoteRepository(db), "")
	if err := notes.SyncFS(); err != nil {
		t.Fatalf("NoteService.SyncFS() error = %v", err)
	}
	config := service.NewConfigService(filepath.Join(root, "config", "config.json"))
	settings, err := service.NewSettingsService(config, notes, "", nil)
	if err != nil {
		t.Fatalf("service.NewSettingsService() error = %v", err)
	}

	return settingsHandlerFixture{
		handler: NewSettingsHandler(settings),
		config:  config,
		notes:   notes,
	}
}

func TestSettingsHandlerGetConfigReturnsDirectConfig(t *testing.T) {
	fixture := newSettingsHandlerFixture(t)
	recorder := httptest.NewRecorder()

	fixture.handler.GetConfig(recorder, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	var raw map[string]json.RawMessage
	decodeHandlerJSON(t, recorder, http.StatusOK, &raw)
	if _, wrapped := raw["config"]; wrapped {
		t.Fatal("GetConfig() returned a SettingsResponse wrapper")
	}
	var setupCompleted bool
	if err := json.Unmarshal(raw["setup_completed"], &setupCompleted); err != nil {
		t.Fatalf("setup_completed is not a JSON boolean: %v", err)
	}
	if setupCompleted {
		t.Fatal("setup_completed = true, want false")
	}
	if got := string(raw["bases"]); got != "null" {
		t.Errorf("bases = %s, want null", got)
	}
}

func TestSettingsHandlerRejectsInvalidJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: "{"},
		{name: "empty", body: ""},
		{name: "multiple values", body: `{"mode":"connect","name":"base","path":"/tmp"} {}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSettingsHandlerFixture(t)
			body := &closeTrackingBody{Reader: strings.NewReader(test.body)}
			request := httptest.NewRequest(http.MethodPost, "/api/setup", nil)
			request.Body = body
			recorder := httptest.NewRecorder()

			fixture.handler.CompleteSetup(recorder, request)

			assertAPIErrorResponse(t, recorder, http.StatusBadRequest, model.APIError{Code: "bad_json", Message: "Invalid JSON"})
			if !body.closed {
				t.Fatal("request body was not closed")
			}
		})
	}
}

func TestSettingsHandlerRequiresSetupAndAddFieldsInOrder(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		field string
	}{
		{name: "mode", body: `{}`, field: "mode"},
		{name: "name", body: `{"mode":"connect"}`, field: "name"},
		{name: "path", body: `{"mode":"connect","name":"base"}`, field: "path"},
	}

	for _, endpoint := range []struct {
		name string
		call func(*SettingsHandler, http.ResponseWriter, *http.Request)
	}{
		{name: "complete setup", call: func(handler *SettingsHandler, w http.ResponseWriter, r *http.Request) { handler.CompleteSetup(w, r) }},
		{name: "add base", call: func(handler *SettingsHandler, w http.ResponseWriter, r *http.Request) { handler.AddBase(w, r) }},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					fixture := newSettingsHandlerFixture(t)
					recorder := httptest.NewRecorder()

					endpoint.call(fixture.handler, recorder, httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(test.body)))

					assertMissingField(t, recorder, test.field)
				})
			}
		})
	}
}

func TestSettingsHandlerTreatsWhitespaceFieldsAsPresent(t *testing.T) {
	fixture := newSettingsHandlerFixture(t)
	path := t.TempDir()
	tests := []struct {
		name   string
		body   string
		status int
		want   model.APIError
	}{
		{
			name:   "mode",
			body:   `{"mode":" ","name":"base","path":"` + path + `"}`,
			status: http.StatusBadRequest,
			want:   model.APIError{Code: "invalid_mode", Message: "mode must be create or connect", Field: "mode"},
		},
		{
			name:   "name",
			body:   `{"mode":"connect","name":" ","path":"` + path + `"}`,
			status: http.StatusUnprocessableEntity,
			want:   model.APIError{Code: "invalid_base_name", Message: "base name is required", Field: "name"},
		},
		{
			name:   "path",
			body:   `{"mode":"connect","name":"base","path":" "}`,
			status: http.StatusUnprocessableEntity,
			want:   model.APIError{Code: "invalid_base_path", Message: "base path is required", Field: "path"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()

			fixture.handler.CompleteSetup(recorder, httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(test.body)))

			assertAPIErrorResponse(t, recorder, test.status, test.want)
		})
	}
}

func TestSettingsHandlerCompleteSetup(t *testing.T) {
	fixture := newSettingsHandlerFixture(t)
	basePath := t.TempDir()
	body := mutationJSON(t, model.BaseMutationRequest{Mode: "connect", Name: "primary", Path: basePath}) + "\n\t "
	recorder := httptest.NewRecorder()

	fixture.handler.CompleteSetup(recorder, httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(body)))

	response := decodeSettingsResponse(t, recorder, http.StatusOK)
	assertSettingsState(t, fixture, response, "primary", basePath, true, 1)

	repeat := httptest.NewRecorder()
	fixture.handler.CompleteSetup(repeat, httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(body)))
	assertAPIErrorResponse(t, repeat, http.StatusConflict, model.APIError{
		Code:    "setup_already_completed",
		Message: "setup already completed",
	})
}

func TestSettingsHandlerBaseLifecycle(t *testing.T) {
	fixture := newSettingsHandlerFixture(t)
	primaryPath := t.TempDir()
	secondaryPath := t.TempDir()
	updatedPrimaryPath := t.TempDir()

	setup := httptest.NewRecorder()
	fixture.handler.CompleteSetup(setup, httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(mutationJSON(t, model.BaseMutationRequest{
		Mode: "connect", Name: "primary", Path: primaryPath,
	}))))
	decodeSettingsResponse(t, setup, http.StatusOK)

	add := httptest.NewRecorder()
	fixture.handler.AddBase(add, httptest.NewRequest(http.MethodPost, "/api/bases", strings.NewReader(mutationJSON(t, model.BaseMutationRequest{
		Mode: "connect", Name: "secondary", Path: secondaryPath,
	}))))
	addResponse := decodeSettingsResponse(t, add, http.StatusOK)
	assertSettingsState(t, fixture, addResponse, "primary", primaryPath, true, 2)

	update := httptest.NewRecorder()
	fixture.handler.UpdateBase(update, httptest.NewRequest(http.MethodPut, "/api/bases?name=primary", strings.NewReader(updateJSON(t, model.BaseUpdateRequest{
		Name: "renamed", Path: updatedPrimaryPath,
	}))))
	updateResponse := decodeSettingsResponse(t, update, http.StatusOK)
	assertSettingsState(t, fixture, updateResponse, "renamed", updatedPrimaryPath, true, 2)

	switchRecorder := httptest.NewRecorder()
	fixture.handler.SwitchBase(switchRecorder, httptest.NewRequest(http.MethodPost, "/api/bases/switch", strings.NewReader(switchJSON(t, model.BaseSwitchRequest{Name: "secondary"}))))
	switchResponse := decodeSettingsResponse(t, switchRecorder, http.StatusOK)
	assertSettingsState(t, fixture, switchResponse, "secondary", secondaryPath, true, 2)

	forget := httptest.NewRecorder()
	fixture.handler.ForgetBase(forget, httptest.NewRequest(http.MethodDelete, "/api/bases?name=renamed", nil))
	forgetResponse := decodeSettingsResponse(t, forget, http.StatusOK)
	assertSettingsState(t, fixture, forgetResponse, "secondary", secondaryPath, true, 1)
	if forgetResponse.Config.Bases[0].Name != "secondary" {
		t.Errorf("remaining base = %q, want secondary", forgetResponse.Config.Bases[0].Name)
	}
}

func TestSettingsHandlerRequiresUpdateForgetAndSwitchFields(t *testing.T) {
	t.Run("update query name before body", func(t *testing.T) {
		fixture := newSettingsHandlerFixture(t)
		recorder := httptest.NewRecorder()
		fixture.handler.UpdateBase(recorder, httptest.NewRequest(http.MethodPut, "/api/bases", strings.NewReader("{")))
		assertMissingField(t, recorder, "name")
	})

	t.Run("forget query name", func(t *testing.T) {
		fixture := newSettingsHandlerFixture(t)
		recorder := httptest.NewRecorder()
		fixture.handler.ForgetBase(recorder, httptest.NewRequest(http.MethodDelete, "/api/bases", nil))
		assertMissingField(t, recorder, "name")
	})

	for _, test := range []struct {
		name  string
		body  string
		field string
	}{
		{name: "update body name", body: `{}`, field: "name"},
		{name: "update body path", body: `{"name":"renamed"}`, field: "path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSettingsHandlerFixture(t)
			recorder := httptest.NewRecorder()
			fixture.handler.UpdateBase(recorder, httptest.NewRequest(http.MethodPut, "/api/bases?name=old", strings.NewReader(test.body)))
			assertMissingField(t, recorder, test.field)
		})
	}

	t.Run("switch body name", func(t *testing.T) {
		fixture := newSettingsHandlerFixture(t)
		recorder := httptest.NewRecorder()
		fixture.handler.SwitchBase(recorder, httptest.NewRequest(http.MethodPost, "/api/bases/switch", strings.NewReader(`{}`)))
		assertMissingField(t, recorder, "name")
	})
}

func TestSettingsHandlerSwitchUnknownBase(t *testing.T) {
	fixture := newSettingsHandlerFixture(t)
	basePath := t.TempDir()
	setup := httptest.NewRecorder()
	fixture.handler.CompleteSetup(setup, httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(mutationJSON(t, model.BaseMutationRequest{
		Mode: "connect", Name: "primary", Path: basePath,
	}))))
	decodeSettingsResponse(t, setup, http.StatusOK)
	recorder := httptest.NewRecorder()

	fixture.handler.SwitchBase(recorder, httptest.NewRequest(http.MethodPost, "/api/bases/switch", strings.NewReader(`{"name":"missing"}`)))

	assertAPIErrorResponse(t, recorder, http.StatusNotFound, model.APIError{Code: "base_not_found", Message: "base not found"})
}

func TestSettingsHandlerSaveConfigPreservesOmittedSetupState(t *testing.T) {
	for _, initiallyCompleted := range []bool{false, true} {
		name := "incomplete"
		if initiallyCompleted {
			name = "complete"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newSettingsHandlerFixture(t)
			if initiallyCompleted {
				setupPath := t.TempDir()
				setup := httptest.NewRecorder()
				fixture.handler.CompleteSetup(setup, httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(mutationJSON(t, model.BaseMutationRequest{
					Mode: "connect", Name: "setup", Path: setupPath,
				}))))
				decodeSettingsResponse(t, setup, http.StatusOK)
			}

			targetPath := t.TempDir()
			body, err := json.Marshal(map[string]any{
				"base_dir":     filepath.Dir(targetPath),
				"bases":        []model.Base{{Name: "replacement", Path: targetPath}},
				"current_base": "replacement",
			})
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			recorder := httptest.NewRecorder()

			fixture.handler.SaveConfig(recorder, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(string(body))))

			response := decodeSettingsResponse(t, recorder, http.StatusOK)
			assertSettingsState(t, fixture, response, "replacement", targetPath, initiallyCompleted, 1)
		})
	}
}

type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

func mutationJSON(t *testing.T, request model.BaseMutationRequest) string {
	t.Helper()
	return marshalHandlerJSON(t, request)
}

func updateJSON(t *testing.T, request model.BaseUpdateRequest) string {
	t.Helper()
	return marshalHandlerJSON(t, request)
}

func switchJSON(t *testing.T, request model.BaseSwitchRequest) string {
	t.Helper()
	return marshalHandlerJSON(t, request)
}

func marshalHandlerJSON(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(body)
}

func assertMissingField(t *testing.T, recorder *httptest.ResponseRecorder, field string) {
	t.Helper()
	assertAPIErrorResponse(t, recorder, http.StatusBadRequest, model.APIError{
		Code:    "missing_field",
		Message: "Missing required field",
		Field:   field,
	})
}

func decodeSettingsResponse(t *testing.T, recorder *httptest.ResponseRecorder, status int) model.SettingsResponse {
	t.Helper()
	var response model.SettingsResponse
	decodeHandlerJSON(t, recorder, status, &response)
	return response
}

func decodeHandlerJSON(t *testing.T, recorder *httptest.ResponseRecorder, status int, target any) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d; body = %q", recorder.Code, status, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	decoder := json.NewDecoder(recorder.Body)
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode response: %v; body = %q", err, recorder.Body.String())
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("response contains multiple JSON values: err = %v, extra = %#v", err, extra)
	}
}

func assertSettingsState(
	t *testing.T,
	fixture settingsHandlerFixture,
	response model.SettingsResponse,
	currentBase string,
	basePath string,
	setupCompleted bool,
	baseCount int,
) {
	t.Helper()
	basePath = filepath.Clean(basePath)
	if response.BasePath != basePath {
		t.Errorf("response base_path = %q, want %q", response.BasePath, basePath)
	}
	if response.Config.CurrentBase != currentBase {
		t.Errorf("response current_base = %q, want %q", response.Config.CurrentBase, currentBase)
	}
	if response.Config.SetupCompleted == nil || *response.Config.SetupCompleted != setupCompleted {
		t.Errorf("response setup_completed = %v, want %t", response.Config.SetupCompleted, setupCompleted)
	}
	if len(response.Config.Bases) != baseCount {
		t.Errorf("response bases count = %d, want %d", len(response.Config.Bases), baseCount)
	}
	if got := fixture.notes.GetBasePath(); got != basePath {
		t.Errorf("runtime base path = %q, want %q", got, basePath)
	}
	persisted, err := fixture.config.Load()
	if err != nil {
		t.Fatalf("ConfigService.Load() error = %v", err)
	}
	if persisted.CurrentBase != currentBase {
		t.Errorf("persisted current_base = %q, want %q", persisted.CurrentBase, currentBase)
	}
	if persisted.SetupCompleted == nil || *persisted.SetupCompleted != setupCompleted {
		t.Errorf("persisted setup_completed = %v, want %t", persisted.SetupCompleted, setupCompleted)
	}
	if len(persisted.Bases) != baseCount {
		t.Errorf("persisted bases count = %d, want %d", len(persisted.Bases), baseCount)
	}
	if !reflect.DeepEqual(*persisted, response.Config) {
		t.Errorf("persisted config = %#v, want response config %#v", *persisted, response.Config)
	}
}
