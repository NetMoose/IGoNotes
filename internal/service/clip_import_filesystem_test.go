package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func prepareClipNoteTest(t *testing.T, dir, parent, name, text string) *clipNoteFile {
	t.Helper()
	root, err := openBaseRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	f, err := prepareClipNote(root, parent, name, text)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.close() })
	return f
}

func stageClipNoteTest(t *testing.T, f *clipNoteFile) {
	t.Helper()
	if _, err := f.create(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.write(); err != nil {
		t.Fatal(err)
	}
}

func TestClipNoteFilesystemCompleteAndNoReplace(t *testing.T) {
	dir := t.TempDir()
	f := prepareClipNoteTest(t, dir, "", "note.md", "# Привет\n\ncomplete\n")
	plan := f.plan
	if plan.TempName == "" || strings.HasSuffix(plan.TempName, ".md") || plan.SHA256 == "" {
		t.Fatalf("bad plan: %+v", plan)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("prepare wrote files before journal")
	}
	stageClipNoteTest(t, f)
	if _, err := os.Stat(filepath.Join(dir, "note.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("visible before publish: %v", err)
	}
	result, err := f.publish()
	if err != nil || !result.Published || result.PublicationUncertain || !result.Durable || result.Identity == (clipFileIdentity{}) {
		t.Fatalf("publish = %+v, %v", result, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "note.md"))
	if err != nil || string(got) != f.markdown {
		t.Fatalf("content = %q, %v", got, err)
	}
	entries, _ = os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary leaked: %v", entries)
	}
	if err := f.discardTemp(); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(dir, "note.md"))
	if string(got) != f.markdown {
		t.Fatal("cleanup removed published note")
	}
}

