package repository

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

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
