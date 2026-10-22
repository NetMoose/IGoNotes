package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

func TestClipImagesNestedDedupDurablePlan(t *testing.T) {
	f := newClipImportFixture(t)
	policy, err := NewPrivateStoragePolicy(t.TempDir(), f.dbPath, filepath.Join(t.TempDir(), "staging"))
	if err != nil {
		t.Fatal(err)
	}
	f.settings.privateStorage = policy
	m := f.manifest(t, "target")
	m.Parent = "research/articles"
	if err := os.MkdirAll(filepath.Join(f.target, m.Parent), 0700); err != nil {
		t.Fatal(err)
	}
	data := clipTestImages(t)["image/png"]
	parts := []clipTestPart{{"a", "image/png", data}, {"b", "image/png", data}}
	m.Assets = []model.ClipperAsset{clipDescriptor(parts[0]), clipDescriptor(parts[1])}
	name := "clip-" + m.ImportID + "-" + m.Assets[0].SHA256 + ".png"
	m.Markdown = "![a](../../assets/images/" + name + ")\n![b](../../assets/images/" + name + ")\n"
	borrowed, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer borrowed.Close()
	staged, err := StageClip(t.Context(), borrowed, clipPartsReader(t, m, parts, false))
	if err != nil {
		t.Fatal(err)
	}
	var importer interface {
		ImportStaged(context.Context, *StagedClip) (model.ClipperImportStatusResponse, bool, error)
	} = nil
	importer, ok := any(f.service).(interface {
		ImportStaged(context.Context, *StagedClip) (model.ClipperImportStatusResponse, bool, error)
	})
	if !ok {
		t.Fatal("actual service does not support validated images")
	}
	status, replay, err := importer.ImportStaged(t.Context(), staged)
	if err != nil || replay || status.State != model.ClipperImportSucceeded {
		t.Fatalf("import: %+v %v %v", status, replay, err)
	}
	got, err := os.ReadFile(filepath.Join(f.target, "assets/images", name))
	if err != nil || string(got) != string(data) {
		t.Fatalf("image: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(f.target, "assets/images"))
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	note, err := os.ReadFile(filepath.Join(f.target, m.Parent, m.Filename))
	if err != nil || string(note) != m.Markdown {
		t.Fatal("markdown changed", err)
	}
	record, _, err := f.store.Get(t.Context(), m.ImportID)
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Version int
		Assets  []json.RawMessage
	}
	if err := json.Unmarshal(record.Journal.Plan, &plan); err != nil || plan.Version != 2 || len(plan.Assets) != 1 {
		t.Fatalf("plan: %s %v", record.Journal.Plan, err)
	}
	borrowed.Close()
	again, replay, err := importer.ImportStaged(t.Context(), staged)
	if err != nil || !replay || again.State != status.State {
		t.Fatalf("replay: %+v %v", again, err)
	}
}

func TestClipImagesCollisionSymlinkAndChangedOwnership(t *testing.T) {
	for _, scenario := range []string{"existing", "identical", "symlink-assets", "symlink-images", "concurrent", "changed", "uncertain"} {
		t.Run(scenario, func(t *testing.T) {
			f, staged := stagedPublicationFixture(t)
			expected := "foreign bytes"
			a := staged.Manifest.Assets[0]
			if scenario == "identical" {
				input, err := staged.OpenAsset(a.ID)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(input)
				input.Close()
				if err != nil {
					t.Fatal(err)
				}
				expected = string(data)
			}
			name := "clip-" + staged.Manifest.ImportID + "-" + a.SHA256 + "." + clipImageExtension(a.MediaType)
			dir := filepath.Join(f.target, "assets/images")
			if scenario == "existing" || scenario == "identical" {
				os.MkdirAll(dir, 0700)
				os.WriteFile(filepath.Join(dir, name), []byte(expected), 0600)
			}
			if scenario == "symlink-assets" {
				if err := os.Symlink(t.TempDir(), filepath.Join(f.target, "assets")); err != nil {
					t.Skip(err)
				}
			}
			if scenario == "symlink-images" {
				os.Mkdir(filepath.Join(f.target, "assets"), 0700)
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Skip(err)
				}
			}
			hook := &hookedClipImportStore{ClipImportStore: f.store}
			count := 0
			hook.checkpoint = func(raw json.RawMessage) error {
				var cp clipImageCheckpoint
				json.Unmarshal(raw, &cp)
				if cp.Step == "written" {
					count++
					if count == 5 && scenario == "concurrent" {
						if err := os.WriteFile(filepath.Join(dir, name), []byte(expected), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				if cp.Step == "published" && (scenario == "changed" || scenario == "uncertain") {
					record, _, _ := f.store.Get(t.Context(), staged.Manifest.ImportID)
					var p clipImagePlan
					json.Unmarshal(record.Journal.Plan, &p)
					name = p.Assets[0].File.Filename
					if scenario == "changed" {
						if err := os.WriteFile(filepath.Join(dir, name), []byte(expected), 0600); err != nil {
							t.Fatal(err)
						}
					}
					return errors.New("checkpoint failure")
				}
				return nil
			}
			f.service.store = hook
			status, _, err := f.service.ImportStaged(t.Context(), staged)
			switch scenario {
			case "existing", "identical":
				requireClipperDestinationCode(t, err, model.ClipperNoteConflict)
			case "symlink-assets", "symlink-images":
				if err == nil {
					t.Fatalf("accepted symlink: %+v", status)
				}
			case "concurrent", "changed":
				if err != nil || status.State != model.ClipperImportRecoveryRequired {
					t.Fatalf("unsafe result: %+v %v", status, err)
				}
			case "uncertain":
				if err != nil || status.State != model.ClipperImportRejected {
					t.Fatalf("safe rollback: %+v %v", status, err)
				}
			}
			if scenario == "existing" || scenario == "identical" || scenario == "concurrent" || scenario == "changed" {
				data, e := os.ReadFile(filepath.Join(dir, name))
				if e != nil || string(data) != expected {
					t.Fatal("changed foreign file", e)
				}
			}
		})
	}
}

func TestClipImagesConcurrentIdenticalID(t *testing.T) {
	f, staged := stagedPublicationFixture(t)
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			status, _, err := f.service.ImportStaged(t.Context(), staged)
			if err != nil || (status.State != model.ClipperImportSucceeded && status.State != model.ClipperImportPending) {
				t.Errorf("import %+v %v", status, err)
			}
		})
	}
	wg.Wait()
	entries, err := os.ReadDir(filepath.Join(f.target, "assets/images"))
	if err != nil || len(entries) != 4 {
		t.Fatal("duplicates", entries, err)
	}
	entries, err = os.ReadDir(f.settings.privateStorage.stagingDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("staging leak", entries, err)
	}
}

