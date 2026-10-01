package handlers

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	gitcmd "IGoNotes/internal/git"
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

type GitOperationConfigurer interface {
	GitConfigurer
	ConfigureGitForInitialize(context.Context, string, model.GitConfigRequest) (model.GitConfigResponse, gitcmd.ConfiguredBase, error)
	GitSnapshot(string) (gitcmd.ConfiguredBase, bool, error)
}

type GitOperations interface {
	QueueInitialize(context.Context, gitcmd.InitializeRequest) (gitcmd.Operation, bool, error)
	QueueSync(context.Context, gitcmd.SyncRequest) (gitcmd.Operation, bool, error)
	Resume(context.Context, string) (gitcmd.Operation, bool, error)
}

type GitHandler struct {
	prober          GitProber
	configurer      GitConfigurer
	statuses        GitStatusReader
	operationConfig GitOperationConfigurer
	operations      GitOperations
}

func NewGitHandler(prober GitProber, configurer GitConfigurer, statuses GitStatusReader) *GitHandler {
	return &GitHandler{prober: prober, configurer: configurer, statuses: statuses}
}

func NewGitHandlerWithOperations(prober GitProber, configurer GitOperationConfigurer, statuses GitStatusReader, operations GitOperations) *GitHandler {
	return &GitHandler{
		prober:          prober,
		configurer:      configurer,
		statuses:        statuses,
		operationConfig: configurer,
		operations:      operations,
	}
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

	if h.operationConfig == nil || h.operations == nil {
		response, err := h.configurer.ConfigureGit(r.Context(), base, request)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	response, snapshot, err := h.operationConfig.ConfigureGitForInitialize(r.Context(), base, request)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	operation, deduplicated, err := h.operations.QueueInitialize(r.Context(), gitcmd.InitializeRequest{
		Snapshot:      snapshot,
		Confirmations: request.Confirmations,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	statuses, err := h.statuses.Status(r.Context(), base)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if len(statuses.Statuses) != 1 {
		writeServiceError(w, errInvalidGitStatusResponse)
		return
	}
	response.Status = statuses.Statuses[0]
	operationResult := gitOperationResponse(operation, deduplicated)
	response.Operation = &operationResult
	writeJSON(w, http.StatusAccepted, response)
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

func (h *GitHandler) Sync(w http.ResponseWriter, r *http.Request) {
	base, ok := readBaseQuery(w, r, true)
	if !ok {
		return
	}
	if h.operationConfig == nil || h.operations == nil {
		writeServiceError(w, errGitOperationsNotInitialized)
		return
	}

	snapshot, _, err := h.operationConfig.GitSnapshot(base)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	operation, deduplicated, err := h.operations.QueueSync(r.Context(), gitcmd.SyncRequest{Snapshot: snapshot})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, gitOperationResponse(operation, deduplicated))
}

func (h *GitHandler) Resume(w http.ResponseWriter, r *http.Request) {
	base, ok := readBaseQuery(w, r, true)
	if !ok {
		return
	}
	base = strings.TrimSpace(base)
	if base == "" {
		writeMissingField(w, "base")
		return
	}
	if h.operations == nil {
		writeServiceError(w, errGitOperationsNotInitialized)
		return
	}
	operation, deduplicated, err := h.operations.Resume(r.Context(), base)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, gitOperationResponse(operation, deduplicated))
}

func operationResponse(operation gitcmd.Operation, deduplicated bool) *model.GitOperationResponse {
	response := gitOperationResponse(operation, deduplicated)
	return &response
}

func gitOperationResponse(operation gitcmd.Operation, deduplicated bool) model.GitOperationResponse {
	return model.GitOperationResponse{
		OperationID:  operation.ID,
		Status:       string(operation.State),
		Deduplicated: deduplicated,
	}
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
