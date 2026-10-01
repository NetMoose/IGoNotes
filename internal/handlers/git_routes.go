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
		http.MethodPut:    guarded(http.HandlerFunc(handler.Configure)),
		http.MethodDelete: guarded(http.HandlerFunc(handler.Disable)),
	})))
	mux.Handle("/api/git/status", RequireLocalOrigin(methods(map[string]http.Handler{
		http.MethodGet: guarded(http.HandlerFunc(handler.Status)),
	})))
	mux.Handle("/api/git/sync", RequireLocalOrigin(methods(map[string]http.Handler{
		http.MethodPost: guarded(http.HandlerFunc(handler.Sync)),
	})))
	mux.Handle("/api/git/resume", RequireLocalOrigin(methods(map[string]http.Handler{
		http.MethodPost: guarded(http.HandlerFunc(handler.Resume)),
	})))
}

func RegisterGitConflictRoutes(mux *http.ServeMux, handler *GitConflictHandler, state SetupState) {
	guarded := func(next http.Handler) http.Handler {
		return RequireSetup(state, next)
	}

	mux.Handle("/api/git/conflicts", RequireLocalOrigin(methods(map[string]http.Handler{
		http.MethodGet: guarded(http.HandlerFunc(handler.List)),
	})))
	mux.Handle("/api/git/conflicts/resolve", RequireLocalOrigin(methods(map[string]http.Handler{
		http.MethodPut: guarded(http.HandlerFunc(handler.Resolve)),
	})))
	mux.Handle("/api/git/conflicts/complete", RequireLocalOrigin(methods(map[string]http.Handler{
		http.MethodPost: guarded(http.HandlerFunc(handler.Complete)),
	})))
	mux.Handle("/api/git/conflicts/abort", RequireLocalOrigin(methods(map[string]http.Handler{
		http.MethodPost: guarded(http.HandlerFunc(handler.Abort)),
	})))
}