func TestClipNoteFilesystemExistingAndConcurrentFile(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "concurrent"}[concurrent], func(t *testing.T) {
			dir := t.TempDir()
			f := prepareClipNoteTest(t, dir, "", "note.md", "new content")
			if !concurrent {
				if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("original\x00bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			stageClipNoteTest(t, f)
			if concurrent {
				if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("original\x00bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := f.publish()
			if !errors.Is(err, os.ErrExist) || result.Published || !result.PublicationUncertain {
				t.Fatalf("publish = %+v, %v", result, err)
			}
			// EEXIST is preserved, but without reconciliation the source remains
			// recovery evidence rather than being deleted based on the errno.
			if err := f.discardTemp(); !errors.Is(err, errClipPublicationUncertain) {
				t.Fatalf("uncertain cleanup = %v", err)
			}
			got, _ := os.ReadFile(filepath.Join(dir, "note.md"))
			if string(got) != "original\x00bytes" {
				t.Fatalf("overwritten: %q", got)
			}
		})
	}
}

func TestClipNoteFilesystemConcurrentPublish(t *testing.T) {
	dir := t.TempDir()
	a := prepareClipNoteTest(t, dir, "", "note.md", "first")
	b := prepareClipNoteTest(t, dir, "", "note.md", "second")
	if a.plan.TempName == b.plan.TempName {
		t.Fatal("reused temporary name")
	}
	stageClipNoteTest(t, a)
	stageClipNoteTest(t, b)
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for _, f := range []*clipNoteFile{a, b} {
		wg.Go(func() {
			r, err := f.publish()
			if err != nil && !errors.Is(err, os.ErrExist) {
				t.Error(err)
			}
			results <- r.Published
		})
	}
	wg.Wait()
	close(results)
	winners := 0
	for published := range results {
		if published {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners: %d", winners)
	}
	for _, f := range []*clipNoteFile{a, b} {
		err := f.discardTemp()
		if f.progress.PublicationUncertain {
			if !errors.Is(err, errClipPublicationUncertain) {
				t.Fatalf("uncertain cleanup = %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, "note.md"))
	if string(got) != "first" && string(got) != "second" {
		t.Fatalf("partial content: %q", got)
	}
}

func TestClipNoteFilesystemRejectsUnsafeOrMissingParent(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"escape": outside, "alias": filepath.Join(dir, ".git")} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}
	root, err := openBaseRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, parent := range []string{"missing", "escape", "alias", ".git", "../outside", "/absolute", "a\\b"} {
		f, err := prepareClipNote(root, parent, "note.md", "body")
		if err == nil {
			f.close()
			t.Errorf("accepted %q", parent)
		}
	}
	for _, name := range []string{"../note.md", "a/b.md", "note.txt", ".md", "NUL.md"} {
		f, err := prepareClipNote(root, "", name, "body")
		if err == nil {
			f.close()
			t.Errorf("accepted %q", name)
		}
	}
	for _, text := range []string{"", " \n", string([]byte{0xff}), strings.Repeat("x", 5*1024*1024+1)} {
		f, err := prepareClipNote(root, "", "note.md", text)
		if err == nil {
			f.close()
			t.Error("accepted invalid text")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created missing parent")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("escaped root")
	}
}

func TestClipNoteFilesystemInjectedFailures(t *testing.T) {
	injected := errors.New("injected")
	for _, step := range []string{"write", "short", "sync", "close", "rename", "parent-sync"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			// All failure paths must leave unrelated existing files byte-exact.
			existing := filepath.Join(dir, "existing.md")
			if err := os.WriteFile(existing, []byte("original\x00bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			f := prepareClipNoteTest(t, dir, "", "note.md", "complete markdown")
			if _, err := f.create(); err != nil {
				t.Fatal(err)
			}
			switch step {
			case "write":
				f.ops.write = func(file *os.File, b []byte) (int, error) { n, _ := file.Write(b[:3]); return n, injected }
			case "short":
				f.ops.write = func(file *os.File, b []byte) (int, error) { return 0, nil }
			case "sync":
				f.ops.sync = func(*os.File) error { return injected }
			case "close":
				f.ops.close = func(file *os.File) error { return errors.Join(file.Close(), injected) }
			case "rename":
				f.ops.rename = func(*os.File, string, string) error { return injected }
			case "parent-sync":
				f.ops.syncParent = func(*os.File) error { return injected }
			}
			_, writeErr := f.write()
			if step == "rename" || step == "parent-sync" {
				if writeErr != nil {
					t.Fatal(writeErr)
				}
				r, err := f.publish()
				if !errors.Is(err, injected) || r.Published != (step == "parent-sync") || r.PublicationUncertain != (step == "rename") || r.Durable {
					t.Fatalf("result %+v, %v", r, err)
				}
			} else if writeErr == nil {
				t.Fatal("ignored write failure")
			}
			cleanupErr := f.discardTemp()
			if step == "rename" {
				if !errors.Is(cleanupErr, errClipPublicationUncertain) {
					t.Fatalf("uncertain cleanup = %v", cleanupErr)
				}
			} else if cleanupErr != nil {
				t.Fatal(cleanupErr)
			}
			entries, _ := os.ReadDir(dir)
			want := 1
			if step == "parent-sync" || step == "rename" {
				want = 2
			}
			if len(entries) != want {
				t.Fatalf("unexpected files: %v", entries)
			}
			got, err := os.ReadFile(existing)
			if err != nil || string(got) != "original\x00bytes" {
				t.Fatalf("existing changed: %q, %v", got, err)
			}
		})
	}
}

func TestClipNoteFilesystemExclusiveCreateAndJournalCheckpoints(t *testing.T) {
	dir := t.TempDir()
	f := prepareClipNoteTest(t, dir, "", "note.md", "# body\n")
	name := filepath.Join(dir, f.plan.TempName)
	if err := os.WriteFile(name, []byte("foreign temp"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.create(); !errors.Is(err, os.ErrExist) {
		t.Fatalf("create collision: %v", err)
	}
	if err := f.discardTemp(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(name)
	if string(got) != "foreign temp" {
		t.Fatal("adopted foreign temp")
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	created, err := f.create()
	if err != nil {
		t.Fatal(err)
	}
	if created.Size != 0 || created.SHA256 != clipContentHash(nil) || created.Published || created.PublicationUncertain {
		t.Fatalf("create checkpoint: %+v", created)
	}
	var restored clipFileProgress
	encoded, err := json.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &restored); err != nil || restored != created {
		t.Fatalf("identity checkpoint not serializable: %+v, %v", restored, err)
	}
	// The writer must handle valid short writes, not publish a truncated note.
	f.ops.write = func(file *os.File, data []byte) (int, error) {
		if len(data) > 2 {
			data = data[:2]
		}
		return file.Write(data)
	}
	written, err := f.write()
	if err != nil {
		t.Fatal(err)
	}
	if written.Size != f.plan.Size || written.SHA256 != f.plan.SHA256 || written.Identity != created.Identity {
		t.Fatalf("write checkpoint: %+v", written)
	}
	if err := f.discardTemp(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned temp remains: %v", err)
	}
}

func TestClipNoteFilesystemEmptyCreateCleanup(t *testing.T) {
	dir := t.TempDir()
	f := prepareClipNoteTest(t, dir, "", "note.md", "body")
	if _, err := f.create(); err != nil {
		t.Fatal(err)
	}
	// A failed journal checkpoint can safely discard the not-yet-written file.
	if err := f.discardTemp(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("empty temp leaked: %v", entries)
	}
}

func TestClipNoteFilesystemPinnedParentAndDestinationSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "parent"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	f := prepareClipNoteTest(t, dir, "parent", "note.md", "body")
	if err := os.Rename(filepath.Join(dir, "parent"), filepath.Join(dir, "original")); err != nil {
		t.Skipf("cannot move pinned parent on host: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, ".git"), filepath.Join(dir, "parent")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	stageClipNoteTest(t, f)
	if _, err := f.publish(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "original", "note.md"))
	if err != nil || string(got) != "body" {
		t.Fatalf("lost pinned parent: %q %v", got, err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, ".git"))
	if len(entries) != 0 {
		t.Fatal("followed parent replacement into .git")
	}
	foreign := filepath.Join(outside, "foreign.md")
	if err := os.WriteFile(foreign, []byte("external bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(dir, "note.md")); err != nil {
		t.Fatal(err)
	}
	g := prepareClipNoteTest(t, dir, "", "note.md", "new body")
	stageClipNoteTest(t, g)
	if r, err := g.publish(); !errors.Is(err, os.ErrExist) || r.Published || !r.PublicationUncertain {
		t.Fatalf("destination symlink: %+v %v", r, err)
	}
	if err := g.discardTemp(); !errors.Is(err, errClipPublicationUncertain) {
		t.Fatalf("uncertain cleanup = %v", err)
	}
	got, _ = os.ReadFile(foreign)
	if string(got) != "external bytes" {
		t.Fatal("followed destination symlink")
	}
}

func TestClipNoteFilesystemFailureDoesNotDeleteExternalChanges(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "modified", true: "replaced"}[replace], func(t *testing.T) {
			dir := t.TempDir()
			f := prepareClipNoteTest(t, dir, "", "note.md", "body")
			if _, err := f.create(); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(dir, f.plan.TempName)
			f.ops.close = func(file *os.File) error {
				if err := file.Close(); err != nil {
					return err
				}
				if replace {
					if err := os.Rename(name, name+".original"); err != nil {
						return err
					}
				}
				if err := os.WriteFile(name, []byte("external change"), 0600); err != nil {
					return err
				}
				return errors.New("close failure")
			}
			if _, err := f.write(); err == nil {
				t.Fatal("expected failure")
			}
			if err := f.discardTemp(); err == nil {
				t.Fatal("ignored ownership change")
			}
			got, _ := os.ReadFile(name)
			if string(got) != "external change" {
				t.Fatalf("deleted external change: %q", got)
			}
		})
	}
}

func TestClipNoteFilesystemPreservesChangedTemporary(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "modified", true: "replaced"}[replace], func(t *testing.T) {
			dir := t.TempDir()
			f := prepareClipNoteTest(t, dir, "", "note.md", "body")
			stageClipNoteTest(t, f)
			name := filepath.Join(dir, f.plan.TempName)
			if replace {
				if err := os.Rename(name, name+".original"); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(name, []byte("external bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			if r, err := f.publish(); err == nil || r.Published || r.PublicationUncertain {
				t.Fatalf("published changed temp: %+v %v", r, err)
			}
			if err := f.discardTemp(); err == nil {
				t.Fatal("did not report changed ownership")
			}
			got, _ := os.ReadFile(name)
			if string(got) != "external bytes" {
				t.Fatalf("deleted external temp: %q", got)
			}
		})
	}
}

func TestClipNoteFilesystemIdenticalReplacementDoesNotProveOwnership(t *testing.T) {
	dir := t.TempDir()
	f := prepareClipNoteTest(t, dir, "", "note.md", "same bytes")
	stageClipNoteTest(t, f)
	name := filepath.Join(dir, f.plan.TempName)
	// Keep the original inode alive, preventing inode reuse from weakening the
	// assertion. Even a byte-identical external replacement is not ours.
	if err := os.Rename(name, name+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(f.markdown), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := f.publish(); !errors.Is(err, errClipFileChanged) || result.Published || result.PublicationUncertain {
		t.Fatalf("adopted replacement: %+v %v", result, err)
	}
	if err := f.discardTemp(); !errors.Is(err, errClipFileChanged) {
		t.Fatalf("cleanup = %v", err)
	}
	got, err := os.ReadFile(name)
	if err != nil || string(got) != f.markdown {
		t.Fatalf("deleted identical replacement: %q %v", got, err)
	}
}

func TestClipNoteFilesystemTemporarySymlinkIsNeverRemoved(t *testing.T) {
	dir := t.TempDir()
	f := prepareClipNoteTest(t, dir, "", "note.md", "same bytes")
	stageClipNoteTest(t, f)
	name := filepath.Join(dir, f.plan.TempName)
	if err := os.Rename(name, name+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(name+".original", name); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if result, err := f.publish(); !errors.Is(err, errClipFileChanged) || result.Published || result.PublicationUncertain {
		t.Fatalf("adopted symlink: %+v %v", result, err)
	}
	if err := f.discardTemp(); !errors.Is(err, errClipFileChanged) {
		t.Fatalf("cleanup = %v", err)
	}
	info, err := os.Lstat(name)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("removed foreign symlink: %v", err)
	}
}

func TestClipNoteFilesystemClosedOperationCannotPerformLateCleanup(t *testing.T) {
	dir := t.TempDir()
	f := prepareClipNoteTest(t, dir, "", "note.md", "body")
	stageClipNoteTest(t, f)
	if err := f.close(); err != nil {
		t.Fatal(err)
	}
	if err := f.discardTemp(); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("late cleanup = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, f.plan.TempName))
	if err != nil || string(got) != "body" {
		t.Fatalf("late cleanup changed temp: %q %v", got, err)
	}
}

func TestClipNoteFilesystemRenameErrorRetainsUncertainPublication(t *testing.T) {
	for _, renamed := range []bool{false, true} {
		for _, inspectionFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("renamed=%t/inspectionFails=%t", renamed, inspectionFails), func(t *testing.T) {
				dir := t.TempDir()
				f := prepareClipNoteTest(t, dir, "", "note.md", "complete markdown")
				stageClipNoteTest(t, f)
				injected := errors.New("rename acknowledgement lost")
				calls := 0
				f.ops.rename = func(parent *os.File, source, destination string) error {
					calls++
					if renamed {
						if err := renameNoReplace(parent, source, destination); err != nil {
							t.Fatal(err)
						}
					}
					if inspectionFails {
						// A failed lookup after an ambiguous rename cannot establish
						// that publication did not happen. No reconciliation is assumed.
						if err := f.parent.Close(); err != nil {
							t.Fatal(err)
						}
					}
					return injected
				}
				result, err := f.publish()
				if !errors.Is(err, injected) || !result.PublicationUncertain || result.Published || result.Durable {
					t.Fatalf("ambiguous rename treated as a definite result: %+v, %v", result, err)
				}
				if inspectionFails {
					if _, err := f.parent.Lstat(f.plan.TempName); err == nil || errors.Is(err, os.ErrNotExist) {
						t.Fatalf("expected inconclusive inspection, got %v", err)
					}
				}
				encoded, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				var restored clipFileProgress
				if err := json.Unmarshal(encoded, &restored); err != nil || restored != result {
					t.Fatalf("uncertainty lost in checkpoint: %+v, %v", restored, err)
				}
				for range 2 {
					again, err := f.publish()
					if !errors.Is(err, errClipPublicationUncertain) || again != result {
						t.Fatalf("blind retry = %+v, %v", again, err)
					}
					if err := f.discardTemp(); !errors.Is(err, errClipPublicationUncertain) {
						t.Fatalf("uncertain cleanup = %v", err)
					}
				}
				if calls != 1 {
					t.Fatalf("retried rename %d times", calls)
				}
				if f.progress != result {
					t.Fatalf("cleanup changed uncertainty: %+v", f.progress)
				}
				// Inspect using independent paths: both a retained source and an
				// actually published destination must survive untouched for recovery.
				name := f.plan.TempName
				absent := f.plan.Filename
				if renamed {
					name, absent = absent, name
				}
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(got) != f.markdown {
					t.Fatalf("lost recovery evidence: %q, %v", got, err)
				}
				if _, err := os.Lstat(filepath.Join(dir, absent)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unexpected entry: %v", err)
				}
			})
		}
	}
}
