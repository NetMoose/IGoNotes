package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

// The child actually exits inside production publication, bypassing every defer
// and failure-cleanup closure. The parent opens a new DB and runtime afterwards.
func recoveryFixture(t *testing.T, home string) *clipImportFixture {
	t.Helper()
	f := &clipImportFixture{active: filepath.Join(home, "active"), target: filepath.Join(home, "target"), dbPath: filepath.Join(home, "metadata.db")}
	for _, dir := range []string{f.active, f.target, filepath.Join(home, "config")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	f.db, err = repository.InitDB(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Close() })
	f.index = repository.NewNoteRepository(f.db)
	c := NewBaseOperationCoordinator()
	f.notes = NewNoteService(f.index, f.active, c)
	t.Cleanup(func() { f.notes.Close() })
	done := true
	cfg := model.Config{Bases: []model.Base{{Name: "active", Path: f.active}, {Name: "target", Path: f.target}}, CurrentBase: "active", SetupCompleted: &done}
	f.settings, err = NewSettingsService(&fakeConfigStore{config: &cfg}, f.notes, c, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.settings.privateStorage, err = NewPrivateStoragePolicy(filepath.Join(home, "config"), f.dbPath, filepath.Join(home, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	f.dest = NewClipperDestinationService(f.settings)
	f.store = repository.NewClipImportRepository(f.db)
	f.service, err = NewClipImportService(f.settings, f.dest, f.notes, f.store)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

type crashClipStore struct {
	ClipImportStore
	phase string
}

func (s crashClipStore) Claim(ctx context.Context, r repository.ClipImportRecord) (repository.ClipImportRecord, bool, error) {
	r, created, err := s.ClipImportStore.Claim(ctx, r)
	if err == nil && s.phase == "prepared" {
		os.Exit(73)
	}
	return r, created, err
}
func (s crashClipStore) Checkpoint(ctx context.Context, id string, v repository.ClipImportVersion, raw json.RawMessage) error {
	var cp clipImageCheckpoint
	json.Unmarshal(raw, &cp)
	if (s.phase == "create-gap" && cp.Step == "writing") || (s.phase == "write-gap" && cp.Step == "written") ||
		(s.phase == "note-publish-gap" && cp.Step == "published" && cp.Note.Published) || (s.phase == "after-index" && cp.Step == "receipt") {
		os.Exit(73)
	}
	err := s.ClipImportStore.Checkpoint(ctx, id, v, raw)
	if err == nil && ((s.phase == "before-note" && cp.Step == "written" && cp.Note.Identity != (clipFileIdentity{})) || (s.phase == "after-note" && cp.Step == "published" && cp.Note.Published)) {
		os.Exit(73)
	}
	return err
}
func (s crashClipStore) Transition(ctx context.Context, id string, v repository.ClipImportVersion, next repository.ClipImportState, r *model.ClipperImportResult, e *model.APIError) error {
	if next == repository.ClipImportSucceeded && s.phase == "before-receipt" {
		os.Exit(73)
	}
	err := s.ClipImportStore.Transition(ctx, id, v, next, r, e)
	if err == nil && next == repository.ClipImportSucceeded && s.phase == "after-receipt" {
		os.Exit(73)
	}
	return err
}

func TestClipRecoveryCrashChild(t *testing.T) {
	home := os.Getenv("IGONOTES_CLIP_CRASH_HOME")
	if home == "" {
		return
	}
	f := recoveryFixture(t, home)
	base := os.Getenv("IGONOTES_CLIP_CRASH_BASE")
	m := f.manifest(t, base)
	m.ExternalImages = []model.ClipperExternalImage{{URL: "https://example.com/a", Reason: "offline"}, {URL: "https://example.com/b", Reason: "offline"}}
	f.service.store = crashClipStore{f.store, os.Getenv("IGONOTES_CLIP_CRASH_PHASE")}
	if os.Getenv("IGONOTES_CLIP_CRASH_IMAGES") == "yes" {
		part := clipTestPart{"a", "image/png", clipTestImages(t)["image/png"]}
		m.Assets = []model.ClipperAsset{clipDescriptor(part)}
		root, err := os.OpenRoot(home)
		if err != nil {
			t.Fatal(err)
		}
		staged, err := StageClip(t.Context(), root, clipPartsReader(t, m, []clipTestPart{part}, false))
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = f.service.ImportStaged(t.Context(), staged)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		_, _, err := f.service.Import(t.Context(), m)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("crash hook not reached")
}

func crashedRecovery(t *testing.T, phase, base string, images bool) (*clipImportFixture, repository.ClipImportRecord) {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestClipRecoveryCrashChild$")
	imageFlag := "no"
	if images {
		imageFlag = "yes"
	}
	cmd.Env = append(os.Environ(), "IGONOTES_CLIP_CRASH_HOME="+home, "IGONOTES_CLIP_CRASH_PHASE="+phase, "IGONOTES_CLIP_CRASH_BASE="+base, "IGONOTES_CLIP_CRASH_IMAGES="+imageFlag)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("child %v: %s", err, out)
	}
	f := recoveryFixture(t, home)
	r, found, err := f.store.Get(t.Context(), validClipManifest().ImportID)
	if err != nil || !found {
		t.Fatal("missing durable row", err)
	}
	return f, r
}

func TestClipRecoveryCrashRollForward(t *testing.T) {
	for _, images := range []bool{false, true} {
		for _, base := range []string{"active", "target"} {
			for _, phase := range []string{"prepared", "write-gap", "before-note", "note-publish-gap", "after-note", "after-index", "before-receipt", "after-receipt"} {
				t.Run(base+"/"+phase+"/"+map[bool]string{false: "text", true: "images"}[images], func(t *testing.T) {
					f, original := crashedRecovery(t, phase, base, images)
					before, _ := f.index.GetAllNodes()
					if err := f.service.RecoverLocal(t.Context()); err != nil {
						t.Fatal(err)
					}
					r, _, err := f.store.Get(t.Context(), original.ImportID)
					if err != nil || r.State != repository.ClipImportSucceeded || r.Result == nil {
						t.Fatalf("recovery %+v %v", r, err)
					}
					if r.Result.Base != original.BaseName || r.Result.Path != original.NotePath || !strings.HasPrefix(r.Result.Revision, "sha256:") || !reflect.DeepEqual(r.Result.Warnings, []string{"offline", "offline"}) {
						t.Fatal("receipt", r.Result)
					}
					if !reflect.DeepEqual(r.Journal.Plan, original.Journal.Plan) {
						t.Fatal("immutable plan changed")
					}
					after, _ := f.index.GetAllNodes()
					if base == "target" && !reflect.DeepEqual(before, after) {
						t.Fatal("inactive index changed")
					}
					root := f.target
					if base == "active" {
						root = f.active
					}
					data, err := os.ReadFile(filepath.Join(root, r.NotePath))
					if err != nil || clipContentHash(data) != r.NoteContentHash {
						t.Fatal("note", err)
					}
					if err := f.service.RecoverLocal(t.Context()); err != nil {
						t.Fatal(err)
					}
					entries, err := os.ReadDir(root)
					wantEntries := 1
					if images {
						wantEntries = 2
					}
					if err != nil || len(entries) != wantEntries {
						t.Fatal("duplicate/temp artifacts", entries, err)
					}
					if images {
						entries, err := os.ReadDir(filepath.Join(root, "assets/images"))
						if err != nil || len(entries) != 1 {
							t.Fatal("duplicate/temp images", entries, err)
						}
					}
					// Once terminal, deletion or corruption of public files must not cause replay.
					if err := os.Remove(filepath.Join(root, r.NotePath)); err != nil {
						t.Fatal(err)
					}
					if err := f.service.RecoverLocal(t.Context()); err != nil {
						t.Fatal(err)
					}
					again, _, _ := f.store.Get(t.Context(), r.ImportID)
					if !reflect.DeepEqual(r, again) {
						t.Fatal("terminal receipt mutated")
					}
					if _, err := os.Stat(filepath.Join(root, r.NotePath)); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("deleted note recreated")
					}
				})
			}
		}
	}
}

func TestClipRecoveryPreservesAmbiguousOwnership(t *testing.T) {
	for _, scenario := range []string{"create-gap", "edited", "foreign-same-bytes", "deleted", "source-edited", "source-vanished", "wrong-root", "forgotten", "git-conflict", "git-identity"} {
		t.Run(scenario, func(t *testing.T) {
			phase := "before-note"
			if scenario == "create-gap" {
				phase = scenario
			}
			f, r := crashedRecovery(t, phase, "target", true)
			var p clipImagePlan
			json.Unmarshal(r.Journal.Plan, &p)
			name := filepath.Join(f.target, p.Note.TempName)
			var protected []byte
			switch scenario {
			case "edited":
				protected = []byte("user modification")
				if err := os.WriteFile(name, protected, 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign-same-bytes":
				protected = []byte(p.Markdown)
				if err := os.Rename(name, name+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, protected, 0600); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				os.Remove(name)
			case "source-edited":
				os.WriteFile(filepath.Join(f.settings.privateStorage.stagingDir, p.StagingID, p.Assets[0].Source.Name), []byte("changed"), 0600)
			case "source-vanished":
				os.Remove(filepath.Join(f.settings.privateStorage.stagingDir, p.StagingID, p.Assets[0].Source.Name))
			case "wrong-root":
				f.settings.config.Bases[1].Path = t.TempDir()
			case "forgotten":
				f.settings.config.Bases = f.settings.config.Bases[:1]
			case "git-conflict":
				f.settings.coordinator.SetConflict(f.target, true)
			case "git-identity":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(f.target, alias); err != nil {
					t.Skip(err)
				}
				f.settings.coordinator.SetConflict(alias, true)
			}
			if err := f.service.RecoverLocal(t.Context()); err != nil {
				t.Fatal(err)
			}
			r, _, _ = f.store.Get(t.Context(), r.ImportID)
			if r.State != repository.ClipImportRecoveryRequired || r.Error == nil || r.Error.Code != string(model.ClipperRecoveryRequired) {
				t.Fatalf("unsafe state %+v", r)
			}
			if protected != nil {
				data, err := os.ReadFile(name)
				if err != nil || !reflect.DeepEqual(data, protected) {
					t.Fatal("user bytes changed", err)
				}
			}
			if _, err := os.Stat(filepath.Join(f.target, p.Note.Filename)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("published ambiguous note", err)
			}
		})
	}
}

func rewriteRecoveryJournal(t *testing.T, f *clipImportFixture, r repository.ClipImportRecord, plan, checkpoint json.RawMessage) {
	t.Helper()
	raw, err := json.Marshal(repository.ClipImportJournal{Plan: plan, Checkpoint: checkpoint})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("UPDATE clip_imports SET journal_json=? WHERE import_id=?", string(raw), r.ImportID); err != nil {
		t.Fatal(err)
	}
}

func TestClipRecoveryStrictJournal(t *testing.T) {
	for _, images := range []bool{false, true} {
		for _, bad := range []string{"unknown-version", "duplicate", "escaped-duplicate", "unknown-field", "null-warnings", "missing-warnings", "null-warning", "unsafe-temp", "wrong-hash", "wrong-size", "wrong-parent", "wrong-row", "bad-checkpoint", "null-identity", "missing-progress", "unknown-step", "bad-digest", "bad-root"} {
			t.Run(bad+"/"+map[bool]string{false: "text", true: "images"}[images], func(t *testing.T) {
				f, r := crashedRecovery(t, "prepared", "target", images)
				var plan map[string]json.RawMessage
				json.Unmarshal(r.Journal.Plan, &plan)
				var note map[string]any
				json.Unmarshal(plan["note"], &note)
				cp := r.Journal.Checkpoint
				switch bad {
				case "unknown-version":
					plan["version"] = json.RawMessage(`99`)
				case "unknown-field":
					plan["future"] = json.RawMessage(`true`)
				case "null-warnings":
					plan["warnings"] = json.RawMessage(`null`)
				case "missing-warnings":
					delete(plan, "warnings")
				case "null-warning":
					plan["warnings"] = json.RawMessage(`[null]`)
				case "unsafe-temp":
					note["temp_name"] = "../outside.tmp"
				case "wrong-hash":
					note["sha256"] = strings.Repeat("b", 64)
				case "wrong-size":
					note["size"] = 0
				case "wrong-parent":
					note["parent"] = ".git"
				case "wrong-row":
					f.db.Exec("UPDATE clip_imports SET note_path='another.md' WHERE import_id=?", r.ImportID)
				case "bad-checkpoint":
					cp = json.RawMessage(`{"unknown":true}`)
				case "null-identity":
					cp = json.RawMessage(strings.Replace(string(cp), `"identity":{"device":0,"file":0}`, `"identity":null`, 1))
				case "missing-progress":
					cp = json.RawMessage(strings.Replace(string(cp), `"published":false,`, "", 1))
				case "unknown-step":
					cp = json.RawMessage(strings.Replace(string(cp), `"creating"`, `"unknown"`, 1))
				case "bad-digest":
					// Simulate corrupt/imported metadata beyond SQLite's normal CHECK.
					f.db.SetMaxOpenConns(1)
					conn, err := f.db.Conn(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					if _, err = conn.ExecContext(t.Context(), "PRAGMA ignore_check_constraints=ON"); err != nil {
						t.Fatal(err)
					}
					if _, err = conn.ExecContext(t.Context(), "UPDATE clip_imports SET request_digest='broken' WHERE import_id=?", r.ImportID); err != nil {
						t.Fatal(err)
					}
					conn.Close()
				case "bad-root":
					f.db.Exec(`UPDATE clip_imports SET root_identity='{"device":0,"file":0}' WHERE import_id=?`, r.ImportID)
				}
				plan["note"], _ = json.Marshal(note)
				raw, _ := json.Marshal(plan)
				if bad == "duplicate" {
					raw = append([]byte(`{"version":1,`), raw[1:]...)
				}
				if bad == "escaped-duplicate" {
					raw = append([]byte(`{"\u0076ersion":1,`), raw[1:]...)
				}
				rewriteRecoveryJournal(t, f, r, raw, cp)
				persisted, _, err := f.store.Get(t.Context(), r.ImportID)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.service.RecoverLocal(t.Context()); err != nil {
					t.Fatal(err)
				}
				got, _, _ := f.store.Get(t.Context(), r.ImportID)
				if got.State != repository.ClipImportRecoveryRequired || !reflect.DeepEqual(got.Journal, persisted.Journal) {
					t.Fatal("invalid evidence mutated", got.State)
				}
				entries, _ := os.ReadDir(f.target)
				if len(entries) != 0 {
					t.Fatal("invalid journal wrote files", entries)
				}
			})
		}
	}
}

func TestClipRecoveryMovedRootReboundSettings(t *testing.T) {
	f, r := crashedRecovery(t, "before-note", "target", true)
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(f.target, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := f.settings.UpdateBase("target", model.BaseUpdateRequest{Name: "renamed", Path: moved}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.RecoverLocal(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _, _ := f.store.Get(t.Context(), r.ImportID)
	if got.State != repository.ClipImportSucceeded || got.Result.Base != "target" || got.BasePath != r.BasePath {
		t.Fatal("historical destination lost", got)
	}
	if _, err := os.Stat(filepath.Join(moved, r.NotePath)); err != nil {
		t.Fatal(err)
	}
	if f.notes.GetBasePath() != f.active {
		t.Fatal("runtime switched")
	}
}

func TestClipRecoveryIndexAndReceiptFailures(t *testing.T) {
	for _, boundary := range []string{"index", "checkpoint", "receipt", "lost-receipt", "index-db", "cancel-index-db"} {
		t.Run(boundary, func(t *testing.T) {
			f, r := crashedRecovery(t, "after-note", "active", true)
			failure := errors.New("injected private failure")
			ctx := t.Context()
			var release func()
			if boundary == "index" {
				f.notes.scan = func(*os.Root) ([]model.NoteNode, error) { return nil, failure }
			}
			if boundary == "index-db" {
				f.notes.repo = &failedRecoveryIndex{noteRepository: f.index, err: failure}
			}
			if boundary == "cancel-index-db" {
				// Saturate the real pool only after scanning, so journal reads and
				// checkpoints work and cancellation occurs inside index preparation.
				ctx, release = context.WithTimeout(ctx, 150*time.Millisecond)
				defer release()
				f.db.SetMaxOpenConns(1)
				f.notes.scan = func(root *os.Root) ([]model.NoteNode, error) {
					nodes, err := scanNotes(root)
					conn, e := f.db.Conn(t.Context())
					if e != nil {
						t.Fatal(e)
					}
					t.Cleanup(func() { conn.Close() })
					return nodes, err
				}
			}
			hook := &hookedClipImportStore{ClipImportStore: f.store}
			if boundary == "checkpoint" {
				hook.checkpoint = func(json.RawMessage) error { return failure }
			}
			if boundary == "receipt" {
				hook.transition = func(next repository.ClipImportState) error {
					if next == repository.ClipImportSucceeded {
						return failure
					}
					return nil
				}
			}
			f.service.store = hook
			if boundary == "lost-receipt" {
				f.service.store = &lostRecoveryReceipt{ClipImportStore: f.store, err: failure}
			}
			err := f.service.RecoverLocal(ctx)
			if boundary == "index" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("persistence/cancellation error swallowed")
			}
			if boundary == "cancel-index-db" {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
				return
			}
			got, _, _ := f.store.Get(t.Context(), r.ImportID)
			if boundary == "index" && got.State != repository.ClipImportRecoveryRequired {
				t.Fatal(got.State)
			}
			if boundary == "lost-receipt" && got.State != repository.ClipImportSucceeded {
				t.Fatal("lost durable receipt")
			}
			if _, err := os.Stat(filepath.Join(f.active, r.NotePath)); err != nil {
				t.Fatal("public rollback", err)
			}
			// A fresh runtime can retry without the injection, including a prior
			// recovery_required outcome and an unacknowledged durable receipt.
			f.notes.scan = scanNotes
			f.notes.repo = f.index
			f.service.store = f.store
			if err := f.service.RecoverLocal(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, _, _ = f.store.Get(t.Context(), r.ImportID)
			if got.State != repository.ClipImportSucceeded {
				t.Fatal(got.State)
			}
		})
	}
}

type failedRecoveryIndex struct {
	noteRepository
	err error
}

func (r *failedRecoveryIndex) BeginReplaceAll([]model.NoteNode) (func() error, func() error, error, error) {
	return nil, nil, r.err, nil
}

type lostRecoveryReceipt struct {
	ClipImportStore
	err error
}

func (s *lostRecoveryReceipt) Transition(ctx context.Context, id string, v repository.ClipImportVersion, next repository.ClipImportState, r *model.ClipperImportResult, e *model.APIError) error {
	err := s.ClipImportStore.Transition(ctx, id, v, next, r, e)
	if err == nil && next == repository.ClipImportSucceeded {
		return s.err
	}
	return err
}

func TestClipRecoveryCheckpointBeforeDirectoryEffects(t *testing.T) {
	f, _ := crashedRecovery(t, "prepared", "target", true)
	f.service.store = &hookedClipImportStore{ClipImportStore: f.store, checkpoint: func(json.RawMessage) error { return errors.New("DB unavailable") }}
	if err := f.service.RecoverLocal(t.Context()); err == nil {
		t.Fatal("lost DB failure")
	}
	entries, _ := os.ReadDir(f.target)
	if len(entries) != 0 {
		t.Fatal("directory effects before recovery checkpoint", entries)
	}
}

func TestClipRecoveryUnknownPrivateEntriesAndTerminalSkip(t *testing.T) {
	f, r := crashedRecovery(t, "before-note", "target", true)
	var p clipImagePlan
	json.Unmarshal(r.Journal.Plan, &p)
	foreign := filepath.Join(f.settings.privateStorage.stagingDir, p.StagingID, "foreign")
	if err := os.WriteFile(foreign, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.service.RecoverLocal(t.Context()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "keep" {
		t.Fatal("foreign private evidence deleted", err)
	}
	// Terminal plans, including legacy/unknown ones, are never validated/repaired.
	r, _, _ = f.store.Get(t.Context(), r.ImportID)
	rewriteRecoveryJournal(t, f, r, json.RawMessage(`{"version":99}`), r.Journal.Checkpoint)
	if err := os.Remove(filepath.Join(f.target, r.NotePath)); err != nil {
		t.Fatal(err)
	}
	if err := f.service.RecoverLocal(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _, _ := f.store.Get(t.Context(), r.ImportID)
	if !reflect.DeepEqual(got.Result, r.Result) {
		t.Fatal("terminal result rewritten")
	}
}

func TestClipRecoverySlowJournalDBCancellation(t *testing.T) {
	f, r := crashedRecovery(t, "prepared", "target", false)
	f.db.SetMaxOpenConns(1)
	conn, err := f.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err = f.service.RecoverLocal(ctx)
	conn.Close()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation %v", err)
	}
	got, _, _ := f.store.Get(t.Context(), r.ImportID)
	if !reflect.DeepEqual(r, got) {
		t.Fatal("canceled IO fabricated outcome")
	}
}

func TestClipRecoveryContinuesOtherRoots(t *testing.T) {
	f, bad := crashedRecovery(t, "prepared", "target", false)
	rewriteRecoveryJournal(t, f, bad, json.RawMessage(`{"version":99}`), bad.Journal.Checkpoint)
	m := f.manifest(t, "active")
	m.ImportID = "98765432-1234-4567-8901-123456789abc"
	// A real prepared claim with no files, independent of the bad root.
	root, err := os.OpenRoot(f.active)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := prepareClipNote(root, m.Parent, m.Filename, m.Markdown)
	if err != nil {
		t.Fatal(err)
	}
	defer file.close()
	good := bad
	good.ImportID = m.ImportID
	good.BaseName = "active"
	good.BasePath = f.active
	dir, _ := root.Open(".")
	id, _ := clipIdentity(dir)
	dir.Close()
	raw, _ := json.Marshal(id)
	good.RootIdentity = string(raw)
	good.Journal.Plan, _ = json.Marshal(clipImportPlan{Version: 1, Note: file.plan, Markdown: m.Markdown, Warnings: clipImportPlanWarnings(m)})
	if _, _, err := f.store.Claim(t.Context(), good); err != nil {
		t.Fatal(err)
	}
	if err := f.service.RecoverLocal(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _, _ := f.store.Get(t.Context(), good.ImportID)
	if got.State != repository.ClipImportSucceeded {
		t.Fatal("other root blocked", got.State)
	}
}

func TestClipRecoveryAdoptedFilesSyncedBeforeReceipt(t *testing.T) {
	for _, phase := range []string{"before-note", "note-publish-gap"} {
		t.Run(phase, func(t *testing.T) {
			f, r := crashedRecovery(t, phase, "target", false)
			f.service.prepareRecovery = func(root *os.Root, plan clipNotePlan) (*clipNoteFile, error) {
				file, err := prepareClipFile(root, plan)
				if err != nil {
					return nil, err
				}
				file.ops.sync = func(*os.File) error { return errors.New("durability failed") }
				return file, nil
			}
			if err := f.service.RecoverLocal(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, _, _ := f.store.Get(t.Context(), r.ImportID)
			if got.State != repository.ClipImportRecoveryRequired {
				t.Fatal("receipt without fsync", got.State)
			}
		})
	}
}

func TestClipRecoveryRenameUncertaintyRetainsEvidence(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "not-renamed", true: "lost-ack"}[completed], func(t *testing.T) {
			f, r := crashedRecovery(t, "before-note", "target", true)
			f.service.prepareRecovery = func(root *os.Root, plan clipNotePlan) (*clipNoteFile, error) {
				file, err := prepareClipFile(root, plan)
				if err != nil {
					return nil, err
				}
				if strings.HasSuffix(plan.Filename, ".md") {
					file.ops.rename = func(dir *os.File, from, to string) error {
						if completed {
							if err := renameNoReplace(dir, from, to); err != nil {
								return err
							}
						}
						return os.ErrExist
					}
				}
				return file, nil
			}
			if err := f.service.RecoverLocal(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, _, _ := f.store.Get(t.Context(), r.ImportID)
			var cp clipImageCheckpoint
			json.Unmarshal(got.Journal.Checkpoint, &cp)
			if got.State != repository.ClipImportRecoveryRequired || !cp.Note.PublicationUncertain {
				t.Fatal("rename uncertainty lost", got.State, cp)
			}
			var p clipImagePlan
			json.Unmarshal(got.Journal.Plan, &p)
			if _, err := os.Stat(filepath.Join(f.target, "assets/images", p.Assets[0].File.Filename)); err != nil {
				t.Fatal("late asset rollback", err)
			}
			f.service.prepareRecovery = prepareClipFile
			if err := f.service.RecoverLocal(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, _, _ = f.store.Get(t.Context(), r.ImportID)
			if got.State != repository.ClipImportSucceeded {
				t.Fatal("uncertain rename not reconciled", got.State)
			}
		})
	}
}

func TestClipRecoveryPartialCheckpointedOwnTemp(t *testing.T) {
	f, r := crashedRecovery(t, "write-gap", "target", false)
	var p clipImportPlan
	json.Unmarshal(r.Journal.Plan, &p)
	var cp clipImportCheckpoint
	json.Unmarshal(r.Journal.Checkpoint, &cp)
	data := []byte(p.Markdown[:len(p.Markdown)/2])
	if err := os.WriteFile(filepath.Join(f.target, p.Note.TempName), data, 0600); err != nil {
		t.Fatal(err)
	}
	cp.Note.SHA256 = clipContentHash(data)
	cp.Note.Size = int64(len(data))
	raw, _ := json.Marshal(cp)
	rewriteRecoveryJournal(t, f, r, r.Journal.Plan, raw)
	if err := f.service.RecoverLocal(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _, _ := f.store.Get(t.Context(), r.ImportID)
	if got.State != repository.ClipImportSucceeded {
		t.Fatal(got.State)
	}
	actual, err := os.ReadFile(filepath.Join(f.target, r.NotePath))
	if err != nil || string(actual) != p.Markdown {
		t.Fatal("partial recovery", err)
	}
}

func TestClipRecoveryRejectsImpossibleCheckpointBoundaries(t *testing.T) {
	for _, bad := range []string{"indexed-before-note", "complete-wrong-hash", "empty-wrong-hash", "published-and-uncertain", "receipt-before-assets", "duplicate-native-identity"} {
		t.Run(bad, func(t *testing.T) {
			f, r := crashedRecovery(t, "before-note", "target", true)
			var cp clipImageCheckpoint
			json.Unmarshal(r.Journal.Checkpoint, &cp)
			switch bad {
			case "indexed-before-note":
				cp.Indexed = true
			case "complete-wrong-hash":
				cp.Note.SHA256 = strings.Repeat("b", 64)
			case "empty-wrong-hash":
				cp.Note.Size = 0
				cp.Note.SHA256 = strings.Repeat("b", 64)
			case "published-and-uncertain":
				cp.Note.Published = true
				cp.Note.PublicationUncertain = true
			case "receipt-before-assets":
				cp.Step = "receipt"
			case "duplicate-native-identity":
				cp.Assets[0].Identity = cp.Note.Identity
			}
			raw, _ := json.Marshal(cp)
			rewriteRecoveryJournal(t, f, r, r.Journal.Plan, raw)
			persisted, _, _ := f.store.Get(t.Context(), r.ImportID)
			if err := f.service.RecoverLocal(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, _, _ := f.store.Get(t.Context(), r.ImportID)
			if got.State != repository.ClipImportRecoveryRequired || !reflect.DeepEqual(got.Journal, persisted.Journal) {
				t.Fatal("impossible checkpoint adopted", got.State)
			}
		})
	}
}

func TestClipRecoveryPrivateCleanupRejectsAliasOrForeignInode(t *testing.T) {
	for _, kind := range []string{"alias", "source-replaced"} {
		t.Run(kind, func(t *testing.T) {
			f, r := crashedRecovery(t, "before-note", "target", true)
			var p clipImagePlan
			json.Unmarshal(r.Journal.Plan, &p)
			parent, err := os.OpenRoot(f.settings.privateStorage.stagingDir)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			root, err := parent.OpenRoot(p.StagingID)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if kind == "alias" {
				if err := parent.Rename(p.StagingID, p.StagingID+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(p.StagingID+"-original", filepath.Join(f.settings.privateStorage.stagingDir, p.StagingID)); err != nil {
					t.Skip(err)
				}
			} else {
				a := p.Assets[0]
				data, err := root.ReadFile(a.Source.Name)
				if err != nil {
					t.Fatal(err)
				}
				if err := root.Rename(a.Source.Name, a.Source.Name+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := root.WriteFile(a.Source.Name, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			cleanupClipRecoverySources(parent, root, p)
			if _, err := parent.Lstat(p.StagingID); err != nil {
				t.Fatal("foreign alias/directory deleted", err)
			}
			if _, err := root.Stat(p.Assets[0].Source.Name); err != nil {
				t.Fatal("private evidence deleted", err)
			}
		})
	}
}

func TestClipRecoveryMalformedEnvelopeIsPerRecord(t *testing.T) {
	for _, kind := range []string{"malformed-plan", "duplicate-plan", "unknown-key", "null-checkpoint", "malformed-checkpoint-type"} {
		t.Run(kind, func(t *testing.T) {
			f, r := crashedRecovery(t, "prepared", "target", false)
			encoded, _ := json.Marshal(r.Journal)
			raw := string(encoded)
			switch kind {
			case "malformed-plan":
				raw = `{"plan":[],"checkpoint":{}}`
			case "duplicate-plan":
				raw = `{"plan":` + string(r.Journal.Plan) + `,` + raw[1:]
			case "unknown-key":
				raw = `{"future":true,` + raw[1:]
			case "null-checkpoint":
				raw = `{"plan":` + string(r.Journal.Plan) + `,"checkpoint":null}`
			case "malformed-checkpoint-type":
				raw = `{"plan":` + string(r.Journal.Plan) + `,"checkpoint":"broken"}`
			}
			if _, err := f.db.Exec("UPDATE clip_imports SET journal_json=? WHERE import_id=?", raw, r.ImportID); err != nil {
				t.Fatal(err)
			}
			if err := f.service.RecoverLocal(t.Context()); err != nil {
				t.Fatal("data error aborted recovery", err)
			}
			var state, evidence string
			if err := f.db.QueryRow("SELECT state,journal_json FROM clip_imports WHERE import_id=?", r.ImportID).Scan(&state, &evidence); err != nil {
				t.Fatal(err)
			}
			if state != "recovery_required" || evidence != raw {
				t.Fatal("invalid envelope changed", state, evidence)
			}
		})
	}
}

func TestClipRecoveryPublishedUserChangesNeverDeleted(t *testing.T) {
	for _, kind := range []string{"note-edited", "asset-edited", "asset-foreign-identical", "asset-deleted"} {
		t.Run(kind, func(t *testing.T) {
			f, r := crashedRecovery(t, "note-publish-gap", "target", true)
			var p clipImagePlan
			json.Unmarshal(r.Journal.Plan, &p)
			name := filepath.Join(f.target, r.NotePath)
			if kind != "note-edited" {
				name = filepath.Join(f.target, "assets/images", p.Assets[0].File.Filename)
			}
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "asset-foreign-identical" {
				if err := os.Rename(name, name+".original"); err != nil {
					t.Fatal(err)
				}
			} else {
				data = []byte("user changed")
			}
			if kind == "asset-deleted" {
				os.Remove(name)
			} else {
				if err := os.WriteFile(name, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := f.service.RecoverLocal(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			got, _, _ := f.store.Get(t.Context(), r.ImportID)
			if got.State != repository.ClipImportRecoveryRequired {
				t.Fatal(got.State)
			}
			actual, err := os.ReadFile(name)
			if kind == "asset-deleted" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatal("deleted dependency recreated", err)
				}
			} else if err != nil || !reflect.DeepEqual(actual, data) {
				t.Fatal("modified published file deleted/changed", err)
			}
			if _, err := os.Stat(filepath.Join(f.target, r.NotePath)); err != nil {
				t.Fatal("late note rollback", err)
			}
		})
	}
}

func TestClipRecoveryRechecksAssetsBeforeNotePublication(t *testing.T) {
	f, r := crashedRecovery(t, "prepared", "target", true)
	var p clipImagePlan
	json.Unmarshal(r.Journal.Plan, &p)
	changed := false
	f.service.store = &hookedClipImportStore{ClipImportStore: f.store, checkpoint: func(raw json.RawMessage) error {
		var cp clipImageCheckpoint
		json.Unmarshal(raw, &cp)
		if !changed && cp.Step == "published" && cp.Assets[0].Published && !cp.Note.Published {
			changed = true
			if err := os.WriteFile(filepath.Join(f.target, "assets/images", p.Assets[0].File.Filename), []byte("user edit"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}}
	if err := f.service.RecoverLocal(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("boundary not reached")
	}
	if _, err := os.Stat(filepath.Join(f.target, r.NotePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("note published with changed dependency", err)
	}
	got, _, _ := f.store.Get(t.Context(), r.ImportID)
	if got.State != repository.ClipImportRecoveryRequired {
		t.Fatal(got.State)
	}
}
