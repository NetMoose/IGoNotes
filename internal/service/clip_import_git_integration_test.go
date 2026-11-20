package service

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

func requireClipNativeGit(t *testing.T) {
	t.Helper()
	gitProbeTestEnvironment(t)
	version, err := gitcmd.NewClient(gitcmd.NewCommandRunner()).Version(t.Context(), t.TempDir())
	if err != nil || !version.Supported() {
		if os.Getenv("IGONOTES_REQUIRE_GIT_INTEGRATION") == "1" {
			t.Fatalf("native Git >= 2.28 required: version=%+v err=%v", version, err)
		}
		t.Skipf("native Git >= 2.28 unavailable: %v", err)
	}
}

func clipPublicSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, name)
		files[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestClipStartupRecoveryNativeGit(t *testing.T) {
	for _, baseName := range []string{"active", "target"} {
		for _, scenario := range []string{"conflict", "ambiguous", "committed-public", "ordinary-sync"} {
			t.Run(baseName+"/"+scenario, func(t *testing.T) {
				requireClipNativeGit(t)
				phase := "before-note"
				if scenario == "committed-public" {
					phase = "before-receipt"
				}
				f, row := crashedRecovery(t, phase, baseName, true)
				root := f.target
				i := 1
				if baseName == "active" {
					root, i = f.active, 0
				}
				beforeIndex, err := f.index.GetAllNodes()
				if err != nil {
					t.Fatal(err)
				}
				remote := filepath.Join(t.TempDir(), "remote.git")
				runManagerGit(t, root, "init", "--bare", "--initial-branch=main", remote)
				runManagerGit(t, root, "init", "--initial-branch=main")
				runManagerGit(t, root, "config", "user.name", "Clip Recovery Test")
				runManagerGit(t, root, "config", "user.email", "clip@example.invalid")
				runManagerGit(t, root, "config", "core.autocrlf", "false")
				writeTestNote(t, root, "git-note.md", "initial\n")
				runManagerGit(t, root, "add", "git-note.md")
				runManagerGit(t, root, "commit", "-m", "initial")
				runManagerGit(t, root, "remote", "add", "origin", remote)
				runManagerGit(t, root, "push", "origin", "main")
				head := runManagerGit(t, root, "rev-parse", "HEAD")
				runManagerGit(t, root, "update-ref", "refs/igonotes/remotes/main", head)
				f.settings.config.Bases[i].GitURL = remote
				f.settings.config.Bases[i].GitBranch = "main"
				snapshot, active, err := f.settings.GitSnapshot(baseName)
				if err != nil || active != (baseName == "active") {
					t.Fatal("snapshot", err)
				}
				statuses := repository.NewGitStatusRepository(f.db)
				operations := repository.NewGitOperationRepository(f.db)
				seedManagerTrust(t, &gitManagerFixture{operations: operations, statuses: statuses}, snapshot, head)
				var network atomic.Int32
				delegate := gitcmd.NewCommandRunner()
				runner := managerRunnerFunc(func(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
					if command.Scope == gitcmd.NetworkOperation {
						network.Add(1)
					}
					return delegate.Run(ctx, command)
				})
				client := gitcmd.NewClient(runner)
				manager := NewGitManager(gitcmd.NewService(runner, client), statuses, operations, NewGitProbeService(f.settings, client), f.settings.GitSnapshot, f.notes, f.settings.coordinator)
				t.Cleanup(func() { manager.Close() })
				if scenario == "conflict" {
					runManagerGit(t, root, "switch", "-c", "other")
					writeTestNote(t, root, "git-note.md", "other\n")
					runManagerGit(t, root, "commit", "-am", "other")
					runManagerGit(t, root, "switch", "main")
					writeTestNote(t, root, "git-note.md", "main\n")
					runManagerGit(t, root, "commit", "-am", "main")
					cmd := exec.Command("git", "merge", "other")
					cmd.Dir = root
					cmd.Env = append(os.Environ(), "LC_ALL=C")
					if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "CONFLICT") {
						t.Fatalf("expected merge conflict: %s %v", output, err)
					}
					now := time.Now().UTC()
					op := gitcmd.Operation{ID: strings.Repeat("d", 32), BaseName: baseName, RepoPath: root, ConfigFingerprint: snapshot.Fingerprint, RemoteFingerprint: snapshot.RemoteFingerprint, Kind: gitcmd.OperationSync, State: gitcmd.OperationQueued, Stage: gitcmd.StageMerging, Branch: "main", RemoteOID: head, CreatedAt: now, UpdatedAt: now}
					if err := operations.CreateQueued(t.Context(), op); err != nil {
						t.Fatal(err)
					}
				}
				if err := manager.RecoverLocal(t.Context(), []gitcmd.ConfiguredBase{snapshot}); err != nil {
					t.Fatal(err)
				}
				if network.Load() != 0 {
					t.Fatal("Git startup recovery accessed network")
				}
				var plan clipImagePlan
				if err := json.Unmarshal(row.Journal.Plan, &plan); err != nil {
					t.Fatal(err)
				}
				assetPath := filepath.ToSlash(filepath.Join("assets/images", plan.Assets[0].File.Filename))
				committedInodes := map[string]os.FileInfo{}
				if scenario == "committed-public" {
					// Another Git user/operation already committed and pushed these
					// public inodes while the import receipt remained unfinished.
					runManagerGit(t, root, "add", "--", row.NotePath, assetPath)
					runManagerGit(t, root, "commit", "-m", "clip before receipt")
					runManagerGit(t, root, "push", "origin", "main")
					for _, name := range []string{row.NotePath, assetPath} {
						info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
						if err != nil {
							t.Fatal(err)
						}
						committedInodes[name] = info
					}
				}
				if scenario == "ambiguous" {
					if err := os.WriteFile(filepath.Join(root, plan.Note.TempName), []byte("external edit"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				before := clipPublicSnapshot(t, root)
				gitBefore := runManagerGit(t, root, "status", "--porcelain=v1")
				stagesBefore := runManagerGit(t, root, "ls-files", "--stage")
				if err := f.service.RecoverLocal(t.Context()); err != nil {
					t.Fatal(err)
				}
				recovered, _, err := f.store.Get(t.Context(), row.ImportID)
				if err != nil {
					t.Fatal(err)
				}
				recoveryIndex, err := f.index.GetAllNodes()
				if err != nil {
					t.Fatal(err)
				}
				if baseName == "target" && !reflect.DeepEqual(beforeIndex, recoveryIndex) {
					t.Fatal("inactive recovery changed active index")
				}
				if scenario == "conflict" || scenario == "ambiguous" {
					if recovered.State != repository.ClipImportRecoveryRequired {
						t.Fatalf("conflict gate/state: %s", recovered.State)
					}
					if scenario == "conflict" && !errors.Is(f.settings.coordinator.CheckMutation(root), ErrGitConflictPending) {
						t.Fatal("missing Git gate")
					}
					if !reflect.DeepEqual(before, clipPublicSnapshot(t, root)) || gitBefore != runManagerGit(t, root, "status", "--porcelain=v1") || stagesBefore != runManagerGit(t, root, "ls-files", "--stage") {
						t.Fatal("recovery changed conflicting worktree")
					}
					m := f.manifest(t, baseName)
					m.ImportID = "98765432-1234-4567-8901-123456789abc"
					_, _, err := f.service.Import(t.Context(), m)
					if scenario == "ambiguous" {
						requireClipperDestinationCode(t, err, model.ClipperRecoveryRequired)
					} else if !errors.Is(err, ErrGitConflictPending) {
						t.Fatal("new publication bypassed Git gate", err)
					}
					other := "active"
					if baseName == "active" {
						other = "target"
					}
					m = f.manifest(t, other)
					m.ImportID = "87654321-1234-4567-8901-123456789abc"
					status, _, err := f.service.Import(t.Context(), m)
					if err != nil || status.State != model.ClipperImportSucceeded {
						t.Fatalf("unrelated base blocked: %+v %v", status, err)
					}
				} else {
					if recovered.State != repository.ClipImportSucceeded {
						t.Fatalf("recovery: %s", recovered.State)
					}
					if scenario == "committed-public" && !reflect.DeepEqual(before, clipPublicSnapshot(t, root)) {
						t.Fatal("late recovery removed/changed Git-synced files")
					}
					if _, _, err := manager.QueueSync(t.Context(), gitcmd.SyncRequest{Snapshot: snapshot}); err != nil {
						t.Fatal(err)
					}
					if err := manager.Start(); err != nil {
						t.Fatal(err)
					}
					waitManagerTerminal(t, manager)
					status, _, err := statuses.Get(t.Context(), root)
					if err != nil || status.State != model.GitStateReady || status.Error != nil {
						t.Fatalf("ordinary sync: %+v %v", status, err)
					}
					for _, name := range []string{row.NotePath, assetPath} {
						local, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
						if err != nil {
							t.Fatal(err)
						}
						cmd := exec.Command("git", "--git-dir="+remote, "show", "main:"+name)
						remoteData, err := cmd.Output()
						if err != nil || string(local) != string(remoteData) {
							t.Fatalf("synced bytes %s: %v", name, err)
						}
					}
					if err := f.service.RecoverLocal(t.Context()); err != nil {
						t.Fatal(err)
					}
					for name, original := range committedInodes {
						current, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
						if err != nil || !os.SameFile(original, current) {
							t.Fatalf("Git-synced inode removed/replaced: %s %v", name, err)
						}
					}
				}
				if f.notes.GetBasePath() != f.active {
					t.Fatal("recovery/sync switched active runtime")
				}
				afterIndex, err := f.index.GetAllNodes()
				if err != nil {
					t.Fatal(err)
				}
				if baseName == "target" && scenario != "conflict" && scenario != "ambiguous" && !reflect.DeepEqual(beforeIndex, afterIndex) {
					t.Fatal("inactive recovery/sync changed active index")
				}
			})
		}
	}
}
