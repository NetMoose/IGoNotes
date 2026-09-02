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
}
