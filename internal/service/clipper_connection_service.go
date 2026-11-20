package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

// ClipperConnectionStore keeps credential storage independent of app Config.
type ClipperConnectionStore interface {
	Create(context.Context, model.ClipperConnection, string) error
	List(context.Context) ([]model.ClipperConnection, error)
	CredentialByID(context.Context, string) (repository.ClipperConnectionCredential, bool, error)
	Revoke(context.Context, string, time.Time) error
}

type ClipperConnectionService struct {
	store  ClipperConnectionStore
	random io.Reader
}

func NewClipperConnectionService(store ClipperConnectionStore) *ClipperConnectionService {
	return &ClipperConnectionService{store: store, random: rand.Reader}
}

// Create returns the opaque secret only after its digest has been persisted.
// Labels are bounded in Unicode characters, not bytes; controls are rejected
// before trimming so a pasted newline cannot silently become a valid label.
func (s *ClipperConnectionService) Create(ctx context.Context, label string) (model.ClipperConnectionCreatedResponse, error) {
	if !utf8.ValidString(label) || utf8.RuneCountInString(label) > 128 || strings.ContainsFunc(label, unicode.IsControl) || strings.TrimSpace(label) == "" {
		return model.ClipperConnectionCreatedResponse{}, &model.ClipperError{Code: model.ClipperInvalidRequest}
	}
	var entropy [16 + 32]byte
	if _, err := io.ReadFull(s.random, entropy[:]); err != nil {
		return model.ClipperConnectionCreatedResponse{}, errors.New("generate Clipper credential failed")
	}
	connection := model.ClipperConnection{
		ID:        hex.EncodeToString(entropy[:16]),
		Label:     strings.TrimSpace(label),
		CreatedAt: time.Now().UTC(),
	}
	secret := connection.ID + "." + base64.RawURLEncoding.EncodeToString(entropy[16:])
	digest := sha256.Sum256([]byte(secret))
	if err := s.store.Create(ctx, connection, hex.EncodeToString(digest[:])); err != nil {
		// Store errors may contain SQL parameters, including the digest.
		return model.ClipperConnectionCreatedResponse{}, errors.New("create Clipper connection failed")
	}
	return model.ClipperConnectionCreatedResponse{Connection: connection, Secret: secret}, nil
}

// List exposes metadata only, including revoked connections.
func (s *ClipperConnectionService) List(ctx context.Context) ([]model.ClipperConnection, error) {
	connections, err := s.store.List(ctx)
	if err != nil {
		return nil, errors.New("list Clipper connections failed")
	}
	if connections == nil {
		connections = []model.ClipperConnection{}
	}
	return connections, nil
}

// Revoke is idempotent for existing connections; unknown IDs preserve ErrNoRows.
func (s *ClipperConnectionService) Revoke(ctx context.Context, id string) error {
	if err := s.store.Revoke(ctx, id, time.Now().UTC()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("revoke Clipper connection: %w", sql.ErrNoRows)
		}
		return errors.New("revoke Clipper connection failed")
	}
	return nil
}

// Authenticate accepts an opaque token, not an HTTP Authorization header.
// Unknown, malformed, mismatched and revoked credentials share one public error.
// Lookup failures fail closed with a fixed operational error, without causes.
func (s *ClipperConnectionService) Authenticate(ctx context.Context, token string) error {
	unauthorized := &model.ClipperError{Code: model.ClipperUnauthorized}
	// Bound input before parsing, decoding, hashing or querying the store.
	if len(token) != 32+1+43 || token[32] != '.' || strings.Trim(token[:32], "0123456789abcdef") != "" {
		return unauthorized
	}
	random, err := base64.RawURLEncoding.Strict().DecodeString(token[33:])
	if err != nil || len(random) != 32 {
		return unauthorized
	}
	credential, found, err := s.store.CredentialByID(ctx, token[:32])
	if err != nil {
		return errors.New("authenticate Clipper connection failed")
	}
	// Always compare fixed-size digests for syntactically valid tokens, even if
	// the ID is unknown or stored data is corrupt. This is not a claim of equal
	// end-to-end timing: database lookups and malformed-token parsing differ.
	expected := sha256.Sum256(nil)
	stored, decodeErr := hex.DecodeString(credential.Digest)
	validDigest := decodeErr == nil && len(stored) == sha256.Size
	if found && validDigest {
		copy(expected[:], stored)
	}
	actual := sha256.Sum256([]byte(token))
	match := subtle.ConstantTimeCompare(actual[:], expected[:])
	if match != 1 || !found || !validDigest || credential.Connection.RevokedAt != nil {
		return unauthorized
	}
	return nil
}
