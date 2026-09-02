package repository

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"IGoNotes/internal/model"
)

func TestGitStatusRepositoryRoundTripCompleteStatus(t *testing.T) {
	repo, db := openTestGitStatusRepository(t)
	defer db.Close()
	updatedAt := time.Date(2026, time.September, 2, 12, 13, 14, 987654321, time.FixedZone("test", 3*60*60))
	repo.now = func() time.Time { return updatedAt }
	lastAttempt := time.Date(2026, time.September, 1, 8, 7, 6, 123456789, time.FixedZone("attempt", -4*60*60))
	lastSuccess := time.Date(2026, time.August, 31, 5, 4, 3, 765432100, time.FixedZone("success", 2*60*60))
	want := model.GitStatus{
		Base:                "work-notes",
		RepositoryPath:      "/notes/work",
		State:               model.GitStateError,
		OperationID:         "sync-42",
		Stage:               "push",
		Ahead:               3,
		Behind:              2,
		ConsecutiveFailures: 4,
		LastAttempt:         &lastAttempt,
		LastSuccess:         &lastSuccess,
		ChangedPaths:        []string{"README.md", "daily/2026-09-02.md"},
		RemoteOID:           "0123456789abcdef",
		Error: &model.APIError{
			Code:    "git_push_failed",
			Message: "push rejected",
			Field:   "git_url",
		},
	}

	if err := repo.Upsert(context.Background(), want); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	got, found, err := repo.Get(context.Background(), want.RepositoryPath)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !found {
		t.Fatal("Get() found = false, want true")
	}
	want.LastAttempt = millisecondUTC(lastAttempt)
	want.LastSuccess = millisecondUTC(lastSuccess)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Get() status mismatch:\n got: %#v\nwant: %#v", got, want)
	}

	var gotUpdatedAt int64
	if err := db.QueryRow("SELECT updated_at_unix_ms FROM git_status WHERE repository_path = ?", want.RepositoryPath).Scan(&gotUpdatedAt); err != nil {
		t.Fatalf("query updated time: %v", err)
	}
	if gotUpdatedAt != updatedAt.UnixMilli() {
		t.Errorf("updated_at_unix_ms = %d, want %d", gotUpdatedAt, updatedAt.UnixMilli())
	}
}

func TestGitStatusRepositoryRoundTripNilPathsAndError(t *testing.T) {
	repo, db := openTestGitStatusRepository(t)
	defer db.Close()
	want := model.GitStatus{
		Base:           "personal",
		RepositoryPath: "/notes/personal",
		State:          model.GitStateReady,
	}

	if err := repo.Upsert(context.Background(), want); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	got, found, err := repo.Get(context.Background(), want.RepositoryPath)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !found {
		t.Fatal("Get() found = false, want true")
	}
	if got.ChangedPaths == nil || len(got.ChangedPaths) != 0 {
		t.Errorf("Get().ChangedPaths = %#v, want non-nil empty slice", got.ChangedPaths)
	}
	if got.Error != nil {
		t.Errorf("Get().Error = %#v, want nil", got.Error)
	}

	var changedPaths, errorCode, errorMessage, errorField string
	if err := db.QueryRow(`
		SELECT changed_paths_json, error_code, error_message, error_field
		FROM git_status WHERE repository_path = ?
	`, want.RepositoryPath).Scan(&changedPaths, &errorCode, &errorMessage, &errorField); err != nil {
		t.Fatalf("query nil encodings: %v", err)
	}
	if changedPaths != "[]" {
		t.Errorf("changed_paths_json = %q, want []", changedPaths)
	}
	if errorCode != "" || errorMessage != "" || errorField != "" {
		t.Errorf("error triplet = %q/%q/%q, want empty", errorCode, errorMessage, errorField)
	}
}

