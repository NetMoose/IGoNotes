package repository

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"IGoNotes/internal/model"
)

func openTestNoteRepository(t *testing.T) (*NoteRepository, *sql.DB) {
	t.Helper()
	db, err := InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	return NewNoteRepository(db), db
}

func TestNoteRepositoryDeleteNodeRemovesOnlyExactNodeAndDescendants(t *testing.T) {
	tests := []struct {
		name      string
		root      string
		rootType  string
		nodes     []string
		remaining []string
	}{
		{
			name:      "ordinary directory",
			root:      "folder",
			rootType:  "dir",
			nodes:     []string{"folder", "folder/note.md", "folder/nested", "folder/nested/note.md", "folderX", "folderX/note.md", "other.md"},
			remaining: []string{"folderX", "folderX/note.md", "other.md"},
		},
		{
			name:      "ordinary file",
			root:      "note.md",
			nodes:     []string{"note.md", "note.md.bak", "notes.md"},
			remaining: []string{"note.md.bak", "notes.md"},
		},
		{
			name:      "percent in directory ID",
			root:      "folder%",
			rootType:  "dir",
			nodes:     []string{"folder%", "folder%/note.md", "folder%/nested/note.md", "folderX", "folderX/note.md", "folderXYZ/nested.md", "other.md"},
			remaining: []string{"folderX", "folderX/note.md", "folderXYZ/nested.md", "other.md"},
		},
		{
			name:      "underscore in directory ID",
			root:      "a_b",
			rootType:  "dir",
			nodes:     []string{"a_b", "a_b/note.md", "a_b/nested/note.md", "acb", "acb/note.md", "axb/nested.md", "other.md"},
			remaining: []string{"acb", "acb/note.md", "axb/nested.md", "other.md"},
		},
		{
			name:      "backslash and quote in directory ID",
			root:      `dir\with'quote`,
			rootType:  "dir",
			nodes:     []string{`dir\with'quote`, `dir\with'quote/note.md`, `dir\with'quote/nested/note.md`, `dirXwith'quote`, `dirXwith'quote/note.md`, "other.md"},
			remaining: []string{`dirXwith'quote`, `dirXwith'quote/note.md`, "other.md"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, db := openTestNoteRepository(t)
			defer db.Close()
			for _, id := range test.nodes {
				nodeType := "file"
				if id == test.root && test.rootType != "" {
					nodeType = test.rootType
				}
				if err := repo.UpsertNode(id, id, id, nil, nodeType); err != nil {
					t.Fatalf("UpsertNode(%q) error = %v", id, err)
				}
			}

			if err := repo.DeleteNode(test.root); err != nil {
				t.Fatalf("DeleteNode(%q) error = %v", test.root, err)
			}

			rows := repositoryRows(t, db, "SELECT id FROM notes ORDER BY id", 1)
			got := make([]string, len(rows))
			for index := range rows {
				got[index] = rows[index][0]
			}
			if !reflect.DeepEqual(got, test.remaining) {
				t.Errorf("remaining IDs = %#v, want %#v", got, test.remaining)
			}
		})
	}
}

