package git

import (
	"reflect"
	"strings"
	"testing"

	"IGoNotes/internal/model"
)

func TestCleanConflictPath(t *testing.T) {
	for _, value := range []string{
		"note.md",
		"notes/idea [final].md",
		"-leading/space name",
		".gitignore",
		"literal\\backslash.txt",
	} {
		t.Run("accept "+value, func(t *testing.T) {
			got, err := cleanConflictPath(value, "result_path")
			if err != nil || got != value {
				t.Fatalf("cleanConflictPath(%q) = %q, %v", value, got, err)
			}
		})
	}

	for _, value := range []string{
		"",
		"\x00",
		"/absolute.md",
		".",
		"../escape.md",
		"notes/../../escape.md",
		"notes//idea.md",
		"notes/./idea.md",
		"notes/idea.md/",
		".git",
		"notes/.git/config",
		"notes/.GiT/config",
	} {
		t.Run("reject "+strings.ReplaceAll(value, "\x00", "NUL"), func(t *testing.T) {
			_, err := cleanConflictPath(value, "result_path")
			if err == nil {
				t.Fatalf("cleanConflictPath(%q) succeeded", value)
			}
			field, ok := err.(*SafeError)
			if !ok || field.Field != "result_path" || field == ErrConflictUnsupported {
				t.Fatalf("cleanConflictPath(%q) error = %#v", value, err)
			}
		})
	}
}

func TestValidateResolution(t *testing.T) {
	conflict := resolutionTestConflict()
	content := "merged\n"
	valid := []struct {
		name    string
		request model.GitConflictResolveRequest
	}{
		{"local", resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "local.md", LocalOID: "local-oid"})},
		{"remote", resolutionRequest(model.GitConflictUseRemote, model.GitConflictResolveRequest{ResultPath: "remote.md", RemoteOID: "remote-oid"})},
		{"manual", resolutionRequest(model.GitConflictManual, model.GitConflictResolveRequest{ResultPath: "manual.md", Content: &content})},
		{"keep both", resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{LocalPath: "local.md", RemotePath: "remote.md", LocalOID: "local-oid", RemoteOID: "remote-oid"})},
		{"delete", resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{})},
	}
	for _, test := range valid {
		t.Run("accept "+test.name, func(t *testing.T) {
			if err := validateResolution(conflict, test.request); err != nil {
				t.Fatalf("validateResolution() error = %v", err)
			}
		})
	}

	t.Run("ignores non-empty differing operation identity for manager", func(t *testing.T) {
		request := resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{})
		request.OperationID = "another-operation"
		if err := validateResolution(conflict, request); err != nil {
			t.Fatalf("validateResolution() error = %v", err)
		}
	})

	t.Run("does not require operation identity at manager boundary", func(t *testing.T) {
		request := resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{})
		request.OperationID = ""
		if err := validateResolution(conflict, request); err != nil {
			t.Fatalf("validateResolution() error = %v", err)
		}
	})

	for _, test := range []struct {
		name    string
		request model.GitConflictResolveRequest
	}{
		{"conflict ID", resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{ConflictID: "changed"})},
		{"path", resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{Path: "changed.md"})},
		{"local OID", resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "note.md", LocalOID: "changed"})},
		{"remote OID", resolutionRequest(model.GitConflictUseRemote, model.GitConflictResolveRequest{ResultPath: "note.md", RemoteOID: "changed"})},
	} {
		t.Run("stale "+test.name, func(t *testing.T) {
			if err := validateResolution(conflict, test.request); err != ErrConflictStale {
				t.Fatalf("validateResolution() error = %v, want ErrConflictStale", err)
			}
		})
	}

	for _, test := range []struct {
		name    string
		request model.GitConflictResolveRequest
		field   string
	}{
		{"action not offered", resolutionRequest(model.GitConflictAction("unknown"), model.GitConflictResolveRequest{}), "action"},
		{"local missing result", resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{LocalOID: "local-oid"}), "result_path"},
		{"remote missing result", resolutionRequest(model.GitConflictUseRemote, model.GitConflictResolveRequest{RemoteOID: "remote-oid"}), "result_path"},
		{"manual missing result", resolutionRequest(model.GitConflictManual, model.GitConflictResolveRequest{Content: &content}), "result_path"},
		{"manual missing content", resolutionRequest(model.GitConflictManual, model.GitConflictResolveRequest{ResultPath: "manual.md"}), "content"},
		{"keep both missing local path", resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{RemotePath: "remote.md", LocalOID: "local-oid", RemoteOID: "remote-oid"}), "local_path"},
		{"keep both missing remote path", resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{LocalPath: "local.md", LocalOID: "local-oid", RemoteOID: "remote-oid"}), "remote_path"},
		{"keep both same path", resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{LocalPath: "same.md", RemotePath: "same.md", LocalOID: "local-oid", RemoteOID: "remote-oid"}), "remote_path"},
		{"local content", resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "note.md", LocalOID: "local-oid", Content: &content}), "content"},
		{"remote local source", resolutionRequest(model.GitConflictUseRemote, model.GitConflictResolveRequest{ResultPath: "note.md", RemoteOID: "remote-oid", LocalPath: "local.md"}), "local_path"},
		{"manual remote source", resolutionRequest(model.GitConflictManual, model.GitConflictResolveRequest{ResultPath: "manual.md", Content: &content, RemoteOID: "remote-oid"}), "remote_oid"},
		{"keep both result", resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{ResultPath: "result.md", LocalPath: "local.md", RemotePath: "remote.md", LocalOID: "local-oid", RemoteOID: "remote-oid"}), "result_path"},
		{"delete output", resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{ResultPath: "result.md"}), "result_path"},
		{"delete source", resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{LocalOID: "local-oid"}), "local_oid"},
		{"invalid result path", resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "../escape.md", LocalOID: "local-oid"}), "result_path"},
		{"invalid keep both path", resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{LocalPath: "local.md", RemotePath: ".git/config", LocalOID: "local-oid", RemoteOID: "remote-oid"}), "remote_path"},
	} {
		t.Run("reject "+test.name, func(t *testing.T) {
			assertConflictField(t, validateResolution(conflict, test.request), test.field)
		})
	}

	t.Run("selected stage must exist", func(t *testing.T) {
		conflict := resolutionTestConflict()
		conflict.Local = nil
		request := resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "note.md", LocalOID: "local-oid"})
		assertConflictField(t, validateResolution(conflict, request), "action")
	})
}

