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
