package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"reflect"
	"strings"
	"time"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

// ClipImportStore owns durable claims and guarded mutations, not publication.
type ClipImportStore interface {
	Claim(context.Context, repository.ClipImportRecord) (repository.ClipImportRecord, bool, error)
	Get(context.Context, string) (repository.ClipImportRecord, bool, error)
	ListUnfinished(context.Context) ([]repository.ClipImportRecord, error)
	Checkpoint(context.Context, string, repository.ClipImportVersion, json.RawMessage) error
	Transition(context.Context, string, repository.ClipImportVersion, repository.ClipImportState, *model.ClipperImportResult, *model.APIError) error
}

type ClipImportService struct {
	settings     *SettingsService
	destinations *ClipperDestinationService
	notes        *NoteService
	store        ClipImportStore
	prepare      func(*os.Root, string, string, string) (*clipNoteFile, error)
}

func NewClipImportService(settings *SettingsService, destinations *ClipperDestinationService, notes *NoteService, store ClipImportStore) (*ClipImportService, error) {
	if settings == nil || destinations == nil || notes == nil || store == nil ||
		(reflect.ValueOf(store).Kind() == reflect.Pointer && reflect.ValueOf(store).IsNil()) ||
		destinations.settings != settings || settings.notes != notes || settings.coordinator == nil || notes.coordinator != settings.coordinator {
		return nil, ErrInvalidConfig
	}
	return &ClipImportService{settings: settings, destinations: destinations, notes: notes, store: store, prepare: prepareClipNote}, nil
}

// Versioned private journal: the immutable text is deliberately retained until
// recovery/staging can own it. A hash alone cannot roll an interrupted import
// forward. Future asset/staging plans may extend this object without changing the
// public Import/Status contracts. Never serialize this journal into a response.
type clipImportPlan struct {
	Version  int          `json:"version"`
	Note     clipNotePlan `json:"note"`
	Markdown string       `json:"markdown"`
}

type clipImportCheckpoint struct {
	Step    string           `json:"step"`
	Note    clipFileProgress `json:"note"`
	Indexed bool             `json:"indexed"`
}

// Import returns a public snapshot and whether the ID already existed. A new
// success is 201; a replayed success is 200; pending is 202. Rejected/recovery
// snapshots carry fixed public errors. Transport/authentication belongs upstream.
// Completed receipts are looked up BEFORE touching the process-local registry.
func (s *ClipImportService) Import(ctx context.Context, manifest model.ClipperManifest) (status model.ClipperImportStatusResponse, replay bool, err error) {
	digest, err := ClipperManifestDigest(manifest)
	if err != nil {
		return status, false, fixedClipperError(err)
	}
	lookup := func() (bool, error) {
		record, found, err := s.store.Get(ctx, manifest.ImportID)
		if err != nil {
			return false, fixedClipperError(err)
		}
		if !found {
			return false, nil
		}
		if record.RequestDigest != digest {
			return false, &model.ClipperError{Code: model.ClipperRequestChanged}
		}
		status, replay = publicClipImport(record), true
		return true, nil
	}
	if found, err := lookup(); found || err != nil {
		return status, replay, err
	}
	// Stage 4 has no asset bytes. Reject before claiming; the manifest and journal
	// APIs remain extensible for the asset publication stage.
	if len(manifest.Assets) != 0 {
		return status, false, &model.ClipperError{Code: model.ClipperInvalidRequest}
	}
	s.settings.coordinator.Lock()
	defer s.settings.coordinator.Unlock()
	if found, err := lookup(); found || err != nil {
		return status, replay, err
	}
	// New note parents must be visible in the notes tree. Keep this publication
	// policy after receipt lookup, separate from syntax/digest validation: a
	// historical result is replayed verbatim even if its parent is now excluded.
	if manifest.Parent != "" {
		for _, component := range strings.Split(manifest.Parent, "/") {
			if !noteDirectoryVisible(component) {
				return status, false, &model.ClipperError{Code: model.ClipperInvalidRequest}
			}
		}
	}
	err = s.destinations.withDestinationCoordinatorLocked(manifest.Base, manifest.DestinationRevision, func(snapshot ClipperDestinationSnapshot, root *os.Root) error {
		var err error
		status, replay, err = s.importDestinationLocked(ctx, manifest, digest, snapshot, root)
		return err
	})
	// Closing a scoped root cannot invalidate an already persisted receipt or
	// turn an uncertain attempt into a definite HTTP rejection.
	if status.ImportID != "" {
		return status, replay, nil
	}
	return status, replay, fixedClipperError(err)
}

