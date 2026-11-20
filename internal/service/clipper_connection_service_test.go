package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

func requireClipperConnectionCode(t *testing.T, err error, code model.ClipperErrorCode) {
	t.Helper()
	var coded *model.ClipperError
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("error = %v; want %s", err, code)
	}
}

func TestClipperConnectionSecretOncePersistenceRevocationRotation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := NewConfigService(configPath).Save(&model.Config{}); err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "metadata.db")
	db, err := repository.InitDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repo := repository.NewClipperConnectionRepository(db)
	svc := NewClipperConnectionService(repo)
	empty, err := svc.List(ctx)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty List = %#v, %v", empty, err)
	}
	created, err := svc.Create(ctx, "Chromium — рабочий")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(created.Secret, ".")
	if len(parts) != 2 || parts[0] != created.Connection.ID || len(parts[0]) != 32 || strings.Trim(parts[0], "0123456789abcdef") != "" {
		t.Fatal("invalid opaque token structure")
	}
	random, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(random) != 32 {
		t.Fatal("secret must carry 256 random bits")
	}
	if created.Connection.Label != "Chromium — рабочий" || created.Connection.CreatedAt.IsZero() || created.Connection.RevokedAt != nil {
		t.Fatal("invalid creation metadata")
	}
	credential, found, err := repo.CredentialByID(ctx, created.Connection.ID)
	sum := sha256.Sum256([]byte(created.Secret))
	digest := hex.EncodeToString(sum[:])
	if err != nil || !found || credential.Digest != digest {
		t.Fatal("repository must store SHA-256 of the entire opaque token")
	}
	if err := svc.Authenticate(ctx, created.Secret); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx)
	if err != nil || len(list) != 1 || list[0] != created.Connection {
		t.Fatalf("List = %#v, %v", list, err)
	}
	encoded, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{created.Secret, parts[1], digest, "secret", "digest"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatal("metadata leaked credential material")
		}
	}
	wire, err := json.Marshal(created)
	if err != nil || !bytes.Contains(wire, []byte(`"secret":"`+created.Secret+`"`)) {
		t.Fatal("creation response must return the one-time secret")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil || bytes.Contains(stored, []byte(created.Secret)) || bytes.Contains(stored, []byte(parts[1])) {
		t.Fatal("database contains raw secret or cannot be read")
	}
	reopen := func() {
		t.Helper()
		db, err = repository.InitDB(path)
		if err != nil {
			t.Fatal(err)
		}
		svc = NewClipperConnectionService(repository.NewClipperConnectionRepository(db))
	}
	reopen()
	if err := svc.Authenticate(ctx, created.Secret); err != nil {
		t.Fatal("secret failed after reopening:", err)
	}
	if err := svc.Revoke(ctx, created.Connection.ID); err != nil {
		t.Fatal(err)
	}
	list, err = svc.List(ctx)
	if err != nil || len(list) != 1 || list[0].RevokedAt == nil {
		t.Fatal("revocation metadata missing")
	}
	firstRevoked := *list[0].RevokedAt
	if err := svc.Revoke(ctx, created.Connection.ID); err != nil {
		t.Fatal(err)
	}
	list, err = svc.List(ctx)
	if err != nil || !list[0].RevokedAt.Equal(firstRevoked) {
		t.Fatal("repeated revocation must retain first timestamp")
	}
	if err := svc.Revoke(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing Revoke = %v", err)
	}
	requireClipperConnectionCode(t, svc.Authenticate(ctx, created.Secret), model.ClipperUnauthorized)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopen()
	requireClipperConnectionCode(t, svc.Authenticate(ctx, created.Secret), model.ClipperUnauthorized)
	rotated, err := svc.Create(ctx, created.Connection.Label)
	if err != nil || rotated.Secret == created.Secret || rotated.Connection.ID == created.Connection.ID {
		t.Fatal("new connection must rotate both ID and secret")
	}
	if err := svc.Authenticate(ctx, rotated.Secret); err != nil {
		t.Fatal(err)
	}
	requireClipperConnectionCode(t, svc.Authenticate(ctx, created.Secret), model.ClipperUnauthorized)
	configAfter, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(configBefore, configAfter) {
		t.Fatal("connection lifecycle changed Config")
	}
}

type clipperConnectionStoreFake struct {
	credential repository.ClipperConnectionCredential
	found      bool
	err        error
	creates    int
}

func (f *clipperConnectionStoreFake) Create(context.Context, model.ClipperConnection, string) error {
	f.creates++
	return f.err
}

func (f *clipperConnectionStoreFake) List(context.Context) ([]model.ClipperConnection, error) {
	return nil, f.err
}

func (f *clipperConnectionStoreFake) CredentialByID(context.Context, string) (repository.ClipperConnectionCredential, bool, error) {
	return f.credential, f.found, f.err
}

func (f *clipperConnectionStoreFake) Revoke(context.Context, string, time.Time) error {
	return f.err
}

