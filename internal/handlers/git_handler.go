package handlers

import (
	"context"
	"net/http"
	"net/url"

	"IGoNotes/internal/model"
)

const badQueryMessage = "Invalid query parameters"

type GitProber interface {
	Probe(context.Context, model.GitProbeRequest) (model.GitProbeResponse, error)
}

type GitConfigurer interface {
	ConfigureGit(context.Context, string, model.GitConfigRequest) (model.GitConfigResponse, error)
	DisableGit(context.Context, string) (model.GitConfigResponse, error)
}

type GitStatusReader interface {
	Status(context.Context, string) (model.GitStatusResponse, error)
}

type GitHandler struct {
	prober     GitProber
	configurer GitConfigurer
	statuses   GitStatusReader
}

func NewGitHandler(prober GitProber, configurer GitConfigurer, statuses GitStatusReader) *GitHandler {
	return &GitHandler{prober: prober, configurer: configurer, statuses: statuses}
}

func (h *GitHandler) Probe(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var request model.GitProbeRequest
	if err := decodeSingleJSON(r.Body, &request); err != nil {
		writeBadJSON(w)
		return
	}
	if request.Base == "" {
		writeMissingField(w, "base")
		return
	}
	if request.GitURL == "" {
		writeMissingField(w, "git_url")
		return
	}

	response, err := h.prober.Probe(r.Context(), request)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *GitHandler) Configure(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	base, ok := readBaseQuery(w, r, true)
	if !ok {
		return
	}

	var request model.GitConfigRequest
	if err := decodeSingleJSON(r.Body, &request); err != nil {
		writeBadJSON(w)
		return
	}
	if request.GitURL == "" {
		writeMissingField(w, "git_url")
		return
	}
	if request.GitBranch == "" {
		writeMissingField(w, "git_branch")
		return
	}

	response, err := h.configurer.ConfigureGit(r.Context(), base, request)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *GitHandler) Disable(w http.ResponseWriter, r *http.Request) {
	base, ok := readBaseQuery(w, r, true)
	if !ok {
		return
	}

	response, err := h.configurer.DisableGit(r.Context(), base)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *GitHandler) Status(w http.ResponseWriter, r *http.Request) {
	base, ok := readBaseQuery(w, r, false)
	if !ok {
		return
	}

	response, err := h.statuses.Status(r.Context(), base)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func readBaseQuery(w http.ResponseWriter, r *http.Request, required bool) (string, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		WriteAPIError(w, http.StatusBadRequest, "bad_query", badQueryMessage, "")
		return "", false
	}
	for key := range query {
		if key != "base" {
			WriteAPIError(w, http.StatusBadRequest, "bad_query", badQueryMessage, "")
			return "", false
		}
	}

	values, present := query["base"]
	if len(values) > 1 {
		WriteAPIError(w, http.StatusBadRequest, "bad_query", badQueryMessage, "")
		return "", false
	}
	if !present || values[0] == "" {
		if required {
			writeMissingField(w, "base")
			return "", false
		}
		return "", true
	}
	return values[0], true
}