func TestGitStatusRepositoryUpsertReplacesExistingStatus(t *testing.T) {
	repo, db := openTestGitStatusRepository(t)
	defer db.Close()
	ctx := context.Background()
	path := "/notes/shared"
	if err := repo.Upsert(ctx, model.GitStatus{Base: "old", RepositoryPath: path, State: model.GitStateSyncing, ChangedPaths: []string{"old.md"}}); err != nil {
		t.Fatalf("initial Upsert() error = %v", err)
	}
	want := model.GitStatus{Base: "new", RepositoryPath: path, State: model.GitStatePaused, Behind: 7, ChangedPaths: []string{"new.md"}}
	if err := repo.Upsert(ctx, want); err != nil {
		t.Fatalf("replacement Upsert() error = %v", err)
	}

	got, found, err := repo.Get(ctx, path)
	if err != nil || !found {
		t.Fatalf("Get() = %#v, %v, %v; want replacement", got, found, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("replacement = %#v, want %#v", got, want)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM git_status WHERE repository_path = ?", path).Scan(&count); err != nil {
		t.Fatalf("count replacement rows: %v", err)
	}
	if count != 1 {
		t.Errorf("replacement row count = %d, want 1", count)
	}
}

func TestGitStatusRepositoryListIsDeterministicallySorted(t *testing.T) {
	repo, db := openTestGitStatusRepository(t)
	defer db.Close()
	ctx := context.Background()
	statuses := []model.GitStatus{
		{Base: "zeta", RepositoryPath: "/repo/z", State: model.GitStateReady},
		{Base: "alpha", RepositoryPath: "/repo/b", State: model.GitStatePaused},
		{Base: "alpha", RepositoryPath: "/repo/a", State: model.GitStateSyncing},
	}
	for _, status := range statuses {
		if err := repo.Upsert(ctx, status); err != nil {
			t.Fatalf("Upsert(%q) error = %v", status.RepositoryPath, err)
		}
	}

	got, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	gotOrder := make([]string, len(got))
	for index, status := range got {
		gotOrder[index] = status.Base + ":" + status.RepositoryPath
	}
	wantOrder := []string{"alpha:/repo/a", "alpha:/repo/b", "zeta:/repo/z"}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("List() order = %v, want %v", gotOrder, wantOrder)
	}
}

func TestGitStatusRepositoryDeleteIsIdempotent(t *testing.T) {
	repo, db := openTestGitStatusRepository(t)
	defer db.Close()
	ctx := context.Background()
	path := "/notes/delete"
	if err := repo.Upsert(ctx, model.GitStatus{Base: "delete", RepositoryPath: path, State: model.GitStateReady}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	if err := repo.Delete(ctx, path); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if err := repo.Delete(ctx, path); err != nil {
		t.Fatalf("second Delete() error = %v", err)
	}
	_, found, err := repo.Get(ctx, path)
	if err != nil {
		t.Fatalf("Get() after delete error = %v", err)
	}
	if found {
		t.Fatal("Get() after delete found = true, want false")
	}
}

func TestGitStatusRepositoryRejectsMalformedChangedPathsJSON(t *testing.T) {
	repo, db := openTestGitStatusRepository(t)
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO git_status (repository_path, base_name, state, changed_paths_json, updated_at_unix_ms)
		VALUES (?, ?, ?, ?, ?)
	`, "/notes/broken", "broken", model.GitStateError, "not-json", time.Now().UnixMilli()); err != nil {
		t.Fatalf("insert malformed row: %v", err)
	}

	if _, found, err := repo.Get(context.Background(), "/notes/broken"); err == nil || found {
		t.Errorf("Get() = found %v, error %v; want decode error", found, err)
	}
	if _, err := repo.List(context.Background()); err == nil {
		t.Fatal("List() error = nil, want decode error")
	}
}

func TestGitStatusRepositoryReturnsClosedDatabaseErrors(t *testing.T) {
	repo, db := openTestGitStatusRepository(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	ctx := context.Background()
	status := model.GitStatus{Base: "closed", RepositoryPath: "/notes/closed", State: model.GitStateReady}

	if err := repo.Upsert(ctx, status); err == nil {
		t.Error("Upsert() error = nil, want closed database error")
	}
	if _, found, err := repo.Get(ctx, status.RepositoryPath); err == nil || found {
		t.Errorf("Get() = found %v, error %v; want closed database error", found, err)
	}
	if _, err := repo.List(ctx); err == nil {
		t.Error("List() error = nil, want closed database error")
	}
	if err := repo.Delete(ctx, status.RepositoryPath); err == nil {
		t.Error("Delete() error = nil, want closed database error")
	}
}

func openTestGitStatusRepository(t *testing.T) (*GitStatusRepository, *sql.DB) {
	t.Helper()
	db, err := InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	return NewGitStatusRepository(db), db
}

func millisecondUTC(value time.Time) *time.Time {
	normalized := time.UnixMilli(value.UnixMilli()).UTC()
	return &normalized
}
