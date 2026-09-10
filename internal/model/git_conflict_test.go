package model

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGitConflictJSONContract(t *testing.T) {
	content := "local text\n"
	text := GitConflictListResponse{
		Base:         "work",
		OperationID:  "operation-1",
		HeadOID:      "head-oid",
		MergeHeadOID: "merge-head-oid",
		Conflicts: []GitConflict{{
			ID:          "sha256:abc",
			Kind:        GitConflictContent,
			ContentKind: GitConflictText,
			Path:        "notes/idea.md",
			Base: &GitConflictStage{
				Path: "notes/idea.md", OID: "base-oid", Mode: "100644", Size: 5,
			},
			Local: &GitConflictStage{
				Path: "notes/idea.md", OID: "local-oid", Mode: "100644", Size: int64(len(content)), Content: &content,
			},
			Remote: &GitConflictStage{
				Path: "notes/idea.md", OID: "remote-oid", Mode: "100644", Size: 7, PreviewTruncated: true,
			},
			Actions: []GitConflictAction{GitConflictUseLocal, GitConflictUseRemote, GitConflictManual},
		}},
		CanComplete: false,
	}
	binary := GitConflictListResponse{
		Base: "work", OperationID: "operation-1", HeadOID: "head-oid", MergeHeadOID: "merge-head-oid",
		Conflicts: []GitConflict{{
			ID:           "sha256:def",
			Kind:         GitConflictAddAdd,
			ContentKind:  GitConflictBinary,
			Path:         "assets/image.png",
			OriginalPath: "assets/original.png",
			Local:        &GitConflictStage{Path: "assets/image.png", OID: "local-oid", Mode: "100644", Size: 11},
			Remote:       &GitConflictStage{Path: "assets/image.png", OID: "remote-oid", Mode: "100644", Size: 13},
			Actions:      []GitConflictAction{GitConflictUseLocal, GitConflictUseRemote, GitConflictKeepBoth},
		}},
		CanComplete: true,
	}

	for _, test := range []struct {
		name  string
		value GitConflictListResponse
		want  map[string]any
	}{
		{
			name:  "text",
			value: text,
			want: map[string]any{
				"base": "work", "operation_id": "operation-1", "head_oid": "head-oid", "merge_head_oid": "merge-head-oid", "can_complete": false,
				"conflicts": []any{map[string]any{
					"id": "sha256:abc", "kind": "content", "content_kind": "text", "path": "notes/idea.md",
					"base":    map[string]any{"path": "notes/idea.md", "oid": "base-oid", "mode": "100644", "size": float64(5), "preview_truncated": false},
					"local":   map[string]any{"path": "notes/idea.md", "oid": "local-oid", "mode": "100644", "size": float64(11), "content": "local text\n", "preview_truncated": false},
					"remote":  map[string]any{"path": "notes/idea.md", "oid": "remote-oid", "mode": "100644", "size": float64(7), "preview_truncated": true},
					"actions": []any{"local", "remote", "manual"},
				}},
			},
		},
		{
			name:  "binary add add",
			value: binary,
			want: map[string]any{
				"base": "work", "operation_id": "operation-1", "head_oid": "head-oid", "merge_head_oid": "merge-head-oid", "can_complete": true,
				"conflicts": []any{map[string]any{
					"id": "sha256:def", "kind": "add_add", "content_kind": "binary", "path": "assets/image.png", "original_path": "assets/original.png",
					"local":   map[string]any{"path": "assets/image.png", "oid": "local-oid", "mode": "100644", "size": float64(11), "preview_truncated": false},
					"remote":  map[string]any{"path": "assets/image.png", "oid": "remote-oid", "mode": "100644", "size": float64(13), "preview_truncated": false},
					"actions": []any{"local", "remote", "keep_both"},
				}},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("JSON = %s\nwant %#v\ngot  %#v", encoded, test.want, got)
			}
		})
	}
}
