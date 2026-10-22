package handlers

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"

	"IGoNotes/internal/model"
)

type ClipImportRecovery interface {
	ListUnfinished(context.Context) ([]model.ClipperImportSummary, error)
	Abandon(context.Context, string) (model.ClipperImportStatusResponse, error)
}
type ClipImportManagementHandler struct{ imports ClipImportRecovery }

func NewClipImportManagementHandler(imports ClipImportRecovery) *ClipImportManagementHandler {
	return &ClipImportManagementHandler{imports: imports}
}

func RegisterClipImportManagementRoutes(mux *http.ServeMux, h *ClipImportManagementHandler, state SetupState) {
	register := func(path, method string, handler http.HandlerFunc) {
		// Local-origin rejection precedes method/setup checks. A Clipper credential
		// is never administrative authority, including when Origin is absent.
		guarded := RequireLocalOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(r.Header.Values("Authorization")) != 0 {
				WriteAPIError(w, http.StatusForbidden, "forbidden_origin", "Forbidden request", "")
				return
			}
			methods(map[string]http.Handler{method: RequireSetup(state, handler)}).ServeHTTP(w, r)
		}))
		mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			guarded.ServeHTTP(w, r)
		}))
	}
	register("/api/clipper/imports", http.MethodGet, h.List)
	register("/api/clipper/imports/abandon", http.MethodPost, h.Abandon)
}

func emptyClipManagementBody(r *http.Request) bool {
	if r.Body == nil {
		return true
	}
	var b [1]byte
	n, err := io.ReadFull(importContextReader{r.Context(), r.Body}, b[:])
	return n == 0 && err == io.EOF
}

func (h *ClipImportManagementHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || !emptyClipManagementBody(r) {
		writeServiceError(w, invalidClipImport())
		return
	}
	imports, err := h.imports.ListUnfinished(r.Context())
	if err != nil {
		writeClipManagementError(w, err)
		return
	}
	if imports == nil {
		imports = []model.ClipperImportSummary{}
	}
	writeClipImportJSON(w, http.StatusOK, model.ClipperImportsResponse{Imports: imports})
}
func (h *ClipImportManagementHandler) Abandon(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.RawQuery) > 8192 {
		writeServiceError(w, invalidClipImport())
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	ids := query["id"]
	if err != nil || len(query) != 1 || len(ids) != 1 || !clipImportID.MatchString(ids[0]) || !emptyClipManagementBody(r) {
		writeServiceError(w, invalidClipImport())
		return
	}
	status, err := h.imports.Abandon(r.Context(), ids[0])
	if err != nil {
		writeClipManagementError(w, err)
		return
	}
	writeClipImportJSON(w, http.StatusOK, status)
}
func writeClipManagementError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		err = invalidClipImport()
	}
	writeServiceError(w, err)
}