func TestBuildResolutionPlan(t *testing.T) {
	content := "manual bytes\x00remain exact"
	for _, test := range []struct {
		name     string
		request  model.GitConflictResolveRequest
		conflict Conflict
		want     resolutionPlan
	}{
		{
			name:     "local",
			request:  resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "local.md", LocalOID: "local-oid"}),
			conflict: resolutionTestConflict(),
			want:     resolutionPlan{RemovePaths: []string{"note.md", "old.md"}, Writes: []resolutionWrite{{Path: "local.md", Stage: 2, OID: "local-oid", Mode: "100755"}}, StagePaths: []string{"local.md", "note.md", "old.md"}},
		},
		{
			name:     "remote",
			request:  resolutionRequest(model.GitConflictUseRemote, model.GitConflictResolveRequest{ResultPath: "remote.md", RemoteOID: "remote-oid"}),
			conflict: resolutionTestConflict(),
			want:     resolutionPlan{RemovePaths: []string{"note.md", "old.md"}, Writes: []resolutionWrite{{Path: "remote.md", Stage: 3, OID: "remote-oid", Mode: "120000"}}, StagePaths: []string{"note.md", "old.md", "remote.md"}},
		},
		{
			name:     "manual uses local mode and exact data",
			request:  resolutionRequest(model.GitConflictManual, model.GitConflictResolveRequest{ResultPath: "manual.md", Content: &content}),
			conflict: resolutionTestConflict(),
			want:     resolutionPlan{RemovePaths: []string{"note.md", "old.md"}, Writes: []resolutionWrite{{Path: "manual.md", Mode: "100755", Data: []byte(content)}}, StagePaths: []string{"manual.md", "note.md", "old.md"}},
		},
		{
			name:     "manual falls back to remote mode",
			request:  resolutionRequest(model.GitConflictManual, model.GitConflictResolveRequest{ResultPath: "manual.md", Content: &content}),
			conflict: Conflict{ID: "conflict-id", Path: "note.md", Actions: []string{"manual"}, Remote: &ConflictStage{OID: "remote-oid", Mode: "120000"}},
			want:     resolutionPlan{RemovePaths: []string{"note.md"}, Writes: []resolutionWrite{{Path: "manual.md", Mode: "120000", Data: []byte(content)}}, StagePaths: []string{"manual.md", "note.md"}},
		},
		{
			name:     "manual defaults mode",
			request:  resolutionRequest(model.GitConflictManual, model.GitConflictResolveRequest{ResultPath: "manual.md", Content: &content}),
			conflict: Conflict{ID: "conflict-id", Path: "note.md", Actions: []string{"manual"}},
			want:     resolutionPlan{RemovePaths: []string{"note.md"}, Writes: []resolutionWrite{{Path: "manual.md", Mode: "100644", Data: []byte(content)}}, StagePaths: []string{"manual.md", "note.md"}},
		},
		{
			name:     "keep both",
			request:  resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{LocalPath: "local.md", RemotePath: "remote.md", LocalOID: "local-oid", RemoteOID: "remote-oid"}),
			conflict: resolutionTestConflict(),
			want:     resolutionPlan{RemovePaths: []string{"note.md", "old.md"}, Writes: []resolutionWrite{{Path: "local.md", Stage: 2, OID: "local-oid", Mode: "100755"}, {Path: "remote.md", Stage: 3, OID: "remote-oid", Mode: "120000"}}, StagePaths: []string{"local.md", "note.md", "old.md", "remote.md"}},
		},
		{
			name:     "delete",
			request:  resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{}),
			conflict: resolutionTestConflict(),
			want:     resolutionPlan{RemovePaths: []string{"note.md", "old.md"}, Writes: []resolutionWrite{}, StagePaths: []string{"note.md", "old.md"}},
		},
		{
			name:     "reused source is not removed",
			request:  resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "note.md", LocalOID: "local-oid"}),
			conflict: resolutionTestConflict(),
			want:     resolutionPlan{RemovePaths: []string{"old.md"}, Writes: []resolutionWrite{{Path: "note.md", Stage: 2, OID: "local-oid", Mode: "100755"}}, StagePaths: []string{"note.md", "old.md"}},
		},
		{
			name:     "only reused source has an explicit empty removal list",
			request:  resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "note.md", LocalOID: "local-oid"}),
			conflict: Conflict{ID: "conflict-id", Path: "note.md", Actions: []string{"local"}, Local: &ConflictStage{OID: "local-oid", Mode: "100755"}},
			want:     resolutionPlan{RemovePaths: []string{}, Writes: []resolutionWrite{{Path: "note.md", Stage: 2, OID: "local-oid", Mode: "100755"}}, StagePaths: []string{"note.md"}},
		},
		{
			name:     "sorts unsorted keep both inputs bytewise",
			request:  resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{Path: "z-source.md", LocalPath: "z-output.md", RemotePath: "a-output.md", LocalOID: "local-oid", RemoteOID: "remote-oid"}),
			conflict: Conflict{ID: "conflict-id", Path: "z-source.md", OriginalPath: "a-source.md", Actions: []string{"keep_both"}, Local: &ConflictStage{OID: "local-oid", Mode: "100755"}, Remote: &ConflictStage{OID: "remote-oid", Mode: "120000"}},
			want:     resolutionPlan{RemovePaths: []string{"a-source.md", "z-source.md"}, Writes: []resolutionWrite{{Path: "a-output.md", Stage: 3, OID: "remote-oid", Mode: "120000"}, {Path: "z-output.md", Stage: 2, OID: "local-oid", Mode: "100755"}}, StagePaths: []string{"a-output.md", "a-source.md", "z-output.md", "z-source.md"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := buildResolutionPlan(test.conflict, []Conflict{test.conflict}, test.request)
			if err != nil {
				t.Fatalf("buildResolutionPlan() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("buildResolutionPlan() = %#v, want %#v", got, test.want)
			}
		})
	}

	for _, test := range []struct {
		name     string
		conflict Conflict
		request  model.GitConflictResolveRequest
	}{
		{"missing local stage", Conflict{ID: "conflict-id", Path: "note.md", Actions: []string{"local"}}, resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "note.md"})},
		{"missing selected mode", Conflict{ID: "conflict-id", Path: "note.md", Actions: []string{"remote"}, Remote: &ConflictStage{OID: "remote-oid"}}, resolutionRequest(model.GitConflictUseRemote, model.GitConflictResolveRequest{ResultPath: "note.md", RemoteOID: "remote-oid"})},
		{"unsupported selected mode", Conflict{ID: "conflict-id", Path: "note.md", Actions: []string{"local"}, Local: &ConflictStage{OID: "local-oid", Mode: "160000"}}, resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "note.md", LocalOID: "local-oid"})},
	} {
		t.Run("reject "+test.name, func(t *testing.T) {
			if _, err := buildResolutionPlan(test.conflict, []Conflict{test.conflict}, test.request); err == nil {
				t.Fatal("buildResolutionPlan() succeeded")
			}
		})
	}

	t.Run("rejects output collision with another conflict", func(t *testing.T) {
		conflict := resolutionTestConflict()
		other := Conflict{ID: "other", Path: "taken.md", OriginalPath: "legacy.md"}
		request := resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "taken.md", LocalOID: "local-oid"})
		if _, err := buildResolutionPlan(conflict, []Conflict{conflict, other}, request); err == nil {
			t.Fatal("buildResolutionPlan() succeeded")
		}
	})

	t.Run("rejects output collision with another conflict original path", func(t *testing.T) {
		conflict := resolutionTestConflict()
		other := Conflict{ID: "other", Path: "other.md", OriginalPath: "taken.md"}
		request := resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "taken.md", LocalOID: "local-oid"})
		if _, err := buildResolutionPlan(conflict, []Conflict{conflict, other}, request); err == nil {
			t.Fatal("buildResolutionPlan() succeeded")
		}
	})

	t.Run("rejects source collision with another conflict", func(t *testing.T) {
		conflict := resolutionTestConflict()
		conflict.OriginalPath = "taken.md"
		other := Conflict{ID: "other", Path: "taken.md"}
		request := resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{})
		if _, err := buildResolutionPlan(conflict, []Conflict{conflict, other}, request); err == nil {
			t.Fatal("buildResolutionPlan() succeeded")
		}
	})

	t.Run("rejects source collision with another conflict original path", func(t *testing.T) {
		conflict := resolutionTestConflict()
		conflict.OriginalPath = "taken.md"
		other := Conflict{ID: "other", Path: "other.md", OriginalPath: "taken.md"}
		request := resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{})
		if _, err := buildResolutionPlan(conflict, []Conflict{conflict, other}, request); err == nil {
			t.Fatal("buildResolutionPlan() succeeded")
		}
	})
}

func resolutionTestConflict() Conflict {
	return Conflict{
		ID:           "conflict-id",
		Path:         "note.md",
		OriginalPath: "old.md",
		Local:        &ConflictStage{OID: "local-oid", Mode: "100755"},
		Remote:       &ConflictStage{OID: "remote-oid", Mode: "120000"},
		Actions:      []string{"local", "remote", "manual", "keep_both", "delete"},
	}
}

func resolutionRequest(action model.GitConflictAction, request model.GitConflictResolveRequest) model.GitConflictResolveRequest {
	request.OperationID = "operation-id"
	if request.ConflictID == "" {
		request.ConflictID = "conflict-id"
	}
	if request.Path == "" {
		request.Path = "note.md"
	}
	request.Action = action
	return request
}

func assertConflictField(t *testing.T, err error, want string) {
	t.Helper()
	field, ok := err.(*SafeError)
	if !ok || field.Field != want || field == ErrConflictUnsupported {
		t.Fatalf("error = %#v, want fresh field error for %q", err, want)
	}
}
