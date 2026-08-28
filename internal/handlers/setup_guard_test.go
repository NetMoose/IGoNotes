package handlers

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"IGoNotes/internal/model"
)

type mutableSetupState struct {
	mu        sync.RWMutex
	completed bool
}

func (s *mutableSetupState) SetupCompleted() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.completed
}

func (s *mutableSetupState) setCompleted(completed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = completed
}

func TestRequireSetupReadsCurrentStateForEveryRequest(t *testing.T) {
	state := &mutableSetupState{}
	calls := 0
	handler := RequireSetup(state, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))

	blocked := httptest.NewRecorder()
	handler.ServeHTTP(blocked, httptest.NewRequest(http.MethodGet, "/api/notes", nil))

	assertAPIErrorResponse(t, blocked, http.StatusPreconditionRequired, model.APIError{
		Code:    "setup_required",
		Message: "setup required",
	})
	if calls != 0 {
		t.Fatalf("next calls = %d, want 0", calls)
	}

	state.setCompleted(true)
	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, httptest.NewRequest(http.MethodGet, "/api/notes", nil))

	if allowed.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", allowed.Code, http.StatusNoContent)
	}
	if calls != 1 {
		t.Errorf("next calls = %d, want 1", calls)
	}
}
