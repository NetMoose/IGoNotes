package service

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"IGoNotes/internal/model"
)

func TestClipperDestinationFoldersSelectedRootLeavesEditorAndIndexUnchanged(t *testing.T) {
	f := newClipperDestinationFixture(t)
	config := f.settings.GetConfig()
	tree, err := f.notes.GetTree()
	if err != nil {
		t.Fatal(err)
	}
	nodes, _ := f.repo.GetAllNodes()
	prepare, commit, rollback := f.repo.prepareCalls, f.repo.commitCalls, f.repo.rollbackCalls
	for _, base := range []struct{ name, root string }{{"active", f.active}, {"target", f.target}} {
		want := []string{"", "nested", "nested/Юникод 🦆", "spaces & [brackets]", "z"}
		allDirs := append(append([]string{}, want[1:]...), ".hidden/deep", "assets/images", "ASSETS/deep", "nested/.cache/deep", "nested/assets/x", "nested/AsSeTs/x")
		for _, dir := range allDirs {
			if err := os.MkdirAll(filepath.Join(base.root, filepath.FromSlash(dir)), 0700); err != nil {
				t.Fatal(err)
			}
		}
		// Enumeration must not open even an unreadable Markdown file.
		if err := os.WriteFile(filepath.Join(base.root, "nested", "private.md"), []byte("private Markdown"), 0000); err != nil {
			t.Fatal(err)
		}
		got, err := f.service.Folders(base.name)
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(want)
		if got.Base != base.name || !reflect.DeepEqual(got.Folders, want) {
			t.Fatalf("folders = %+v, want %v", got, want)
		}
		if got.DestinationRevision == "" || got.DestinationRevision != issueClipperDestination(t, f.service, base.name) {
			t.Fatal("listing did not issue the current binding")
		}
		if err := f.service.WithDestination(base.name, got.DestinationRevision, func(snapshot ClipperDestinationSnapshot, root *os.Root) error {
			if snapshot.Name != base.name {
				t.Fatal("wrong snapshot")
			}
			_, err := root.Stat("nested/Юникод 🦆")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	afterTree, err := f.notes.GetTree()
	if err != nil {
		t.Fatal(err)
	}
	afterNodes, _ := f.repo.GetAllNodes()
	if !reflect.DeepEqual(config, f.settings.GetConfig()) || f.notes.GetBasePath() != f.active || f.store.saveCalls != 0 {
		t.Fatal("discovery changed configuration/runtime")
	}
	if !reflect.DeepEqual(tree, afterTree) || !reflect.DeepEqual(nodes, afterNodes) || prepare != f.repo.prepareCalls || commit != f.repo.commitCalls || rollback != f.repo.rollbackCalls {
		t.Fatal("discovery changed the active index")
	}
}

func TestClipperDestinationFoldersEmptyMissingAndInaccessible(t *testing.T) {
	f := newClipperDestinationFixture(t)
	if err := os.Remove(filepath.Join(f.target, "target.md")); err != nil {
		t.Fatal(err)
	}
	got, err := f.service.Folders("target")
	if err != nil || !reflect.DeepEqual(got.Folders, []string{""}) {
		t.Fatalf("empty root = %+v, %v", got, err)
	}
	for _, name := range []string{"", "missing", "../target", f.target} {
		got, err := f.service.Folders(name)
		requireClipperDestinationCode(t, err, model.ClipperDestinationNotFound)
		if !reflect.DeepEqual(got, model.ClipperFoldersResponse{}) {
			t.Fatal("error returned partial listing")
		}
	}
	if err := os.Remove(f.target); err != nil {
		t.Fatal(err)
	}
	got, err = f.service.Folders("target")
	requireClipperDestinationCode(t, err, model.ClipperDestinationChanged)
	if !reflect.DeepEqual(got, model.ClipperFoldersResponse{}) || strings.Contains(err.Error(), f.target) {
		t.Fatalf("unsafe failed response: %+v, %v", got, err)
	}
}

func TestClipperDestinationFoldersSkipGitAndAllSymlinkAliases(t *testing.T) {
	f := newClipperDestinationFixture(t)
	outside := t.TempDir()
	for _, dir := range []string{".git/secret", ".GiT/secret", "real/child", "real/.GIT/secret"} {
		if err := os.MkdirAll(filepath.Join(f.target, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(outside, "outside-secret"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"escape": outside, "git-alias": ".git", "internal": "real", "loop": ".", "broken": "missing", "note.md": filepath.Join(f.target, "target.md")} {
		if err := os.Symlink(target, filepath.Join(f.target, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	got, err := f.service.Folders("target")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Folders, []string{"", "real", "real/child"}) {
		t.Fatalf("unsafe listing: %+v", got)
	}
}

func TestClipperDestinationFoldersRevisionMatchesListingDuringRebind(t *testing.T) {
	f := newClipperDestinationFixture(t)
	roots := []string{f.target, t.TempDir()}
	for i, root := range roots {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("marker-%d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 60 {
			if _, err := f.settings.UpdateBase("target", model.BaseUpdateRequest{Name: "target", Path: roots[i%2]}); err != nil {
				t.Error(err)
				return
			}
		}
	})
	for range 60 {
		got, err := f.service.Folders("target")
		if err != nil {
			t.Fatal(err)
		}
		err = f.service.WithDestination("target", got.DestinationRevision, func(_ ClipperDestinationSnapshot, root *os.Root) error {
			for _, folder := range got.Folders {
				if _, err := root.Stat(folderOrDot(folder)); err != nil {
					return fmt.Errorf("revision/listing mismatch: %w", err)
				}
			}
			return nil
		})
		if err != nil {
			requireClipperDestinationCode(t, err, model.ClipperDestinationChanged)
		}
	}
	wg.Wait()
}

func folderOrDot(folder string) string {
	if folder == "" {
		return "."
	}
	return folder
}

// Deterministically model replacement after ReadDir/Info, before traversal.
func TestClipperDestinationFoldersRejectDirectoryReplacementBeforeRead(t *testing.T) {
	for _, replacement := range []string{"outside symlink", "git alias", "original alias", "other directory", "removed"} {
		t.Run(replacement, func(t *testing.T) {
			base := t.TempDir()
			if err := os.Mkdir(filepath.Join(base, "folder"), 0700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(base)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			expected, err := root.Lstat("folder")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(base, "folder"), filepath.Join(base, "original")); err != nil {
				t.Fatal(err)
			}
			switch replacement {
			case "other directory":
				err = os.Mkdir(filepath.Join(base, "folder"), 0700)
			case "removed":
			default:
				target := t.TempDir()
				if replacement == "git alias" {
					target = filepath.Join(base, ".git")
					if err := os.Mkdir(target, 0700); err != nil {
						t.Fatal(err)
					}
				} else if replacement == "original alias" {
					target = filepath.Join(base, "original")
				}
				err = os.Symlink(target, filepath.Join(base, "folder"))
				if err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			child, err := openClipperFolder(root, "folder", expected)
			if child != nil {
				child.Close()
				t.Fatal("replacement handle accepted for traversal")
			}
			if err == nil {
				t.Fatal("replacement did not fail")
			}
		})
	}
}

func TestClipperDestinationFoldersFailWholeListingOnUnreadableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions")
	}
	f := newClipperDestinationFixture(t)
	blocked := filepath.Join(f.target, "unreadable")
	if err := os.Mkdir(blocked, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0700) })
	if file, err := os.Open(blocked); err == nil {
		file.Close()
		t.Skip("process can bypass directory permissions")
	}
	got, err := f.service.Folders("target")
	requireClipperDestinationCode(t, err, model.ClipperDestinationChanged)
	if !reflect.DeepEqual(got, model.ClipperFoldersResponse{}) || strings.Contains(err.Error(), blocked) {
		t.Fatalf("partial/private error response: %+v, %v", got, err)
	}
}

func TestClipperDestinationFoldersScanErrorHasNoPartialResult(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	folders, err := clipperFolders(root)
	if err == nil || folders != nil {
		t.Fatalf("failed scan = %v, %v; want no partial folders", folders, err)
	}
}

func TestClipperDestinationFoldersConcurrentSymlinkReplacement(t *testing.T) {
	f := newClipperDestinationFixture(t)
	for _, dir := range []string{"folder", ".git/git-secret"} {
		if err := os.MkdirAll(filepath.Join(f.target, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(".git", filepath.Join(f.target, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Remove(filepath.Join(f.target, "alias")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 150 {
			if err := os.Rename(filepath.Join(f.target, "folder"), filepath.Join(f.target, "parked")); err != nil {
				t.Error(err)
				return
			}
			if err := os.Symlink(".git", filepath.Join(f.target, "folder")); err != nil {
				t.Error(err)
				return
			}
			runtime.Gosched()
			if err := os.Remove(filepath.Join(f.target, "folder")); err != nil {
				t.Error(err)
				return
			}
			if err := os.Rename(filepath.Join(f.target, "parked"), filepath.Join(f.target, "folder")); err != nil {
				t.Error(err)
				return
			}
		}
	})
	for range 150 {
		got, err := f.service.Folders("target")
		if err != nil {
			requireClipperDestinationCode(t, err, model.ClipperDestinationChanged)
			if !reflect.DeepEqual(got, model.ClipperFoldersResponse{}) {
				t.Error("partial response on race")
			}
			continue
		}
		for _, folder := range got.Folders {
			if strings.Contains(folder, "secret") || strings.Contains(strings.ToLower(folder), ".git") {
				t.Errorf("symlink race leaked %q", folder)
			}
		}
	}
	wg.Wait()
}
