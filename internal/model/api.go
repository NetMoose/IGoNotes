package model

import "time"

type GitState string

const (
	GitStateUnconfigured   GitState = "unconfigured"
	GitStateInitializing   GitState = "initializing"
	GitStateReady          GitState = "ready"
	GitStateSyncing        GitState = "syncing"
	GitStateError          GitState = "error"
	GitStatePaused         GitState = "paused"
	GitStateConflict       GitState = "conflict"
	GitStateNeedsReconnect GitState = "needs_reconnect"
)

type CreateNoteRequest struct {
	ParentID string `json:"parent_id"` // пустая строка для корня
	Name     string `json:"name"`
	Type     string `json:"type"` // "file" или "dir"
}

type SaveNoteRequest struct {
	ID               string  `json:"id"`
	Content          string  `json:"content"`
	ExpectedRevision *string `json:"expected_revision,omitempty"`
}

type NoteContentResponse struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Revision string `json:"revision"`
}

type SaveNoteResponse struct {
	Status   string `json:"status"`
	Revision string `json:"revision"`
}

type RenameRequest struct {
	ID      string `json:"id"`
	NewName string `json:"new_name"`
}

type BaseMutationRequest struct {
	Mode string `json:"mode"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type BaseUpdateRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type BaseSwitchRequest struct {
	Name string `json:"name"`
}

type SettingsResponse struct {
	Config   Config `json:"config"`
	BasePath string `json:"base_path"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

type GitProbeRequest struct {
	Base      string `json:"base"`
	GitURL    string `json:"git_url"`
	GitBranch string `json:"git_branch,omitempty"`
}

type GitConfirmations struct {
	CreateRepository bool `json:"create_repository"`
	ReplaceOrigin    bool `json:"replace_origin"`
	CreateBranch     bool `json:"create_branch"`
	MergeHistories   bool `json:"merge_histories"`
}

type GitConfigRequest struct {
	GitURL                   string           `json:"git_url"`
	GitBranch                string           `json:"git_branch"`
	AutoSync                 bool             `json:"auto_sync"`
	AutoSyncIntervalMinutes  int              `json:"auto_sync_interval_minutes"`
	GitCommitMessageTemplate string           `json:"git_commit_message_template"`
	Confirmations            GitConfirmations `json:"confirmations"`
}

type GitRequiredMutations struct {
	CreateRepository bool `json:"create_repository"`
	AddOrigin        bool `json:"add_origin"`
	ReplaceOrigin    bool `json:"replace_origin"`
	CreateBranch     bool `json:"create_branch"`
	MergeHistories   bool `json:"merge_histories"`
}

type GitProbeResponse struct {
	Base                  string               `json:"base"`
	GitVersion            string               `json:"git_version"`
	HasRepository         bool                 `json:"has_repository"`
	RepositoryRoot        string               `json:"repository_root,omitempty"`
	RepositoryRootMatches bool                 `json:"repository_root_matches"`
	CurrentBranch         string               `json:"current_branch,omitempty"`
	DetachedHead          bool                 `json:"detached_head"`
	WorkingTreeClean      bool                 `json:"working_tree_clean"`
	ExistingOriginURL     string               `json:"existing_origin_url,omitempty"`
	RemoteBranches        []string             `json:"remote_branches"`
	EmptyRemote           bool                 `json:"empty_remote"`
	PendingOperation      string               `json:"pending_operation,omitempty"`
	IdentityConfigured    bool                 `json:"identity_configured"`
	HistoryRelation       string               `json:"history_relation"`
	CanConfigure          bool                 `json:"can_configure"`
	RequiredMutations     GitRequiredMutations `json:"required_mutations"`
	Warnings              []string             `json:"warnings"`
	BlockingError         *APIError            `json:"blocking_error,omitempty"`
}

type GitStatus struct {
	Base                string     `json:"base"`
	RepositoryPath      string     `json:"repository_path,omitempty"`
	State               GitState   `json:"state"`
	OperationID         string     `json:"operation_id,omitempty"`
	Stage               string     `json:"stage,omitempty"`
	Ahead               int        `json:"ahead"`
	Behind              int        `json:"behind"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastAttempt         *time.Time `json:"last_attempt,omitempty"`
	LastSuccess         *time.Time `json:"last_success,omitempty"`
	ChangedPaths        []string   `json:"changed_paths"`
	RemoteOID           string     `json:"remote_oid,omitempty"`
	Error               *APIError  `json:"error,omitempty"`
}

type GitConfigResponse struct {
	Base      Base                  `json:"base"`
	Status    GitStatus             `json:"status"`
	Operation *GitOperationResponse `json:"operation,omitempty"`
}

type GitOperationResponse struct {
	OperationID  string `json:"operation_id"`
	Status       string `json:"status"`
	Deduplicated bool   `json:"deduplicated"`
}

type GitStatusResponse struct {
	Statuses []GitStatus `json:"statuses"`
}

type GitConflictKind string
type GitConflictContentKind string
type GitConflictAction string

const (
	GitConflictContent      GitConflictKind = "content"
	GitConflictAddAdd       GitConflictKind = "add_add"
	GitConflictModifyDelete GitConflictKind = "modify_delete"
	GitConflictRenameDelete GitConflictKind = "rename_delete"

	GitConflictText   GitConflictContentKind = "text"
	GitConflictBinary GitConflictContentKind = "binary"

	GitConflictUseLocal  GitConflictAction = "local"
	GitConflictUseRemote GitConflictAction = "remote"
	GitConflictManual    GitConflictAction = "manual"
	GitConflictDelete    GitConflictAction = "delete"
	GitConflictKeepBoth  GitConflictAction = "keep_both"
)

type GitConflictStage struct {
	Path             string  `json:"path"`
	OID              string  `json:"oid"`
	Mode             string  `json:"mode"`
	Size             int64   `json:"size"`
	Content          *string `json:"content,omitempty"`
	PreviewTruncated bool    `json:"preview_truncated"`
}

type GitConflict struct {
	ID           string                 `json:"id"`
	Kind         GitConflictKind        `json:"kind"`
	ContentKind  GitConflictContentKind `json:"content_kind"`
	Path         string                 `json:"path"`
	OriginalPath string                 `json:"original_path,omitempty"`
	Base         *GitConflictStage      `json:"base,omitempty"`
	Local        *GitConflictStage      `json:"local,omitempty"`
	Remote       *GitConflictStage      `json:"remote,omitempty"`
	Actions      []GitConflictAction    `json:"actions"`
}

type GitConflictListResponse struct {
	Base         string        `json:"base"`
	OperationID  string        `json:"operation_id"`
	HeadOID      string        `json:"head_oid"`
	MergeHeadOID string        `json:"merge_head_oid"`
	Conflicts    []GitConflict `json:"conflicts"`
	CanComplete  bool          `json:"can_complete"`
}

type GitConflictResolveRequest struct {
	Base        string            `json:"base"`
	OperationID string            `json:"operation_id"`
	ConflictID  string            `json:"conflict_id"`
	Path        string            `json:"path"`
	Action      GitConflictAction `json:"action"`
	ResultPath  string            `json:"result_path,omitempty"`
	Content     *string           `json:"content,omitempty"`
	LocalPath   string            `json:"local_path,omitempty"`
	RemotePath  string            `json:"remote_path,omitempty"`
	LocalOID    string            `json:"local_oid,omitempty"`
	RemoteOID   string            `json:"remote_oid,omitempty"`
}

type GitConflictResolveResponse struct {
	ResolvedPath string                  `json:"resolved_path"`
	Remaining    GitConflictListResponse `json:"remaining"`
}
