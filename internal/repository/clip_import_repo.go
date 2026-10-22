package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"IGoNotes/internal/model"
)

type ClipImportState string

const (
	ClipImportPrepared         ClipImportState = "prepared"
	ClipImportPublishing       ClipImportState = "publishing"
	ClipImportSucceeded        ClipImportState = "succeeded"
	ClipImportRejected         ClipImportState = "rejected"
	ClipImportRecoveryRequired ClipImportState = "recovery_required"
)

var ErrClipImportTransition = errors.New("invalid or stale clip import transition")

// ClipImportJournal separates the immutable publication plan (paths, hashes,
// ownership tokens and staging references) from mutable ownership checkpoints.
// Both are JSON objects whose internal shape belongs to the import service.
// The service may retain bounded Markdown in the private plan until staging
// owns recovery content. Neither object is an API response.
type ClipImportJournal struct {
	Plan       json.RawMessage `json:"plan"`
	Checkpoint json.RawMessage `json:"checkpoint"`
}

// ClipImportRecord is private durable state, deliberately excluded from JSON.
// Only Result and Error may be copied into a public status DTO.
type ClipImportRecord struct {
	ImportID            string            `json:"-"`
	RequestDigest       string            `json:"-"`
	BaseName            string            `json:"-"`
	BasePath            string            `json:"-"`
	RootIdentity        string            `json:"-"`
	DestinationRevision string            `json:"-"`
	NotePath            string            `json:"-"`
	NoteContentHash     string            `json:"-"`
	Journal             ClipImportJournal `json:"-"`
	// Invalid private envelopes are data errors, not failed database IO. Keep
	// them listable so recovery can mark this row safe without rewriting bytes.
	JournalInvalid bool                       `json:"-"`
	State          ClipImportState            `json:"-"`
	Result         *model.ClipperImportResult `json:"-"`
	Error          *model.APIError            `json:"-"`
	CreatedAt      time.Time                  `json:"-"`
	UpdatedAt      time.Time                  `json:"-"`
}

// ClipImportVersion guards both state transitions and same-state checkpoints.
// A successful mutation invalidates every previously read version.
type ClipImportVersion struct {
	State     ClipImportState
	UpdatedAt time.Time
}

func (v ClipImportRecord) Version() ClipImportVersion { return ClipImportVersion{v.State, v.UpdatedAt} }

type ClipImportRepository struct{ db *sql.DB }

func NewClipImportRepository(db *sql.DB) *ClipImportRepository { return &ClipImportRepository{db: db} }

const clipImportColumns = `import_id, request_digest, base_name, base_path, root_identity,
 destination_revision, note_path, note_content_hash, journal_json, state, result_json,
 error_code, error_message, error_field, created_at, updated_at`

