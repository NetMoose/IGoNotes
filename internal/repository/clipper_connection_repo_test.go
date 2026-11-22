package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"IGoNotes/internal/model"
)

func clipperConnectionFixture() (model.ClipperConnection, string) {
	sum := sha256.Sum256([]byte("test-only-256-bit-secret-material-never-persisted"))
	return model.ClipperConnection{
		ID: "connection-1", Label: "Chromium — рабочий",
		CreatedAt: time.Date(2026, 10, 21, 12, 30, 45, 123456789, time.FixedZone("test", 3*60*60)),
	}, hex.EncodeToString(sum[:])
}

func TestClipperConnectionRepositoryPersistenceAndPrivacy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metadata.db")
	db, err := InitDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repo := NewClipperConnectionRepository(db)
	list, err := repo.List(ctx)
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty List = %#v, %v", list, err)
	}
	connection, digest := clipperConnectionFixture()
	if err := repo.Create(ctx, connection, digest); err != nil {
		t.Fatal(err)
	}
	connection.CreatedAt = connection.CreatedAt.UTC()
	check := func(revoked bool) {
		t.Helper()
		metadata, err := repo.List(ctx)
		if err != nil || !reflect.DeepEqual(metadata, []model.ClipperConnection{connection}) {
			t.Fatalf("List = %#v, %v; want %#v", metadata, err, connection)
		}
		credential, found, err := repo.CredentialByID(ctx, connection.ID)
		if err != nil || !found || credential.Digest != digest || !reflect.DeepEqual(credential.Connection, connection) {
			t.Fatalf("credential roundtrip failed: found=%v err=%v", found, err)
		}
		if (credential.Connection.RevokedAt != nil) != revoked {
			t.Fatal("incorrect revocation state")
		}
		for _, value := range []any{metadata, credential} {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), digest) || strings.Contains(string(encoded), "test-only-256-bit-secret-material-never-persisted") || strings.Contains(string(encoded), "digest") {
				t.Fatal("JSON leaked credentials")
			}
		}
	}
	check(false)
	var id, label, storedDigest, created string
	var revoked sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT * FROM clipper_connections").Scan(&id, &label, &storedDigest, &created, &revoked); err != nil {
		t.Fatal(err)
	}
	if id != connection.ID || label != connection.Label || storedDigest != digest || created != connection.CreatedAt.Format(time.RFC3339Nano) || revoked.Valid {
		t.Fatal("unexpected stored row")
	}
	rowJSON, err := json.Marshal([]any{id, label, storedDigest, created, revoked})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rowJSON, []byte("test-only-256-bit-secret-material-never-persisted")) {
		t.Fatal("row contains plaintext secret")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("test-only-256-bit-secret-material-never-persisted")) {
		t.Fatal("database contains plaintext secret")
	}
	db, err = InitDB(path)
	if err != nil {
		t.Fatal(err)
	}
	repo = NewClipperConnectionRepository(db)
	check(false)
	revokedAt := connection.CreatedAt.Add(time.Hour)
	if err := repo.Revoke(ctx, connection.ID, revokedAt); err != nil {
		t.Fatal(err)
	}
	connection.RevokedAt = &revokedAt
	if err := repo.Revoke(ctx, connection.ID, revokedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = InitDB(path)
	if err != nil {
		t.Fatal(err)
	}
	repo = NewClipperConnectionRepository(db)
	check(true)
}

func TestClipperConnectionRepositoryDuplicatesAndMissing(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewClipperConnectionRepository(db)
	ctx := context.Background()
	connection, digest := clipperConnectionFixture()
	if err := repo.Create(ctx, connection, digest); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, connection, strings.Repeat("b", 64)); err == nil {
		t.Fatal("duplicate ID accepted")
	}
	other := connection
	other.ID = "connection-2"
	if err := repo.Create(ctx, other, digest); err == nil {
		t.Fatal("duplicate digest accepted")
	}
	got, found, err := repo.CredentialByID(ctx, "missing")
	if err != nil || found || !reflect.DeepEqual(got, ClipperConnectionCredential{}) {
		t.Fatalf("missing lookup: found=%v err=%v", found, err)
	}
	if err := repo.Revoke(ctx, "missing", time.Now()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing revoke = %v", err)
	}
	list, err := repo.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("failed insert changed list: len=%d err=%v", len(list), err)
	}
}