// importDestinationLocked is deliberately below the scoped snapshot boundary:
// coordinator -> settings.mu -> notes.baseMu are ALREADY held. No public service
// calls, lock reacquisition, runtime switching or network IO is allowed here.
func (s *ClipImportService) importDestinationLocked(ctx context.Context, m model.ClipperManifest, digest string, snapshot ClipperDestinationSnapshot, root *os.Root) (model.ClipperImportStatusResponse, bool, error) {
	zero := model.ClipperImportStatusResponse{}
	if err := s.settings.coordinator.checkMutationForIdentity(snapshot.CanonicalPath, snapshot.RootIdentity); err != nil {
		return zero, false, err
	}
	rootFile, err := root.Open(".")
	if err != nil {
		return zero, false, err
	}
	identity, idErr := clipIdentity(rootFile)
	if err := errors.Join(idErr, rootFile.Close()); err != nil {
		return zero, false, err
	}
	if identity == (clipFileIdentity{}) {
		return zero, false, &model.ClipperError{Code: model.ClipperRecoveryRequired}
	}
	unfinished, err := s.store.ListUnfinished(ctx)
	if err != nil {
		return zero, false, err
	}
	for _, record := range unfinished {
		storedIdentity, err := decodeClipImportRootIdentity(record.RootIdentity)
		// An unreadable identity cannot prove that an unfinished operation is
		// unrelated. Fail closed, including when its historical path differs.
		// Match either identity (directory moved) or path (root replaced).
		if err != nil || storedIdentity == identity || record.BasePath == snapshot.CanonicalPath {
			return zero, false, &model.ClipperError{Code: model.ClipperRecoveryRequired}
		}
	}
	file, err := s.prepare(root, m.Parent, m.Filename, m.Markdown)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return zero, false, &model.ClipperError{Code: model.ClipperDestinationNotFound}
		}
		return zero, false, err
	}
	defer file.close() // handles only; never late rollback
	if _, err := file.parent.Lstat(file.plan.Filename); err == nil {
		return zero, false, &model.ClipperError{Code: model.ClipperNoteConflict}
	} else if !errors.Is(err, os.ErrNotExist) {
		return zero, false, err
	}
	identityJSON, _ := json.Marshal(identity)
	planJSON, _ := json.Marshal(clipImportPlan{Version: 1, Note: file.plan, Markdown: m.Markdown})
	checkpoint := clipImportCheckpoint{Step: "creating"}
	checkpointJSON, _ := json.Marshal(checkpoint)
	now := time.Now().UTC()
	record, created, err := s.store.Claim(ctx, repository.ClipImportRecord{
		ImportID: m.ImportID, RequestDigest: digest, BaseName: snapshot.Name,
		BasePath: snapshot.CanonicalPath, RootIdentity: string(identityJSON), DestinationRevision: m.DestinationRevision,
		NotePath: path.Join(m.Parent, m.Filename), NoteContentHash: file.plan.SHA256,
		Journal: repository.ClipImportJournal{Plan: planJSON, Checkpoint: checkpointJSON},
		State:   repository.ClipImportPrepared, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		var conflict *model.ClipperError
		if errors.As(err, &conflict) && conflict.Code == model.ClipperRequestChanged {
			return zero, false, &model.ClipperError{Code: model.ClipperRequestChanged}
		}
		// Claim may have committed before losing its acknowledgement. No file has
		// been created; don't describe the ID as definitely rejected.
		return zero, false, &model.ClipperError{Code: model.ClipperRecoveryRequired}
	}
	if !created {
		return publicClipImport(record), true, nil
	}
	progress := func(step string) error {
		checkpoint.Step, checkpoint.Note = step, file.progress
		encoded, _ := json.Marshal(checkpoint)
		if err := s.store.Checkpoint(ctx, record.ImportID, record.Version(), encoded); err != nil {
			return err
		}
		return s.reloadImport(ctx, &record)
	}
	fail := func(cause error) (model.ClipperImportStatusResponse, bool, error) {
		return s.finishFailedImport(ctx, record, file, checkpoint, cause), false, nil
	}
	if _, err := file.create(); err != nil {
		return fail(err)
	}
	if err := progress("writing"); err != nil {
		return fail(err)
	}
	if _, err := file.write(); err != nil {
		return fail(err)
	}
	if err := progress("written"); err != nil {
		return fail(err)
	}
	if err := s.store.Transition(ctx, record.ImportID, record.Version(), repository.ClipImportPublishing, nil, nil); err != nil {
		return fail(err)
	}
	if err := s.reloadImport(ctx, &record); err != nil {
		return fail(err)
	}
	if _, err := file.publish(); err != nil {
		return fail(err)
	}
	if err := progress("published"); err != nil {
		return fail(err)
	}
	if snapshot.Active {
		if err := s.notes.replaceIndexLocked(); err != nil {
			return fail(err)
		}
		checkpoint.Indexed = true
	}
	if err := progress("receipt"); err != nil {
		return fail(err)
	}
	warnings := make([]string, 0, len(m.ExternalImages))
	for _, external := range m.ExternalImages {
		warnings = append(warnings, external.Reason)
	}
	result := &model.ClipperImportResult{ImportID: record.ImportID, Base: record.BaseName, Path: record.NotePath, Revision: noteRevision([]byte(m.Markdown)), Warnings: warnings}
	if err := s.store.Transition(ctx, record.ImportID, record.Version(), repository.ClipImportSucceeded, result, nil); err != nil {
		return fail(err)
	}
	record.State, record.Result, record.Error = repository.ClipImportSucceeded, result, nil
	return publicClipImport(record), false, nil
}