// Claim inserts a prepared plan using the database unique key, across processes
// and connections. Replay returns the stored snapshot without changing it, even
// when the supplied destination/staging metadata is newer. Digest computation
// and destination/filesystem validation are the caller's responsibility.
func (r *ClipImportRepository) Claim(ctx context.Context, v ClipImportRecord) (ClipImportRecord, bool, error) {
	if err := validateClipImportPrepared(v); err != nil {
		return ClipImportRecord{}, false, fmt.Errorf("claim clip import: %w", err)
	}
	journal, err := json.Marshal(v.Journal)
	if err != nil {
		return ClipImportRecord{}, false, fmt.Errorf("claim clip import: encode journal: %w", err)
	}
	result, err := r.exec(ctx, `INSERT INTO clip_imports (`+clipImportColumns+`) VALUES (?,?,?,?,?,?,?,?,?, ?,NULL,'','','',?,?) ON CONFLICT(import_id) DO NOTHING`, v.ImportID, v.RequestDigest, v.BaseName, v.BasePath, v.RootIdentity, v.DestinationRevision, v.NotePath, v.NoteContentHash, string(journal), v.State, formatOperationTime(v.CreatedAt), formatOperationTime(v.UpdatedAt))
	if err != nil {
		return ClipImportRecord{}, false, fmt.Errorf("claim clip import: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return ClipImportRecord{}, false, fmt.Errorf("claim clip import: %w", err)
	}
	stored, found, err := r.Get(ctx, v.ImportID)
	if err != nil {
		return ClipImportRecord{}, false, err
	}
	if !found {
		return ClipImportRecord{}, false, fmt.Errorf("claim clip import: %w", sql.ErrNoRows)
	}
	if stored.RequestDigest != v.RequestDigest {
		return ClipImportRecord{}, false, &model.ClipperError{Code: model.ClipperRequestChanged}
	}
	return stored, n == 1, nil
}

// Get returns (zero, false, nil) for an unknown import ID.
func (r *ClipImportRepository) Get(ctx context.Context, id string) (ClipImportRecord, bool, error) {
	var v ClipImportRecord
	err := retryClipImportLocked(ctx, func() error {
		var err error
		v, err = scanClipImport(r.db.QueryRowContext(ctx, `SELECT `+clipImportColumns+` FROM clip_imports WHERE import_id=?`, id))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ClipImportRecord{}, false, nil
	}
	if err != nil {
		return ClipImportRecord{}, false, fmt.Errorf("get clip import: %w", err)
	}
	return v, true, nil
}

// ListUnfinished includes recovery_required; receipts are never included.
// Filtering by canonical BasePath can be done by the caller from this snapshot.
func (r *ClipImportRepository) ListUnfinished(ctx context.Context) ([]ClipImportRecord, error) {
	var values []ClipImportRecord
	err := retryClipImportLocked(ctx, func() error {
		var err error
		values, err = r.listUnfinished(ctx)
		return err
	})
	return values, err
}

func (r *ClipImportRepository) listUnfinished(ctx context.Context) ([]ClipImportRecord, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+clipImportColumns+` FROM clip_imports WHERE state IN ('prepared','publishing','recovery_required') ORDER BY created_at, import_id`)
	if err != nil {
		return nil, fmt.Errorf("list unfinished clip imports: %w", err)
	}
	defer rows.Close()
	values := make([]ClipImportRecord, 0)
	for rows.Next() {
		v, err := scanClipImport(rows)
		if err != nil {
			return nil, fmt.Errorf("list unfinished clip imports: %w", err)
		}
		values = append(values, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list unfinished clip imports: %w", err)
	}
	return values, nil
}

// Checkpoint commits ownership progress before the next filesystem step. It
// cannot change the plan, destination, result or error, or edit terminal rows.
func (r *ClipImportRepository) Checkpoint(ctx context.Context, id string, expected ClipImportVersion, checkpoint json.RawMessage) error {
	if !clipJSONObject(checkpoint) {
		return errors.New("checkpoint clip import: expected JSON object")
	}
	if !clipImportUnfinished(expected.State) {
		return fmt.Errorf("checkpoint clip import: %w", ErrClipImportTransition)
	}
	updatedAt, err := nextClipImportTime(expected)
	if err != nil {
		return fmt.Errorf("checkpoint clip import: %w", err)
	}
	result, err := r.exec(ctx, `UPDATE clip_imports SET journal_json=json_set(journal_json,'$.checkpoint',json(?)), updated_at=? WHERE import_id=? AND state=? AND updated_at=?`, string(checkpoint), updatedAt, id, expected.State, formatOperationTime(expected.UpdatedAt))
	return clipImportMutationResult(result, err)
}

// Transition changes only state, safe receipt fields and the update timestamp.
// recovery_required -> publishing is an explicit roll-forward; -> rejected is
// abandonment. Missing/stale rows return ErrClipImportTransition. APIError has
// no cause field: callers must supply public messages, never private diagnostics.
func (r *ClipImportRepository) Transition(ctx context.Context, id string, expected ClipImportVersion, next ClipImportState, result *model.ClipperImportResult, public *model.APIError) error {
	if !clipImportAllowed(expected.State, next) {
		return fmt.Errorf("transition clip import: %w", ErrClipImportTransition)
	}
	var resultJSON any
	code, message, field := "", "", ""
	if next == ClipImportSucceeded {
		if result == nil || public != nil || result.ImportID != id || result.Revision == "" {
			return errors.New("transition clip import: invalid success result")
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("transition clip import: encode result: %w", err)
		}
		resultJSON = string(encoded)
	} else if next == ClipImportRejected || next == ClipImportRecoveryRequired {
		if result != nil || public == nil || public.Code == "" || public.Message == "" {
			return errors.New("transition clip import: invalid public error")
		}
		code, message, field = public.Code, public.Message, public.Field
	} else if result != nil || public != nil {
		return errors.New("transition clip import: publishing cannot have a receipt")
	}
	// Match result destination in the same guarded UPDATE, without a read/write
	// race or accepting edits to the original historical destination.
	base, path := "", ""
	if result != nil {
		base, path = result.Base, result.Path
	}
	updatedAt, err := nextClipImportTime(expected)
	if err != nil {
		return fmt.Errorf("transition clip import: %w", err)
	}
	res, err := r.exec(ctx, `UPDATE clip_imports SET state=?, result_json=?, error_code=?, error_message=?, error_field=?, updated_at=? WHERE import_id=? AND state=? AND updated_at=? AND (? != 'succeeded' OR (base_name=? AND note_path=?))`, next, resultJSON, code, message, field, updatedAt, id, expected.State, formatOperationTime(expected.UpdatedAt), next, base, path)
	return clipImportMutationResult(res, err)
}

func clipImportAllowed(from, to ClipImportState) bool {
	switch from {
	case ClipImportPrepared:
		return to == ClipImportPublishing || to == ClipImportRejected || to == ClipImportRecoveryRequired
	case ClipImportPublishing:
		return to == ClipImportSucceeded || to == ClipImportRejected || to == ClipImportRecoveryRequired
	case ClipImportRecoveryRequired:
		return to == ClipImportPublishing || to == ClipImportRejected
	}
	return false
}

func clipImportUnfinished(state ClipImportState) bool {
	return state == ClipImportPrepared || state == ClipImportPublishing || state == ClipImportRecoveryRequired
}

func nextClipImportTime(expected ClipImportVersion) (string, error) {
	now := time.Now().UTC()
	if !now.After(expected.UpdatedAt) {
		now = expected.UpdatedAt.Add(time.Nanosecond)
	}
	if !validClipperConnectionTime(now) {
		return "", errors.New("next import timestamp is outside the RFC3339 range")
	}
	return formatOperationTime(now), nil
}

func clipImportMutationResult(result sql.Result, err error) error {
	if err != nil {
		return fmt.Errorf("update clip import: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update clip import: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("update clip import: %w", ErrClipImportTransition)
	}
	return nil
}

func clipJSONObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return json.Valid(raw) && len(trimmed) > 0 && trimmed[0] == '{'
}

func validateClipImportPrepared(v ClipImportRecord) error {
	if v.ImportID == "" || v.BaseName == "" || v.BasePath == "" || v.RootIdentity == "" || v.DestinationRevision == "" || v.NotePath == "" {
		return errors.New("missing required plan field")
	}
	for _, digest := range []string{v.RequestDigest, v.NoteContentHash} {
		if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
			return errors.New("invalid SHA-256 digest")
		}
	}
	if v.State != ClipImportPrepared || v.Result != nil || v.Error != nil {
		return errors.New("expected prepared state without receipt")
	}
	if v.JournalInvalid || !clipJSONObject(v.Journal.Plan) || !clipJSONObject(v.Journal.Checkpoint) {
		return errors.New("journal plan and checkpoint must be JSON objects")
	}
	if !validClipperConnectionTime(v.CreatedAt) || !validClipperConnectionTime(v.UpdatedAt) || v.UpdatedAt.Before(v.CreatedAt) {
		return errors.New("invalid import timestamps")
	}
	return nil
}

func scanClipImport(scanner rowScanner) (ClipImportRecord, error) {
	var v ClipImportRecord
	var journal, created, updated, code, message, field string
	var result sql.NullString
	if err := scanner.Scan(&v.ImportID, &v.RequestDigest, &v.BaseName, &v.BasePath, &v.RootIdentity, &v.DestinationRevision, &v.NotePath, &v.NoteContentHash, &journal, &v.State, &result, &code, &message, &field, &created, &updated); err != nil {
		return ClipImportRecord{}, err
	}
	v.Journal, v.JournalInvalid = decodeClipImportJournalEnvelope(journal)
	if result.Valid {
		if err := json.Unmarshal([]byte(result.String), &v.Result); err != nil {
			return ClipImportRecord{}, fmt.Errorf("decode import result: %w", err)
		}
	}
	if code != "" || message != "" || field != "" {
		v.Error = &model.APIError{Code: code, Message: message, Field: field}
	}
	var err error
	v.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return ClipImportRecord{}, fmt.Errorf("parse import creation time: %w", err)
	}
	v.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return ClipImportRecord{}, fmt.Errorf("parse import update time: %w", err)
	}
	return v, nil
}