func TestNoteRepositoryDeleteNodeReturnsQueryErrorUnchanged(t *testing.T) {
	repo, db := openTestNoteRepository(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	_, wantErr := db.Exec("DELETE FROM notes")

	if gotErr := repo.DeleteNode("note.md"); gotErr != wantErr {
		t.Fatalf("DeleteNode() error = %v, want unchanged %v", gotErr, wantErr)
	}
}

func TestNoteRepositoryBeginReplaceAllPreservesExactStateUntilCommit(t *testing.T) {
	repo, db := openTestNoteRepository(t)
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO notes (id, title, path, parent_id, type, created_at, updated_at)
		VALUES ('old.md', 'old title', 'old.md', NULL, 'file', '2001-02-03 04:05:06', '2007-08-09 10:11:12');
		INSERT INTO tags (note_id, tag) VALUES ('old.md', 'keep-tag');
	`); err != nil {
		t.Fatalf("seed metadata error = %v", err)
	}
	wantNotes := repositoryRows(t, db, "SELECT id, title, path, COALESCE(parent_id, ''), type, quote(created_at), quote(updated_at) FROM notes ORDER BY id", 7)
	wantTags := repositoryRows(t, db, "SELECT note_id, tag FROM tags ORDER BY note_id, tag", 2)
	candidate := []model.NoteNode{{ID: "new.md", Name: "new", Path: "new.md", Type: "file"}}

	commit, rollback, operationErr, rollbackErr := repo.BeginReplaceAll(candidate)
	if operationErr != nil || rollbackErr != nil {
		t.Fatalf("BeginReplaceAll() errors = %v, %v", operationErr, rollbackErr)
	}
	if err := rollback(); err != nil {
		t.Fatalf("rollback() error = %v", err)
	}
	if err := rollback(); err != nil {
		t.Fatalf("second rollback() error = %v, want idempotent nil", err)
	}
	if err := commit(); !errors.Is(err, sql.ErrTxDone) {
		t.Errorf("commit() after rollback error = %v, want sql.ErrTxDone", err)
	}
	if got := repositoryRows(t, db, "SELECT id, title, path, COALESCE(parent_id, ''), type, quote(created_at), quote(updated_at) FROM notes ORDER BY id", 7); !reflect.DeepEqual(got, wantNotes) {
		t.Errorf("notes after rollback = %#v, want %#v", got, wantNotes)
	}
	if got := repositoryRows(t, db, "SELECT note_id, tag FROM tags ORDER BY note_id, tag", 2); !reflect.DeepEqual(got, wantTags) {
		t.Errorf("tags after rollback = %#v, want %#v", got, wantTags)
	}

	commit, rollback, operationErr, rollbackErr = repo.BeginReplaceAll(candidate)
	if operationErr != nil || rollbackErr != nil {
		t.Fatalf("second BeginReplaceAll() errors = %v, %v", operationErr, rollbackErr)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit() error = %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("second commit() error = %v, want idempotent nil", err)
	}
	if got := repositoryRows(t, db, "SELECT id, title, path, COALESCE(parent_id, ''), type FROM notes ORDER BY id", 5); !reflect.DeepEqual(got, [][]string{{"new.md", "new", "new.md", "", "file"}}) {
		t.Errorf("notes after commit = %#v, want candidate", got)
	}
	if err := rollback(); !errors.Is(err, sql.ErrTxDone) {
		t.Errorf("rollback() after commit error = %v, want sql.ErrTxDone", err)
	}
}

func TestNoteRepositoryBeginReplaceAllPrepareFailurePreservesState(t *testing.T) {
	repo, db := openTestNoteRepository(t)
	defer db.Close()
	if err := repo.UpsertNode("old.md", "old", "old.md", nil, "file"); err != nil {
		t.Fatalf("UpsertNode() error = %v", err)
	}
	duplicate := []model.NoteNode{
		{ID: "duplicate", Name: "first", Path: "first", Type: "file"},
		{ID: "duplicate", Name: "second", Path: "second", Type: "file"},
	}

	commit, rollback, operationErr, rollbackErr := repo.BeginReplaceAll(duplicate)
	if operationErr == nil {
		t.Fatal("BeginReplaceAll() operation error = nil, want duplicate ID error")
	}
	if rollbackErr != nil {
		t.Fatalf("BeginReplaceAll() rollback error = %v, want nil", rollbackErr)
	}
	if commit != nil || rollback != nil {
		t.Errorf("finalizers present = %t / %t, want false / false after prepare failure", commit != nil, rollback != nil)
	}
	nodes, getErr := repo.GetAllNodes()
	if getErr != nil {
		t.Fatalf("GetAllNodes() error = %v", getErr)
	}
	if len(nodes) != 1 || nodes[0].ID != "old.md" {
		t.Errorf("nodes after prepare failure = %#v, want old.md", nodes)
	}
}

func TestNoteRepositoryBeginReplaceAllBeginFailureHasNoRollbackError(t *testing.T) {
	repo, db := openTestNoteRepository(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	commit, rollback, operationErr, rollbackErr := repo.BeginReplaceAll(nil)
	if operationErr == nil {
		t.Fatal("BeginReplaceAll() operation error = nil, want closed database error")
	}
	if rollbackErr != nil {
		t.Errorf("BeginReplaceAll() rollback error = %v, want nil when Begin failed", rollbackErr)
	}
	if commit != nil || rollback != nil {
		t.Errorf("finalizers present = %t / %t, want false / false", commit != nil, rollback != nil)
	}
}

func repositoryRows(t *testing.T, db *sql.DB, query string, columns int) [][]string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	defer rows.Close()
	var result [][]string
	for rows.Next() {
		values := make([]string, columns)
		destinations := make([]any, columns)
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatalf("Scan() error = %v", err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Rows error = %v", err)
	}
	return result
}

func TestNoteRepositoryReplaceAllIsTransactional(t *testing.T) {
	repo, db := openTestNoteRepository(t)
	defer db.Close()

	if err := repo.UpsertNode("old.md", "old.md", "old.md", nil, "file"); err != nil {
		t.Fatalf("UpsertNode() error = %v", err)
	}

	newNode := model.NoteNode{
		ID:   "new.md",
		Name: "new.md",
		Path: "new.md",
		Type: "file",
	}
	if err := repo.ReplaceAll([]model.NoteNode{newNode}); err != nil {
		t.Fatalf("ReplaceAll() error = %v", err)
	}

	nodes, err := repo.GetAllNodes()
	if err != nil {
		t.Fatalf("GetAllNodes() error = %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "new.md" {
		t.Fatalf("GetAllNodes() = %#v, want only new.md", nodes)
	}

	duplicateNodes := []model.NoteNode{
		{
			ID:   "duplicate",
			Name: "duplicate.md",
			Path: "duplicate.md",
			Type: "file",
		},
		{
			ID:   "duplicate",
			Name: "duplicate",
			Path: "duplicate",
			Type: "dir",
		},
	}
	if err := repo.ReplaceAll(duplicateNodes); err == nil {
		t.Fatal("ReplaceAll() error = nil, want duplicate ID error")
	}

	nodes, err = repo.GetAllNodes()
	if err != nil {
		t.Fatalf("GetAllNodes() after failed replacement error = %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "new.md" {
		t.Fatalf("GetAllNodes() after failed replacement = %#v, want only new.md", nodes)
	}
}