// Decode only the immutable native identity shape written by this service. Do
// not resolve historical paths (which may now name an unrelated root), infer an
// identity from them, or silently fall back to path-only matching on corruption.
func decodeClipImportRootIdentity(raw string) (clipFileIdentity, error) {
	var identity clipFileIdentity
	decoder := json.NewDecoder(strings.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return identity, os.ErrInvalid
	}
	seen := make(map[string]bool, 2)
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] || (name != "device" && name != "file") {
			return clipFileIdentity{}, os.ErrInvalid
		}
		var value *uint64
		if err := decoder.Decode(&value); err != nil || value == nil {
			return clipFileIdentity{}, os.ErrInvalid
		}
		seen[name] = true
		if name == "device" {
			identity.Device = *value
		} else {
			identity.File = *value
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || len(seen) != 2 || identity == (clipFileIdentity{}) {
		return clipFileIdentity{}, os.ErrInvalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return clipFileIdentity{}, os.ErrInvalid
	}
	return identity, nil
}

func (s *ClipImportService) reloadImport(ctx context.Context, record *repository.ClipImportRecord) error {
	current, found, err := s.store.Get(ctx, record.ImportID)
	if err != nil {
		return err
	}
	if !found {
		return sql.ErrNoRows
	}
	*record = current
	return nil
}

// Cleanup is allowed only in the original locked scope and only when publication
// is known not to have happened. Journal failures leave an unfinished record if
// we cannot durably prove a terminal outcome. Cancellation must not skip this
// bounded bookkeeping. Published/uncertain notes are NEVER removed.
func (s *ClipImportService) finishFailedImport(ctx context.Context, record repository.ClipImportRecord, file *clipNoteFile, checkpoint clipImportCheckpoint, cause error) model.ClipperImportStatusResponse {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	safe := !file.progress.Published && !file.progress.PublicationUncertain
	if safe {
		safe = file.discardTemp() == nil
	}
	if err := s.reloadImport(cleanupCtx, &record); err != nil {
		return publicClipImport(record)
	}
	if record.State == repository.ClipImportSucceeded || record.State == repository.ClipImportRejected {
		return publicClipImport(record)
	}
	next := repository.ClipImportRecoveryRequired
	public := &model.APIError{Code: string(model.ClipperRecoveryRequired), Message: "Clipper import requires recovery"}
	checkpoint.Step, checkpoint.Note = "recovery", file.progress
	if safe {
		next = repository.ClipImportRejected
		public = &model.APIError{Code: "internal_error", Message: "Internal server error"}
		checkpoint.Step = "cleaned"
	}
	_ = cause // private IO diagnostics are never copied into receipts or responses
	encoded, _ := json.Marshal(checkpoint)
	if err := s.store.Checkpoint(cleanupCtx, record.ImportID, record.Version(), encoded); err != nil {
		return publicClipImport(record)
	}
	if err := s.reloadImport(cleanupCtx, &record); err != nil {
		return publicClipImport(record)
	}
	if record.State != next {
		if err := s.store.Transition(cleanupCtx, record.ImportID, record.Version(), next, nil, public); err != nil {
			_ = s.reloadImport(cleanupCtx, &record)
			return publicClipImport(record)
		}
	}
	record.State, record.Error, record.Result = next, public, nil
	return publicClipImport(record)
}

func (s *ClipImportService) Status(ctx context.Context, id string) (model.ClipperImportStatusResponse, error) {
	if !clipperUUID.MatchString(id) {
		return model.ClipperImportStatusResponse{}, &model.ClipperError{Code: model.ClipperInvalidRequest}
	}
	record, found, err := s.store.Get(ctx, id)
	if err != nil {
		return model.ClipperImportStatusResponse{}, fixedClipperError(err)
	}
	if !found {
		return model.ClipperImportStatusResponse{}, sql.ErrNoRows
	}
	return publicClipImport(record), nil
}

func publicClipImport(record repository.ClipImportRecord) model.ClipperImportStatusResponse {
	status := model.ClipperImportStatusResponse{ImportID: record.ImportID, State: model.ClipperImportPending}
	switch record.State {
	case repository.ClipImportSucceeded:
		status.State = model.ClipperImportSucceeded
		if record.Result != nil {
			result := *record.Result
			result.Warnings = append([]string{}, result.Warnings...)
			status.Result = &result
		}
	case repository.ClipImportRejected, repository.ClipImportRecoveryRequired:
		status.State = model.ClipperImportState(record.State)
		if record.Error != nil {
			public := *record.Error
			status.Error = &public
		}
	}
	return status
}

// Preserve only recognized public categories. Arbitrary IO/SQL errors can carry
// filesystem paths or journal content; never expose their Error() strings.
func fixedClipperError(err error) error {
	if err == nil {
		return nil
	}
	var public *model.ClipperError
	if errors.As(err, &public) && public != nil {
		return &model.ClipperError{Code: public.Code}
	}
	if errors.Is(err, ErrGitConflictPending) {
		return ErrGitConflictPending
	}
	return errors.New("Clipper import failed")
}
