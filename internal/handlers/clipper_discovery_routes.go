package handlers

import "net/http"

func RegisterClipperDiscoveryRoutes(mux *http.ServeMux, handler *ClipperDiscoveryHandler, auth ClipperAuthenticator, state SetupState) {
	// Keep unknown v1 paths out of the SPA, with the same auth/setup ordering
	// as exact routes. Register the slashless root too, avoiding mux redirects.
	notFound := RequireClipper(auth, RequireSetup(state, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteAPIError(w, http.StatusNotFound, "not_found", "Not found", "")
	})), http.MethodGet)
	mux.Handle("/api/clipper/v1", notFound)
	mux.Handle("/api/clipper/v1/", notFound)
	for path, next := range map[string]http.HandlerFunc{
		"/api/clipper/v1/info":    handler.Info,
		"/api/clipper/v1/bases":   handler.Bases,
		"/api/clipper/v1/folders": handler.Folders,
	} {
		// Preflight terminates in RequireClipper; actual requests authenticate
		// before setup is inspected. These are exact paths, not subtrees.
		mux.Handle(path, RequireClipper(auth, RequireSetup(state, next), http.MethodGet))
	}
}
