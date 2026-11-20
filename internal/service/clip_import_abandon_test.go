package service

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

type abandonFileSnapshot struct {
	Data string
	Info fs.FileInfo
}

func snapshotAbandonFiles(t *testing.T, root string) map[string]abandonFileSnapshot {
	t.Helper()
	result := map[string]abandonFileSnapshot{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		result[p] = abandonFileSnapshot{string(data), info}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return result
}
func assertAbandonFiles(t *testing.T, before, after map[string]abandonFileSnapshot) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatal("file count changed")
	}
	for p, old := range before {
		now, ok := after[p]
		if !ok || old.Data != now.Data || !os.SameFile(old.Info, now.Info) {
			t.Fatalf("file changed: %s", p)
		}
	}
}

func TestClipImportAbandonPreservesEvidenceAndReplay(t *testing.T) {
	for _, images := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "images"}[images], func(t *testing.T) {
			f, row := crashedRecovery(t, "before-receipt", "target", images)
			writeTestNote(t, f.target, row.NotePath, "user edit")
			if err := f.service.RecoverLocal(t.Context()); err != nil {
				t.Fatal(err)
			}
			list, err := f.service.ListUnfinished(t.Context())
			if err != nil || len(list) != 1 || list[0].State != model.ClipperImportRecoveryRequired || list[0].Base != "target" || list[0].Path != row.NotePath {
				t.Fatalf("list %+v %v", list, err)
			}
			publicBefore := snapshotAbandonFiles(t, f.target)
			privateBefore := snapshotAbandonFiles(t, f.settings.privateStorage.stagingDir)
			config := f.settings.GetConfig()
			status, err := f.service.Abandon(t.Context(), row.ImportID)
			if err != nil || status.State != model.ClipperImportRejected || status.Error == nil || status.Error.Code != "clipper_import_abandoned" {
				t.Fatalf("abandon %+v %v", status, err)
			}
			again, err := f.service.Abandon(t.Context(), row.ImportID)
			if err != nil || !reflect.DeepEqual(status, again) {
				t.Fatalf("idempotence %+v %v", again, err)
			}
			assertAbandonFiles(t, publicBefore, snapshotAbandonFiles(t, f.target))
			assertAbandonFiles(t, privateBefore, snapshotAbandonFiles(t, f.settings.privateStorage.stagingDir))
			stored, _, _ := f.store.Get(t.Context(), row.ImportID)
			if !reflect.DeepEqual(stored.Journal, row.Journal) {
				t.Fatal("journal changed")
			}
			db, err := repository.InitDB(f.dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			f.service.store = repository.NewClipImportRepository(db)
			if err := f.service.RecoverLocal(t.Context()); err != nil {
				t.Fatal(err)
			}
			m := validClipManifest()
			m.Base = row.BaseName
			m.DestinationRevision = row.DestinationRevision
			m.ExternalImages = []model.ClipperExternalImage{{URL: "https://example.com/a", Reason: "offline"}, {URL: "https://example.com/b", Reason: "offline"}}
			if images {
				m.Assets = []model.ClipperAsset{clipDescriptor(clipTestPart{"a", "image/png", clipTestImages(t)["image/png"]})}
			}
			replayed, replay, err := f.service.Import(t.Context(), m)
			if err != nil || !replay || !reflect.DeepEqual(replayed, status) {
				t.Fatalf("replay %+v %v %v", replayed, replay, err)
			}
			assertAbandonFiles(t, publicBefore, snapshotAbandonFiles(t, f.target))
			assertAbandonFiles(t, privateBefore, snapshotAbandonFiles(t, f.settings.privateStorage.stagingDir))
			m = f.manifest(t, "target")
			m.ImportID = "12345678-1234-4123-8123-123456789abc"
			m.Filename = "new.md"
			next, _, err := f.service.Import(t.Context(), m)
			if err != nil || next.State != model.ClipperImportSucceeded {
				t.Fatalf("gate not released %+v %v", next, err)
			}
			if !reflect.DeepEqual(config, f.settings.GetConfig()) || f.notes.GetBasePath() != f.active {
				t.Fatal("config/runtime changed")
			}
		})
	}
}