// The envelope schema belongs here; plan/checkpoint versions belong to service.
// Never normalize, repair or persist a malformed envelope during a read.
func decodeClipImportJournalEnvelope(raw string) (ClipImportJournal, bool) {
	var journal ClipImportJournal
	d := json.NewDecoder(strings.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return journal, true
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] || (name != "plan" && name != "checkpoint") {
			return journal, true
		}
		seen[name] = true
		var value json.RawMessage
		if err := d.Decode(&value); err != nil || !clipJSONObject(value) {
			return journal, true
		}
		if name == "plan" {
			journal.Plan = value
		} else {
			journal.Checkpoint = value
		}
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') || len(seen) != 2 {
		return journal, true
	}
	if _, err := d.Token(); err != io.EOF {
		return journal, true
	}
	return journal, false
}

func (r *ClipImportRepository) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	var result sql.Result
	err := retryClipImportLocked(ctx, func() error {
		var err error
		result, err = r.db.ExecContext(ctx, query, args...)
		return err
	})
	return result, err
}

// Retry only SQLite lock contention, including reads during an exclusive commit.
// No read-then-write transaction or process-local mutex arbitrates ownership.
// The bounded wait supports DBs opened without a busy_timeout pragma.
func retryClipImportLocked(ctx context.Context, run func() error) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := run()
		var sqliteError interface{ Code() int }
		if !errors.As(err, &sqliteError) || (sqliteError.Code()&255 != 5 && sqliteError.Code()&255 != 6) || time.Now().After(deadline) {
			return err
		}
		timer := time.NewTimer(time.Millisecond * 5)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
