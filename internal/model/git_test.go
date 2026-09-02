package model

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestBaseLegacyJSONRemainsReadable(t *testing.T) {
	var base Base
	if err := json.Unmarshal([]byte(`{"name":"notes","path":"/notes","git_url":"https://example.com/notes.git","auto_sync":true}`), &base); err != nil {
		t.Fatalf("unmarshal legacy base: %v", err)
	}

	if base.Name != "notes" || base.Path != "/notes" || base.GitURL != "https://example.com/notes.git" || !base.AutoSync {
		t.Fatalf("unexpected base: %+v", base)
	}
	if base.GitConfigured() {
		t.Fatal("URL-only legacy base must not be considered configured")
	}
}

func TestBaseGitConfigured(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		branch string
		want   bool
	}{
		{name: "empty"},
		{name: "URL only", url: "https://example.com/notes.git"},
		{name: "branch only", branch: "main"},
		{name: "both", url: "https://example.com/notes.git", branch: "main", want: true},
		{name: "whitespace URL", url: " \t\n", branch: "main"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := Base{GitURL: tt.url, GitBranch: tt.branch}
			if got := base.GitConfigured(); got != tt.want {
				t.Fatalf("GitConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGitDTOContracts(t *testing.T) {
	configResponse := GitConfigResponse{Base: Base{Name: "notes"}}
	if configResponse.Base.Name != "notes" {
		t.Fatalf("GitConfigResponse.Base = %+v, want Base value", configResponse.Base)
	}

	wantStates := []GitState{
		GitStateUnconfigured,
		GitStateInitializing,
		GitStateReady,
		GitStateSyncing,
		GitStateError,
		GitStatePaused,
		GitStateConflict,
		GitStateNeedsReconnect,
	}
	wantStateValues := []string{
		"unconfigured",
		"initializing",
		"ready",
		"syncing",
		"error",
		"paused",
		"conflict",
		"needs_reconnect",
	}
	for i, state := range wantStates {
		if string(state) != wantStateValues[i] {
			t.Errorf("GitState constant %d = %q, want %q", i, state, wantStateValues[i])
		}
	}

	tests := []struct {
		value any
		want  map[string]string
	}{
		{Base{}, map[string]string{
			"Name": "name", "Path": "path", "GitURL": "git_url,omitempty", "GitBranch": "git_branch,omitempty",
			"AutoSync": "auto_sync", "AutoSyncIntervalMinutes": "auto_sync_interval_minutes,omitempty",
			"GitCommitMessageTemplate": "git_commit_message_template,omitempty",
		}},
		{GitProbeRequest{}, map[string]string{"Base": "base", "GitURL": "git_url", "GitBranch": "git_branch,omitempty"}},
		{GitConfirmations{}, map[string]string{
			"CreateRepository": "create_repository", "ReplaceOrigin": "replace_origin", "CreateBranch": "create_branch", "MergeHistories": "merge_histories",
		}},
		{GitConfigRequest{}, map[string]string{
			"GitURL": "git_url", "GitBranch": "git_branch", "AutoSync": "auto_sync",
			"AutoSyncIntervalMinutes": "auto_sync_interval_minutes", "GitCommitMessageTemplate": "git_commit_message_template",
			"Confirmations": "confirmations",
		}},
		{GitRequiredMutations{}, map[string]string{
			"CreateRepository": "create_repository", "AddOrigin": "add_origin", "ReplaceOrigin": "replace_origin",
			"CreateBranch": "create_branch", "MergeHistories": "merge_histories",
		}},
		{GitProbeResponse{}, map[string]string{
			"Base": "base", "GitVersion": "git_version", "HasRepository": "has_repository", "RepositoryRoot": "repository_root,omitempty",
			"RepositoryRootMatches": "repository_root_matches", "CurrentBranch": "current_branch,omitempty", "DetachedHead": "detached_head",
			"WorkingTreeClean": "working_tree_clean", "ExistingOriginURL": "existing_origin_url,omitempty", "RemoteBranches": "remote_branches",
			"EmptyRemote": "empty_remote", "PendingOperation": "pending_operation,omitempty", "IdentityConfigured": "identity_configured",
			"HistoryRelation": "history_relation", "CanConfigure": "can_configure", "RequiredMutations": "required_mutations",
			"Warnings": "warnings", "BlockingError": "blocking_error,omitempty",
		}},
		{GitStatus{}, map[string]string{
			"Base": "base", "RepositoryPath": "repository_path,omitempty", "State": "state", "OperationID": "operation_id,omitempty",
			"Stage": "stage,omitempty", "Ahead": "ahead", "Behind": "behind", "ConsecutiveFailures": "consecutive_failures",
			"LastAttempt": "last_attempt,omitempty", "LastSuccess": "last_success,omitempty", "ChangedPaths": "changed_paths",
			"RemoteOID": "remote_oid,omitempty", "Error": "error,omitempty",
		}},
		{GitConfigResponse{}, map[string]string{"Base": "base", "Status": "status"}},
		{GitStatusResponse{}, map[string]string{"Statuses": "statuses"}},
	}

	for _, tt := range tests {
		typeOf := reflect.TypeOf(tt.value)
		for fieldName, wantTag := range tt.want {
			field, ok := typeOf.FieldByName(fieldName)
			if !ok {
				t.Errorf("%s is missing field %s", typeOf.Name(), fieldName)
				continue
			}
			if got := field.Tag.Get("json"); got != wantTag {
				t.Errorf("%s.%s JSON tag = %q, want %q", typeOf.Name(), fieldName, got, wantTag)
			}
		}
	}
}

func TestGitStatusJSONRoundTrip(t *testing.T) {
	lastAttempt := time.Date(2026, time.September, 2, 14, 30, 0, 0, time.UTC)
	want := GitStatus{
		Base:         "notes",
		State:        GitStateError,
		LastAttempt:  &lastAttempt,
		ChangedPaths: []string{"README.md", "daily/2026-09-02.md"},
		Error: &APIError{
			Code:    "git_push_failed",
			Message: "push rejected",
			Field:   "git_url",
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal GitStatus: %v", err)
	}

	var got GitStatus
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal GitStatus: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}