func TestClipImportAbandonEligibilityAndCancellation(t *testing.T) {
	f, row := crashedRecovery(t, "prepared", "target", false)
	for _, state := range []repository.ClipImportState{repository.ClipImportPrepared, repository.ClipImportPublishing} {
		if state == repository.ClipImportPublishing {
			if err := f.store.Transition(t.Context(), row.ImportID, row.Version(), state, nil, nil); err != nil {
				t.Fatal(err)
			}
			row, _, _ = f.store.Get(t.Context(), row.ImportID)
		}
		list, err := f.service.ListUnfinished(t.Context())
		if err != nil || len(list) != 1 || list[0].State != model.ClipperImportPending || list[0].Error != nil {
			t.Fatalf("pending summary %+v %v", list, err)
		}
		if _, err := f.service.Abandon(t.Context(), row.ImportID); err == nil {
			t.Fatal("abandoned pending state", state)
		}
	}
	if _, err := f.service.Abandon(t.Context(), row.ImportID); err == nil {
		t.Fatal("abandoned pending")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.service.Abandon(ctx, row.ImportID); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := f.service.Abandon(t.Context(), "bad"); err == nil {
		t.Fatal("invalid ID")
	}
	if _, err := f.service.Abandon(t.Context(), "12345678-1234-4123-8123-123456789abc"); err == nil {
		t.Fatal("unknown ID")
	}
	if err := f.service.RecoverLocal(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Abandon(t.Context(), row.ImportID); err == nil {
		t.Fatal("abandoned success")
	}
}

func TestClipImportAbandonOtherRootHoldAndConcurrentRecovery(t *testing.T) {
	f, row := crashedRecovery(t, "before-receipt", "target", true)
	writeTestNote(t, f.target, row.NotePath, "user edit")
	if err := f.service.RecoverLocal(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A second complete immutable plan in the same root independently holds the gate.
	row2 := row
	row2.ImportID = "12345678-1234-4123-8123-123456789abc"
	row2.State = repository.ClipImportPrepared
	row2.Result = nil
	row2.Error = nil
	var p clipImagePlan
	if err := json.Unmarshal(row2.Journal.Plan, &p); err != nil {
		t.Fatal(err)
	}
	p.Note.Filename = "second.md"
	row2.NotePath = "second.md"
	// Use a valid text plan with unknown ownership, not a fabricated unsafe path.
	row2.Journal.Plan, _ = json.Marshal(clipImportPlan{Version: 1, Note: p.Note, Markdown: p.Markdown, Warnings: p.Warnings})
	row2.Journal.Checkpoint, _ = json.Marshal(clipImportCheckpoint{Step: "creating"})
	r, _, err := f.store.Claim(t.Context(), row2)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Transition(t.Context(), r.ImportID, r.Version(), repository.ClipImportRecoveryRequired, nil, &model.APIError{Code: "clipper_recovery_required", Message: "Clipper import requires recovery"}); err != nil {
		t.Fatal(err)
	}
	// Keep recovery ambiguous for both plans while racing it with abandonment.
	writeTestNote(t, f.target, "second.md", "unrelated user file")
	before := snapshotAbandonFiles(t, f.target)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Go(func() { _, err := f.service.Abandon(t.Context(), row.ImportID); errs <- err })
	wg.Go(func() { errs <- f.service.RecoverLocal(t.Context()) })
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertAbandonFiles(t, before, snapshotAbandonFiles(t, f.target))
	m := f.manifest(t, "target")
	m.ImportID = "22345678-1234-4123-8123-123456789abc"
	m.Filename = "third.md"
	_, _, err = f.service.Import(t.Context(), m)
	var ce *model.ClipperError
	if !errors.As(err, &ce) || ce.Code != model.ClipperRecoveryRequired {
		t.Fatal("other hold released", err)
	}
	list, err := f.service.ListUnfinished(t.Context())
	if err != nil || len(list) != 1 || list[0].ImportID != row2.ImportID {
		t.Fatalf("list %+v %v", list, err)
	}
	// Abandon is metadata-only even with an active Git conflict; it must not clear it.
	f.settings.coordinator.SetConflict(f.target, true)
	if _, err := f.service.Abandon(t.Context(), row2.ImportID); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(f.settings.coordinator.CheckMutation(f.target), ErrGitConflictPending) {
		t.Fatal("Git gate cleared")
	}
}

func TestClipImportAbandonRejectsOtherRejected(t *testing.T) {
	f, row := crashedRecovery(t, "prepared", "target", false)
	if err := f.store.Transition(t.Context(), row.ImportID, row.Version(), repository.ClipImportRejected, nil, &model.APIError{Code: "internal_error", Message: "Internal server error"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Abandon(t.Context(), row.ImportID); err == nil {
		t.Fatal("rewrote rejection")
	}
	stored, _, _ := f.store.Get(t.Context(), row.ImportID)
	if stored.Error.Code != "internal_error" {
		t.Fatal("historical receipt changed")
	}
}

type staleAbandonStore struct{ ClipImportStore }

func (s staleAbandonStore) Transition(ctx context.Context, id string, version repository.ClipImportVersion, next repository.ClipImportState, result *model.ClipperImportResult, public *model.APIError) error {
	// A different DB writer updates the version between read and CAS.
	row, _, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.Checkpoint(ctx, id, row.Version(), row.Journal.Checkpoint); err != nil {
		return err
	}
	return s.ClipImportStore.Transition(ctx, id, version, next, result, public)
}
func TestClipImportAbandonStaleCASAndSafeSummary(t *testing.T) {
	f, row := crashedRecovery(t, "before-receipt", "target", true)
	writeTestNote(t, f.target, row.NotePath, "user edit")
	if err := f.service.RecoverLocal(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.service.store = staleAbandonStore{f.store}
	_, err := f.service.Abandon(t.Context(), row.ImportID)
	var ce *model.ClipperError
	if !errors.As(err, &ce) || ce.Code != model.ClipperImportNotAbandonable {
		t.Fatal("stale CAS", err)
	}
	stored, _, _ := f.store.Get(t.Context(), row.ImportID)
	if stored.State != repository.ClipImportRecoveryRequired {
		t.Fatal("lost CAS mutated state")
	}
	list, err := f.service.ListUnfinished(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	var values []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || len(values[0]) != 5 {
		t.Fatal("nonminimal DTO", string(raw))
	}
	for _, field := range []string{"import_id", "base", "path", "state", "error"} {
		if _, ok := values[0][field]; !ok {
			t.Fatal("missing field", field)
		}
	}
}
