package handlers

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"IGoNotes/internal/appinfo"
	"IGoNotes/internal/model"
)

// ClipperDiscoveryDestinations exposes only public discovery metadata.
type ClipperDiscoveryDestinations interface {
	ListBases() []string
	Folders(string) (model.ClipperFoldersResponse, error)
}

type ClipperDiscoveryHandler struct {
	destinations ClipperDiscoveryDestinations
}

func NewClipperDiscoveryHandler(destinations ClipperDiscoveryDestinations) *ClipperDiscoveryHandler {
	return &ClipperDiscoveryHandler{destinations: destinations}
}

func (h *ClipperDiscoveryHandler) Info(w http.ResponseWriter, r *http.Request) {
	writeClipperDiscovery(w, model.ClipperInfoResponse{
		ProtocolVersion:     model.ClipperProtocolVersion,
		AppName:             appinfo.Name,
		AppVersion:          appinfo.Version,
		Limits:              model.ClipperV1Limits(),
		SupportedImageTypes: []string{model.ClipperImagePNG, model.ClipperImageJPEG, model.ClipperImageGIF, model.ClipperImageWebP},
	})
}

func (h *ClipperDiscoveryHandler) Bases(w http.ResponseWriter, r *http.Request) {
	bases := h.destinations.ListBases()
	if bases == nil {
		bases = []string{}
	}
	writeClipperDiscovery(w, model.ClipperBasesResponse{Bases: bases})
}

func (h *ClipperDiscoveryHandler) Folders(w http.ResponseWriter, r *http.Request) {
	// Bound parsing work and require one explicit destination, never current_base.
	const maxQueryBytes = 8192
	const maxBaseBytes = 1024
	if len(r.URL.RawQuery) > maxQueryBytes {
		writeServiceError(w, &model.ClipperError{Code: model.ClipperInvalidRequest})
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	bases := query["base"]
	if err != nil || len(bases) != 1 || strings.TrimSpace(bases[0]) == "" || len(bases[0]) > maxBaseBytes {
		writeServiceError(w, &model.ClipperError{Code: model.ClipperInvalidRequest})
		return
	}
	response, err := h.destinations.Folders(bases[0])
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if response.Folders == nil {
		response.Folders = []string{}
	}
	writeClipperDiscovery(w, response)
}

func writeClipperDiscovery(w http.ResponseWriter, response any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(response)
}