func TestClipBinaryFileOperationFailures(t *testing.T) {
	for _, boundary := range []string{"write", "close", "sync", "rename", "syncParent"} {
		t.Run(boundary, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			data := []byte{0, 255, 254, 0}
			plan := clipNotePlan{Filename: "image.png", TempName: "owned.tmp", Size: 4, SHA256: clipContentHash(data)}
			f, err := prepareClipBinary(root, plan, func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(string(data))), nil })
			if err != nil {
				t.Fatal(err)
			}
			defer f.close()
			failure := errors.New("injected")
			switch boundary {
			case "write":
				f.ops.write = func(*os.File, []byte) (int, error) { return 0, failure }
			case "close":
				f.ops.close = func(*os.File) error { return failure }
			case "sync":
				f.ops.sync = func(*os.File) error { return failure }
			case "rename":
				f.ops.rename = func(*os.File, string, string) error { return os.ErrExist }
			case "syncParent":
				f.ops.syncParent = func(*os.File) error { return failure }
			}
			if _, err = f.create(); err != nil {
				t.Fatal(err)
			}
			_, err = f.write()
			if boundary == "rename" || boundary == "syncParent" {
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.publish()
			}
			if err == nil {
				t.Fatal("injection ignored")
			}
			if boundary == "rename" {
				if !f.progress.PublicationUncertain || f.discardTemp() == nil {
					t.Fatal("rename uncertainty lost")
				}
			} else {
				f.ops.syncParent = syncClipDirectory
				if err = f.rollbackOwned(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestClipImagesNoteAndIndexFailuresRetainSources(t *testing.T) {
	for _, boundary := range []string{"write", "close", "sync", "rename", "syncParent", "index"} {
		t.Run(boundary, func(t *testing.T) {
			f, staged := stagedPublicationFixture(t)
			if boundary == "index" {
				staged.Manifest.Base = "active"
				staged.Manifest.DestinationRevision = issueClipperDestination(t, f.dest, "active")
				f.notes.scan = func(*os.Root) ([]model.NoteNode, error) { return nil, errors.New("index failure") }
			}
			f.service.prepare = func(root *os.Root, parent, filename, markdown string) (*clipNoteFile, error) {
				file, err := prepareClipNote(root, parent, filename, markdown)
				if err != nil {
					return nil, err
				}
				failure := errors.New("injected note failure")
				switch boundary {
				case "write":
					file.ops.write = func(*os.File, []byte) (int, error) { return 0, failure }
				case "close":
					file.ops.close = func(*os.File) error { return failure }
				case "sync":
					file.ops.sync = func(*os.File) error { return failure }
				case "rename":
					file.ops.rename = func(*os.File, string, string) error { return failure }
				case "syncParent":
					file.ops.syncParent = func(*os.File) error { return failure }
				}
				return file, nil
			}
			status, _, err := f.service.ImportStaged(t.Context(), staged)
			if err != nil {
				t.Fatal(err)
			}
			retain := boundary == "rename" || boundary == "syncParent" || boundary == "index"
			want := model.ClipperImportRejected
			if retain {
				want = model.ClipperImportRecoveryRequired
			}
			if status.State != want {
				t.Fatalf("%+v want %s", status, want)
			}
			record, _, _ := f.store.Get(t.Context(), staged.Manifest.ImportID)
			var p clipImagePlan
			json.Unmarshal(record.Journal.Plan, &p)
			staged.root.Close()
			for _, a := range p.Assets {
				data, e := os.ReadFile(filepath.Join(f.settings.privateStorage.stagingDir, p.StagingID, a.Source.Name))
				if retain {
					if e != nil || clipContentHash(data) != a.File.SHA256 {
						t.Fatal("source lost", e)
					}
				} else if !errors.Is(e, os.ErrNotExist) {
					t.Fatal("source leaked", e)
				}
			}
		})
	}
}

type lostImageClaimStore struct {
	ClipImportStore
	unreadable  bool
	claimed     bool
	unknownPlan bool
}

func (s *lostImageClaimStore) Claim(ctx context.Context, r repository.ClipImportRecord) (repository.ClipImportRecord, bool, error) {
	record, created, err := s.ClipImportStore.Claim(ctx, r)
	if err != nil {
		return record, created, err
	}
	s.claimed = true
	return repository.ClipImportRecord{}, false, errors.New("lost claim acknowledgement")
}
func (s *lostImageClaimStore) Get(ctx context.Context, id string) (repository.ClipImportRecord, bool, error) {
	if s.claimed && s.unreadable {
		return repository.ClipImportRecord{}, false, errors.New("lookup unavailable")
	}
	if s.claimed && s.unknownPlan {
		r, found, err := s.ClipImportStore.Get(ctx, id)
		r.Journal.Plan = json.RawMessage(`{"version":99}`)
		return r, found, err
	}
	return s.ClipImportStore.Get(ctx, id)
}

func TestClipImagesLostClaimAcknowledgementPreservesOwnedBytes(t *testing.T) {
	for _, mode := range []string{"readable", "unreadable", "unknown schema"} {
		t.Run(mode, func(t *testing.T) {
			f, staged := stagedPublicationFixture(t)
			f.service.store = &lostImageClaimStore{ClipImportStore: f.store, unreadable: mode == "unreadable", unknownPlan: mode == "unknown schema"}
			_, _, err := f.service.ImportStaged(t.Context(), staged)
			requireClipperDestinationCode(t, err, model.ClipperRecoveryRequired)
			record, found, err := f.store.Get(t.Context(), staged.Manifest.ImportID)
			if err != nil || !found {
				t.Fatal("claim missing", err)
			}
			var p clipImagePlan
			json.Unmarshal(record.Journal.Plan, &p)
			staged.root.Close()
			for _, a := range p.Assets {
				data, e := os.ReadFile(filepath.Join(f.settings.privateStorage.stagingDir, p.StagingID, a.Source.Name))
				if e != nil || clipContentHash(data) != a.File.SHA256 {
					t.Fatal("claimed source deleted", e)
				}
			}
			if _, err := os.Stat(filepath.Join(f.target, "assets")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("base changed after uncertain claim", err)
			}
		})
	}
}

func TestClipImagesCanceledAndBrokenCopyCleansOnlyOwnedSources(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint(cancel), func(t *testing.T) {
			f, staged := stagedPublicationFixture(t)
			reserved, err := f.settings.OpenPrivateStagingRoot()
			if err != nil {
				t.Fatal(err)
			}
			defer reserved.Close()
			if err := reserved.WriteFile("foreign", []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			if cancel {
				stop()
			} else {
				for _, a := range staged.Assets {
					staged.root.Remove(a.Name)
					break
				}
			}
			_, _, err = f.service.ImportStaged(ctx, staged)
			if err == nil {
				t.Fatal("bad copy accepted")
			}
			entries, err := os.ReadDir(f.settings.privateStorage.stagingDir)
			if err != nil || len(entries) != 1 || entries[0].Name() != "foreign" {
				t.Fatal("private cleanup ownership", entries, err)
			}
		})
	}
}

func TestClipImagesClaimDigestCollisionKeepsWinnerSources(t *testing.T) {
	f, staged := stagedPublicationFixture(t)
	winner := []byte("winner-owned-source")
	var winnerDir string
	hook := &hookedClipImportStore{ClipImportStore: f.store, claim: func(record repository.ClipImportRecord) error {
		root, err := os.OpenRoot(f.settings.privateStorage.stagingDir)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		winnerDir = "owned-winner"
		if err := root.Mkdir(winnerDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := root.WriteFile(winnerDir+"/source-winner", winner, 0600); err != nil {
			t.Fatal(err)
		}
		var p clipImagePlan
		json.Unmarshal(record.Journal.Plan, &p)
		p.StagingID = winnerDir
		record.Journal.Plan, _ = json.Marshal(p)
		record.RequestDigest = strings.Repeat("b", 64)
		_, _, err = f.store.Claim(t.Context(), record)
		return err
	}}
	f.service.store = hook
	_, _, err := f.service.ImportStaged(t.Context(), staged)
	requireClipperDestinationCode(t, err, model.ClipperRequestChanged)
	entries, e := os.ReadDir(f.settings.privateStorage.stagingDir)
	if e != nil || len(entries) != 1 || entries[0].Name() != winnerDir {
		t.Fatal("loser cleanup touched winner or leaked sources", entries, e)
	}
	actual, e := os.ReadFile(filepath.Join(f.settings.privateStorage.stagingDir, winnerDir, "source-winner"))
	if e != nil || string(actual) != string(winner) {
		t.Fatal("winner source deleted", e)
	}
}

func stagedPublicationFixture(t *testing.T) (*clipImportFixture, *StagedClip) {
	t.Helper()
	f := newClipImportFixture(t)
	policy, err := NewPrivateStoragePolicy(t.TempDir(), f.dbPath, filepath.Join(t.TempDir(), "staging"))
	if err != nil {
		t.Fatal(err)
	}
	f.settings.privateStorage = policy
	m := f.manifest(t, "target")
	var parts []clipTestPart
	for media, data := range clipTestImages(t) {
		parts = append(parts, clipTestPart{clipImageExtension(media), media, data})
	}
	m.Assets = nil
	for _, p := range parts {
		m.Assets = append(m.Assets, clipDescriptor(p))
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	staged, err := StageClip(t.Context(), root, clipPartsReader(t, m, parts, false))
	if err != nil {
		t.Fatal(err)
	}
	return f, staged
}

func TestClipBinarySourceClosePreventsPublication(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	data := []byte{0, 255, 128, 1}
	plan := clipNotePlan{Filename: "binary.png", TempName: "binary.tmp", Size: int64(len(data)), SHA256: clipContentHash(data)}
	f, err := prepareClipBinary(root, plan, func() (io.ReadCloser, error) { return &cancelAfterRead{data: data}, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	if _, err = f.create(); err != nil {
		t.Fatal(err)
	}
	if _, err = f.write(); err == nil {
		t.Fatal("source close failure must prevent publication")
	}
}

func TestClipImagesEachAssetOperationFailure(t *testing.T) {
	for asset := 0; asset < 4; asset++ {
		for _, boundary := range []string{"write", "close", "sync", "rename", "lost rename", "syncParent"} {
			t.Run(fmt.Sprintf("%d/%s", asset, boundary), func(t *testing.T) {
				f, staged := stagedPublicationFixture(t)
				count := 0
				f.service.prepareBinary = func(root *os.Root, plan clipNotePlan, source func() (io.ReadCloser, error)) (*clipNoteFile, error) {
					file, err := prepareClipBinary(root, plan, source)
					if err != nil {
						return nil, err
					}
					i := count
					count++
					if i != asset {
						return file, nil
					}
					failure := errors.New("injected asset operation")
					switch boundary {
					case "write":
						file.ops.write = func(*os.File, []byte) (int, error) { return 0, failure }
					case "close":
						file.ops.close = func(*os.File) error { return failure }
					case "sync":
						file.ops.sync = func(*os.File) error { return failure }
					case "rename":
						file.ops.rename = func(*os.File, string, string) error { return os.ErrExist }
					case "lost rename":
						file.ops.rename = func(dir *os.File, old, next string) error {
							if err := renameNoReplace(dir, old, next); err != nil {
								return err
							}
							return failure
						}
					case "syncParent":
						file.ops.syncParent = func(*os.File) error { return failure }
					}
					return file, nil
				}
				status, _, err := f.service.ImportStaged(t.Context(), staged)
				if err != nil {
					t.Fatal(err)
				}
				retain := boundary == "rename" || boundary == "lost rename" || boundary == "syncParent"
				want := model.ClipperImportRejected
				if retain {
					want = model.ClipperImportRecoveryRequired
				}
				if status.State != want {
					t.Fatalf("result %+v want %s", status, want)
				}
				if _, err := os.Stat(filepath.Join(f.target, staged.Manifest.Filename)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("note published after asset error", err)
				}
				record, _, _ := f.store.Get(t.Context(), staged.Manifest.ImportID)
				var p clipImagePlan
				json.Unmarshal(record.Journal.Plan, &p)
				staged.root.Close()
				for _, a := range p.Assets {
					data, e := os.ReadFile(filepath.Join(f.settings.privateStorage.stagingDir, p.StagingID, a.Source.Name))
					if retain {
						if e != nil || clipContentHash(data) != a.File.SHA256 {
							t.Fatal("recovery source missing", e)
						}
					} else if !errors.Is(e, os.ErrNotExist) {
						t.Fatal("safe abort sources leaked", e)
					}
				}
			})
		}
	}
}

type cancelAfterRead struct{ data []byte }

func (r *cancelAfterRead) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}
func (r *cancelAfterRead) Close() error { return errors.New("source close failure") }

func TestClipImagesJournalAndEachBoundary(t *testing.T) {
	for _, boundary := range []string{"claim", "creating-dir", "writing-1", "written-1", "written-2", "written-3", "written-4", "written-5", "published-1", "published-2", "published-3", "published-4", "published-5", "receipt", "success"} {
		t.Run(boundary, func(t *testing.T) {
			f, staged := stagedPublicationFixture(t)
			counts := map[string]int{}
			hook := &hookedClipImportStore{ClipImportStore: f.store}
			hook.claim = func(record repository.ClipImportRecord) error {
				if _, err := os.Stat(filepath.Join(f.target, "assets")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("base write before claim", err)
				}
				var p clipImagePlan
				if err := json.Unmarshal(record.Journal.Plan, &p); err != nil || p.Version != 2 || len(p.Assets) != 4 {
					t.Fatal("incomplete plan", err)
				}
				private := f.settings.privateStorage.stagingDir
				for _, a := range p.Assets {
					data, e := os.ReadFile(filepath.Join(private, p.StagingID, a.Source.Name))
					if e != nil || clipContentHash(data) != a.File.SHA256 {
						t.Fatal("source not durable before claim", e)
					}
				}
				if boundary == "claim" {
					return errors.New("claim lost")
				}
				return nil
			}
			hook.checkpoint = func(raw json.RawMessage) error {
				var cp clipImageCheckpoint
				json.Unmarshal(raw, &cp)
				counts[cp.Step]++
				if cp.Step == "published" {
					_, err := os.Stat(filepath.Join(f.target, staged.Manifest.Filename))
					if counts[cp.Step] <= 4 && !errors.Is(err, os.ErrNotExist) {
						t.Fatal("note published before assets")
					}
				}
				if boundary == cp.Step || boundary == fmt.Sprintf("%s-%d", cp.Step, counts[cp.Step]) {
					return errors.New("boundary failure")
				}
				return nil
			}
			hook.transition = func(next repository.ClipImportState) error {
				if boundary == "success" && next == repository.ClipImportSucceeded {
					return errors.New("receipt failure")
				}
				return nil
			}
			f.service.store = hook
			// Directory creation failures occur only after the claim.
			if boundary == "creating-dir" {
				hook.claim = func(record repository.ClipImportRecord) error {
					if err := os.WriteFile(filepath.Join(f.target, "assets"), []byte("foreign"), 0600); err != nil {
						t.Fatal(err)
					}
					return nil
				}
			}
			status, _, err := f.service.ImportStaged(t.Context(), staged)
			if boundary == "claim" {
				requireClipperDestinationCode(t, err, model.ClipperRecoveryRequired)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			retain := boundary == "published-5" || boundary == "receipt" || boundary == "success"
			want := model.ClipperImportRejected
			if retain {
				want = model.ClipperImportRecoveryRequired
			}
			if status.State != want {
				t.Fatalf("status %+v want %s", status, want)
			}
			record, _, _ := f.store.Get(t.Context(), staged.Manifest.ImportID)
			var p clipImagePlan
			json.Unmarshal(record.Journal.Plan, &p)
			staged.root.Close() // no borrowed source remains usable
			for _, a := range p.Assets {
				data, e := os.ReadFile(filepath.Join(f.settings.privateStorage.stagingDir, p.StagingID, a.Source.Name))
				if retain {
					if e != nil || clipContentHash(data) != a.File.SHA256 {
						t.Fatal("lost durable recovery source", e)
					}
				} else if !errors.Is(e, os.ErrNotExist) {
					t.Fatal("safe abort retained sources", e)
				}
			}
			if !retain {
				entries, _ := os.ReadDir(filepath.Join(f.target, "assets/images"))
				if len(entries) != 0 {
					t.Fatal("leaked publication", entries)
				}
			}
		})
	}
}
