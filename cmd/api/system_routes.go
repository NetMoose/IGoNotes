package main

import (
	"net/http"

	"IGoNotes/internal/handlers"
)

func registerSystemRoutes(mux *http.ServeMux, systemHandler *handlers.SystemHandler) {
	mux.Handle(
		"/api/system/select-directory",
		handlers.RequireLocalOrigin(http.HandlerFunc(systemHandler.SelectDirectory)),
	)
}
