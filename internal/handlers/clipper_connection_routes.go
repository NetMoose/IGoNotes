package handlers

import "net/http"

func RegisterClipperConnectionRoutes(mux *http.ServeMux, handler *ClipperConnectionHandler, state SetupState) {
	// Unknown management paths retain the local UI guard. The more specific
	// v1 namespace and exact management routes take precedence over this fallback.
	notFound := RequireLocalOrigin(RequireSetup(state, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteAPIError(w, http.StatusNotFound, "not_found", "Not found", "")
	})))
	mux.Handle("/api/clipper", notFound)
	mux.Handle("/api/clipper/", notFound)
	mux.Handle("/api/clipper/connections", RequireLocalOrigin(methods(map[string]http.Handler{
		http.MethodGet:    RequireSetup(state, http.HandlerFunc(handler.List)),
		http.MethodPost:   RequireSetup(state, http.HandlerFunc(handler.Create)),
		http.MethodDelete: RequireSetup(state, http.HandlerFunc(handler.Revoke)),
	})))
}
