package repository

import (
	"database/sql"
	"path/filepath"
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

	invalidNode := model.NoteNode{
		ID:   "invalid.md",
		Name: "invalid.md",
		Path: "invalid.md",
		Type: "",
	}
	if err := repo.ReplaceAll([]model.NoteNode{invalidNode}); err == nil {
		t.Fatal("ReplaceAll() error = nil, want invalid node type error")
	}

	nodes, err = repo.GetAllNodes()
	if err != nil {
		t.Fatalf("GetAllNodes() after failed replacement error = %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "new.md" {
		t.Fatalf("GetAllNodes() after failed replacement = %#v, want only new.md", nodes)
	}
}
