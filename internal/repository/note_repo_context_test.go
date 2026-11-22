package repository

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"IGoNotes/internal/model"
)

// An index replacement and a journal/status write share the application's DB.
// The second writer must wait rather than making either operation fail BUSY.
func TestNoteRepositoryIndexReplacementSerializesOtherWrites(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewNoteRepository(db)
	commit, rollback, err, rollbackErr := repo.BeginReplaceAll([]model.NoteNode{{ID: "indexed.md", Name: "Indexed", Path: "indexed.md", Type: "file"}})
	if err != nil || rollbackErr != nil {
		t.Fatalf("prepare: %v / %v", err, rollbackErr)
	}
	defer rollback()
	done := make(chan error, 1)
	go func() { done <- repo.UpsertNode("other.md", "Other", "other.md", nil, "file") }()
	select {
	case err := <-done:
		t.Fatalf("concurrent write returned before index commit: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent write did not resume")
	}
	nodes, err := repo.GetAllNodes()
	if err != nil || len(nodes) != 2 || nodes[0].ID != "indexed.md" || nodes[1].ID != "other.md" {
		t.Fatalf("committed index and concurrent write: %+v, %v", nodes, err)
	}
}

func TestNoteRepositoryContextPreparationCancelsPoolWait(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	commit, rollback, err, rollbackErr := NewNoteRepository(db).BeginReplaceAllContext(ctx, nil)
	if !errors.Is(err, context.DeadlineExceeded) || rollbackErr != nil || commit != nil || rollback != nil {
		t.Fatalf("prepare %v / %v", err, rollbackErr)
	}
}