func TestClipperConnectionValidation(t *testing.T) {
	ctx := context.Background()
	store := &clipperConnectionStoreFake{}
	svc := NewClipperConnectionService(store)
	for _, label := range []string{"", "  \t", "line\nbreak", "nul\x00", "del\x7f", string([]byte{0xff}), strings.Repeat("я", 129)} {
		response, err := svc.Create(ctx, label)
		requireClipperConnectionCode(t, err, model.ClipperInvalidRequest)
		if response != (model.ClipperConnectionCreatedResponse{}) || store.creates != 0 {
			t.Fatal("invalid label created a credential")
		}
	}
	if _, err := svc.Create(ctx, strings.Repeat("я", 128)); err != nil {
		t.Fatal("128 Unicode characters should be accepted:", err)
	}
	list, err := svc.List(ctx)
	if err != nil || list == nil || len(list) != 0 {
		t.Fatal("nil repository list should normalize to []")
	}
}

func TestClipperConnectionAuthenticationRejectsUniformly(t *testing.T) {
	ctx := context.Background()
	id := strings.Repeat("a", 32)
	token := id + "." + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	sum := sha256.Sum256([]byte(token))
	store := &clipperConnectionStoreFake{found: true, credential: repository.ClipperConnectionCredential{
		Connection: model.ClipperConnection{ID: id}, Digest: hex.EncodeToString(sum[:]),
	}}
	svc := NewClipperConnectionService(store)
	for _, invalid := range []string{"", "Bearer " + token, id, "." + token, token + ".extra", token + "=", "../" + token, id + ".bad", strings.Repeat("b", 32) + token[32:], token[:len(token)-2] + "AA"} {
		// Unknown ID is represented by the repository's found flag, not a scan.
		store.found = !strings.HasPrefix(invalid, strings.Repeat("b", 32))
		err := svc.Authenticate(ctx, invalid)
		requireClipperConnectionCode(t, err, model.ClipperUnauthorized)
		if err.Error() != string(model.ClipperUnauthorized) {
			t.Fatal("authentication rejection exposes details")
		}
	}
	store.found = true
	for _, digest := range []string{"", "bad", strings.Repeat("z", 64), strings.Repeat("0", 64)} {
		store.credential.Digest = digest
		requireClipperConnectionCode(t, svc.Authenticate(ctx, token), model.ClipperUnauthorized)
	}
	store.credential.Digest = hex.EncodeToString(sum[:])
	revoked := time.Now()
	store.credential.Connection.RevokedAt = &revoked
	requireClipperConnectionCode(t, svc.Authenticate(ctx, token), model.ClipperUnauthorized)
}

type clipperConnectionFailedRandom struct{}

func (clipperConnectionFailedRandom) Read([]byte) (int, error) {
	return 0, errors.New("private random failure")
}

func TestClipperConnectionFailuresNeverReturnCredentials(t *testing.T) {
	ctx := context.Background()
	store := &clipperConnectionStoreFake{}
	svc := NewClipperConnectionService(store)
	for _, prefix := range []int{0, 16, 47} {
		svc.random = bytes.NewReader(make([]byte, prefix))
		response, err := svc.Create(ctx, "Browser")
		if err == nil || response != (model.ClipperConnectionCreatedResponse{}) || store.creates != 0 {
			t.Fatal("partial entropy must fail before persistence")
		}
	}
	svc.random = clipperConnectionFailedRandom{}
	response, err := svc.Create(ctx, "Browser")
	if err == nil || response != (model.ClipperConnectionCreatedResponse{}) || strings.Contains(err.Error(), "private") {
		t.Fatal("random error leaked details or returned credentials")
	}
	svc = NewClipperConnectionService(store)
	created, err := svc.Create(ctx, "Browser")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(created.Secret))
	digest := hex.EncodeToString(sum[:])
	store.credential = repository.ClipperConnectionCredential{Connection: created.Connection, Digest: digest}
	store.found = true
	store.err = fmt.Errorf("private %s %s: %w", created.Secret, digest, os.ErrPermission)
	response, createErr := svc.Create(ctx, "Browser")
	if response != (model.ClipperConnectionCreatedResponse{}) {
		t.Fatal("failed persistence returned a secret")
	}
	list, listErr := svc.List(ctx)
	if list != nil {
		t.Fatal("failed listing returned metadata")
	}
	for _, err := range []error{createErr, listErr, svc.Revoke(ctx, created.Connection.ID), svc.Authenticate(ctx, created.Secret)} {
		if err == nil || strings.Contains(err.Error(), created.Secret) || strings.Contains(err.Error(), digest) || strings.Contains(err.Error(), "private") {
			t.Fatal("repository failure succeeded or leaked private details")
		}
	}
	store.found = false
	if err := svc.Authenticate(ctx, created.Secret); err == nil {
		t.Fatal("lookup permission failure treated as valid missing credential")
	}
}
