package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"IGoNotes/internal/model"
)

// ClipperConnectionCredential is private authentication data, not a wire DTO.
// A service can encode the connection ID alongside random secret bytes, look up
// this record by ID, and compare SHA-256 digests in constant time. It must also
// reject Connection.RevokedAt != nil; lookup deliberately retains revoked rows.
// Both fields are excluded from JSON, including when this value is embedded.
type ClipperConnectionCredential struct {
	Connection model.ClipperConnection `json:"-"`
	Digest     string                  `json:"-"`
}

type ClipperConnectionRepository struct {
	db *sql.DB
}

func NewClipperConnectionRepository(db *sql.DB) *ClipperConnectionRepository {
	return &ClipperConnectionRepository{db: db}
}

// Create inserts active metadata and a lowercase SHA-256 hex digest, never a raw
// secret. IDs are nonempty ASCII letters/digits/hyphen/underscore; labels must
// be nonblank valid UTF-8 without control characters. Times must be nonzero and
// representable in RFC3339; storage normalizes them to UTC with nanoseconds.
// Duplicate IDs/digests return the wrapped database constraint error.
func (r *ClipperConnectionRepository) Create(ctx context.Context, connection model.ClipperConnection, digest string) error {
	if !validClipperConnectionID(connection.ID) {
		return errors.New("create Clipper connection: invalid ID")
	}
	if !utf8.ValidString(connection.Label) || strings.TrimSpace(connection.Label) == "" || strings.ContainsFunc(connection.Label, unicode.IsControl) {
		return errors.New("create Clipper connection: invalid label")
	}
	if !validClipperConnectionTime(connection.CreatedAt) || connection.RevokedAt != nil {
		return errors.New("create Clipper connection: invalid creation state or time")
	}
	if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
		return errors.New("create Clipper connection: invalid SHA-256 digest")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO clipper_connections (id, label, secret_digest, created_at)
		VALUES (?, ?, ?, ?)
	`, connection.ID, connection.Label, digest, connection.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("create Clipper connection: %w", err)
	}
	return nil
}

// List returns public metadata, including revoked connections, in ID order.
// Its query intentionally never reads the credential column.
func (r *ClipperConnectionRepository) List(ctx context.Context) ([]model.ClipperConnection, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, label, created_at, revoked_at FROM clipper_connections ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list Clipper connections: %w", err)
	}
	defer rows.Close()
	connections := make([]model.ClipperConnection, 0)
	for rows.Next() {
		var connection model.ClipperConnection
		var created string
		var revoked sql.NullString
		if err := rows.Scan(&connection.ID, &connection.Label, &created, &revoked); err != nil {
			return nil, fmt.Errorf("list Clipper connections: %w", err)
		}
		if err := parseClipperConnectionTimes(&connection, created, revoked); err != nil {
			return nil, fmt.Errorf("list Clipper connections: %w", err)
		}
		connections = append(connections, connection)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Clipper connections: %w", err)
	}
	return connections, nil
}

// CredentialByID returns (zero, false, nil) for an unknown ID. It does not
// authenticate: callers must check revocation and compare the digest themselves.
func (r *ClipperConnectionRepository) CredentialByID(ctx context.Context, id string) (ClipperConnectionCredential, bool, error) {
	var credential ClipperConnectionCredential
	var created string
	var revoked sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT id, label, created_at, revoked_at, secret_digest FROM clipper_connections WHERE id = ?`, id).Scan(
		&credential.Connection.ID, &credential.Connection.Label, &created, &revoked, &credential.Digest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ClipperConnectionCredential{}, false, nil
	}
	if err == nil {
		err = parseClipperConnectionTimes(&credential.Connection, created, revoked)
	}
	if err != nil {
		return ClipperConnectionCredential{}, false, fmt.Errorf("lookup Clipper credential: %w", err)
	}
	return credential, true, nil
}

// Revoke preserves the first revocation timestamp on repeated calls. An unknown
// ID returns a wrapped sql.ErrNoRows; metadata and digest remain available.
func (r *ClipperConnectionRepository) Revoke(ctx context.Context, id string, revokedAt time.Time) error {
	if !validClipperConnectionTime(revokedAt) {
		return errors.New("revoke Clipper connection: invalid time")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE clipper_connections SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`, revokedAt.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("revoke Clipper connection: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke Clipper connection: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("revoke Clipper connection: %w", sql.ErrNoRows)
	}
	return nil
}

func validClipperConnectionID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validClipperConnectionTime(value time.Time) bool {
	return !value.IsZero() && value.UTC().Year() >= 1 && value.UTC().Year() <= 9999
}

func parseClipperConnectionTimes(connection *model.ClipperConnection, created string, revoked sql.NullString) error {
	createdAt, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return errors.New("invalid stored Clipper creation time")
	}
	connection.CreatedAt = createdAt.UTC()
	if revoked.Valid {
		revokedAt, err := time.Parse(time.RFC3339Nano, revoked.String)
		if err != nil {
			return errors.New("invalid stored Clipper revocation time")
		}
		revokedAt = revokedAt.UTC()
		connection.RevokedAt = &revokedAt
	}
	return nil
}