func TestClipperConnectionRepositoryValidationAndContext(t *testing.T) {
	db, err := InitDB(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewClipperConnectionRepository(db)
	connection, digest := clipperConnectionFixture()
	for _, tc := range []struct {
		name   string
		change func(*model.ClipperConnection, *string)
	}{
		{"empty ID", func(c *model.ClipperConnection, _ *string) { c.ID = "" }},
		{"unsafe ID", func(c *model.ClipperConnection, _ *string) { c.ID = "bad\nID" }},
		{"blank label", func(c *model.ClipperConnection, _ *string) { c.Label = "  " }},
		{"control label", func(c *model.ClipperConnection, _ *string) { c.Label = "bad\x00label" }},
		{"invalid UTF8", func(c *model.ClipperConnection, _ *string) { c.Label = "\xff" }},
		{"zero time", func(c *model.ClipperConnection, _ *string) { c.CreatedAt = time.Time{} }},
		{"unrepresentable time", func(c *model.ClipperConnection, _ *string) {
			c.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		}},
		{"already revoked", func(c *model.ClipperConnection, _ *string) { c.RevokedAt = &c.CreatedAt }},
		{"raw secret", func(_ *model.ClipperConnection, d *string) { *d = "raw-secret" }},
		{"nonhex digest", func(_ *model.ClipperConnection, d *string) { *d = strings.Repeat("g", 64) }},
		{"uppercase digest", func(_ *model.ClipperConnection, d *string) { *d = strings.Repeat("A", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, d := connection, digest
			tc.change(&c, &d)
			err := repo.Create(context.Background(), c, d)
			if err == nil {
				t.Fatal("invalid input accepted")
			}
			if strings.Contains(err.Error(), d) {
				t.Fatal("error leaked digest/input credential")
			}
		})
	}
	if err := repo.Create(context.Background(), connection, digest); err != nil {
		t.Fatal(err)
	}
	if err := repo.Revoke(context.Background(), connection.ID, time.Time{}); err == nil {
		t.Fatal("zero revocation time accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := repo.Create(ctx, connection, digest); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create cancellation = %v", err)
	}
	if _, err := repo.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("List cancellation = %v", err)
	}
	if _, _, err := repo.CredentialByID(ctx, connection.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("lookup cancellation = %v", err)
	}
	if err := repo.Revoke(ctx, connection.ID, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Revoke cancellation = %v", err)
	}
	got, found, err := repo.CredentialByID(context.Background(), connection.ID)
	if err != nil || !found || got.Connection.RevokedAt != nil {
		t.Fatal("failed revoke changed state")
	}
}

func TestClipperConnectionRepositoryContentionWaitAndCancellation(t *testing.T) {
	for _, operation := range []string{"credential", "list", "create", "revoke"} {
		for _, canceled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel=%v", operation, canceled), func(t *testing.T) {
				db, err := InitDB(filepath.Join(t.TempDir(), "metadata.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				repo := NewClipperConnectionRepository(db)
				connection, digest := clipperConnectionFixture()
				if err := repo.Create(t.Context(), connection, digest); err != nil {
					t.Fatal(err)
				}
				// Exercise SQLite lock retries, separately from the production pool wait.
				db.SetMaxOpenConns(2)
				locker, err := db.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer locker.Close()
				if _, err := locker.ExecContext(t.Context(), "BEGIN EXCLUSIVE"); err != nil {
					t.Fatal(err)
				}
				defer locker.ExecContext(context.Background(), "ROLLBACK")
				revoked := connection.CreatedAt.Add(time.Hour).UTC()
				// A lookup that waited for the writer must observe committed revocation,
				// not credentials cached before contention or an authorization bypass.
				if operation == "credential" {
					if _, err := locker.ExecContext(t.Context(), "UPDATE clipper_connections SET revoked_at=? WHERE id=?", revoked.Format(time.RFC3339Nano), connection.ID); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				result := make(chan error, 1)
				go func() {
					var err error
					switch operation {
					case "credential":
						var got ClipperConnectionCredential
						var found bool
						got, found, err = repo.CredentialByID(ctx, connection.ID)
						if err == nil && (!found || got.Connection.RevokedAt == nil || !got.Connection.RevokedAt.Equal(revoked)) {
							err = errors.New("lookup did not observe committed revocation")
						}
					case "list":
						var got []model.ClipperConnection
						got, err = repo.List(ctx)
						if err == nil && len(got) != 1 {
							err = errors.New("list changed during contention")
						}
					case "create":
						other := connection
						other.ID = "after-lock"
						err = repo.Create(ctx, other, strings.Repeat("b", 64))
					case "revoke":
						err = repo.Revoke(ctx, connection.ID, revoked)
					}
					result <- err
				}()
				select {
				case err := <-result:
					t.Fatalf("operation returned before lock release/cancel: %v", err)
				case <-time.After(50 * time.Millisecond):
				}
				if canceled {
					cancel()
				} else if _, err := locker.ExecContext(t.Context(), "COMMIT"); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-result:
					if canceled && !errors.Is(err, context.Canceled) {
						t.Fatalf("cancellation: %v", err)
					}
					if !canceled && err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("operation did not finish")
				}
				if !canceled && operation == "revoke" {
					if err := repo.Revoke(t.Context(), connection.ID, revoked.Add(time.Hour)); err != nil {
						t.Fatal(err)
					}
					got, found, err := repo.CredentialByID(t.Context(), connection.ID)
					if err != nil || !found || got.Connection.RevokedAt == nil || !got.Connection.RevokedAt.Equal(revoked) {
						t.Fatalf("first revocation changed: %+v %v", got.Connection, err)
					}
				}
			})
		}
	}
}
