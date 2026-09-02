package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
)

func TestGitProbeServiceIntegrationIsReadOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			t.Skip("Git is unavailable")
		}
		t.Fatalf("locate Git: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	ctx := context.Background()
	client := gitcmd.NewClient(gitcmd.NewCommandRunner())
	version, err := client.Version(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("inspect Git version: %v", err)
	}
	if !version.Supported() {
		t.Skipf("Git %d.%d is below 2.28", version.Major, version.Minor)
	}

	root := t.TempDir()
	working := filepath.Join(root, "working")
	remote := filepath.Join(root, "remote.git")
	if err := os.Mkdir(working, 0o755); err != nil {
		t.Fatalf("create working directory: %v", err)
	}
	runGitSetup(t, root, "init", "--bare", remote)
	runGitSetup(t, working, "init", "--initial-branch=main")
	runGitSetup(t, working, "config", "user.name", "IGoNotes Probe Test")
	runGitSetup(t, working, "config", "user.email", "probe@example.invalid")
	if err := os.WriteFile(filepath.Join(working, "note.md"), []byte("# Read only\n"), 0o644); err != nil {
		t.Fatalf("write fixture note: %v", err)
	}
	runGitSetup(t, working, "add", "note.md")
	runGitSetup(t, working, "commit", "-m", "initial fixture")
	remoteURL := "file://" + filepath.ToSlash(remote)
	runGitSetup(t, working, "remote", "add", "origin", remoteURL)
	runGitSetup(t, working, "push", "-u", "origin", "main")

	gitDir := strings.TrimSpace(string(runGitFixture(t, working, "rev-parse", "--absolute-git-dir")))
	gitDir, err = filepath.EvalSymlinks(gitDir)
	if err != nil {
		t.Fatalf("canonicalize Git directory: %v", err)
	}
	remoteOID := strings.TrimSpace(string(runGitFixture(t, working, "rev-parse", "main")))
	beforeConfig := readProbeFixture(t, filepath.Join(gitDir, "config"))
	beforeRefs := runGitFixture(t, working, "show-ref")
	beforeStatus := runGitFixture(t, working, "status", "--porcelain=v1", "-z")
	beforeRemoteRefs := runGitFixture(t, remote, "show-ref")
	beforeFetchHead := snapshotOptionalProbeFile(t, filepath.Join(gitDir, "FETCH_HEAD"))
	beforeObjects := snapshotProbeObjects(t, filepath.Join(gitDir, "objects"))

	settings := probeSettingsStub{config: model.Config{Bases: []model.Base{{Name: "work", Path: working}}, CurrentBase: "work"}}
	recorder := &recordingProbeRunner{delegate: gitcmd.NewCommandRunner()}
	client = gitcmd.NewClient(recorder)
	service := NewGitProbeService(settings, client)
	discovery, err := service.Probe(ctx, model.GitProbeRequest{Base: "work", GitURL: remoteURL})
	if err != nil {
		t.Fatalf("branchless Probe() error = %v", err)
	}
	if discovery.BlockingError != nil || discovery.CanConfigure || !slices.Contains(discovery.RemoteBranches, "main") {
		t.Fatalf("branchless Probe() = %#v, want main discovery without branch blocker", discovery)
	}

	selected, err := service.Probe(ctx, model.GitProbeRequest{Base: "work", GitURL: remoteURL, GitBranch: "main"})
	if err != nil {
		t.Fatalf("selected Probe() error = %v", err)
	}
	if selected.BlockingError != nil || !selected.CanConfigure || selected.HistoryRelation != "shared" {
		t.Fatalf("selected Probe() = %#v, want configurable shared history", selected)
	}

	assertProbeCommands(t, recorder.commands, working, remoteURL, remoteOID)
	assertOptionalProbeFileEqual(t, "FETCH_HEAD", beforeFetchHead, snapshotOptionalProbeFile(t, filepath.Join(gitDir, "FETCH_HEAD")))
	if afterObjects := snapshotProbeObjects(t, filepath.Join(gitDir, "objects")); !slices.Equal(beforeObjects, afterObjects) {
		t.Errorf(".git/objects changed during probe\nbefore: %#v\nafter:  %#v", beforeObjects, afterObjects)
	}
	assertProbeBytesEqual(t, ".git/config", beforeConfig, readProbeFixture(t, filepath.Join(gitDir, "config")))
	assertProbeBytesEqual(t, "working refs", beforeRefs, runGitFixture(t, working, "show-ref"))
	assertProbeBytesEqual(t, "working status", beforeStatus, runGitFixture(t, working, "status", "--porcelain=v1", "-z"))
	assertProbeBytesEqual(t, "bare remote refs", beforeRemoteRefs, runGitFixture(t, remote, "show-ref"))
}

type recordingProbeRunner struct {
	delegate gitcmd.Runner
	commands []gitcmd.Command
}

func (r *recordingProbeRunner) Run(ctx context.Context, command gitcmd.Command) (gitcmd.Result, error) {
	recorded := command
	recorded.Args = append([]string(nil), command.Args...)
	recorded.Secrets = append([]string(nil), command.Secrets...)
	r.commands = append(r.commands, recorded)
	return r.delegate.Run(ctx, command)
}

func assertProbeCommands(t *testing.T, got []gitcmd.Command, dir, remoteURL, remoteOID string) {
	t.Helper()
	local := func(args ...string) gitcmd.Command {
		return gitcmd.Command{Dir: dir, Args: args, Scope: gitcmd.LocalOperation, ReadOnly: true}
	}
	network := func(args ...string) gitcmd.Command {
		return gitcmd.Command{Dir: dir, Args: args, Scope: gitcmd.NetworkOperation, ReadOnly: true, Secrets: []string{remoteURL}}
	}
	inspectLocal := []gitcmd.Command{
		local("rev-parse", "--show-toplevel"),
		local("rev-parse", "--absolute-git-dir"),
		local("symbolic-ref", "--quiet", "HEAD"),
		local("status", "--porcelain=v1", "-z", "--untracked-files=all"),
		local("remote", "get-url", "origin"),
		local("config", "--get", "user.name"),
		local("config", "--get", "user.email"),
		local("rev-parse", "--verify", "HEAD"),
	}

	want := []gitcmd.Command{local("--version")}
	want = append(want, inspectLocal...)
	want = append(want, network("ls-remote", "--symref", remoteURL))
	want = append(want, local("--version"))
	want = append(want, inspectLocal...)
	want = append(want,
		local("check-ref-format", "refs/heads/main"),
		network("ls-remote", "--symref", remoteURL),
		local("config", "--get", "extensions.partialClone"),
		local("config", "--name-only", "--get-regexp", `^remote\..*\.(promisor|partialclonefilter)$`),
		local("cat-file", "-e", remoteOID+"^{commit}"),
		local("merge-base", "--is-ancestor", remoteOID, "HEAD"),
	)

	mutating := map[string]bool{
		"fetch": true, "add": true, "commit": true, "merge": true, "push": true, "update-ref": true,
		"init": true, "checkout": true, "switch": true, "reset": true, "rebase": true, "cherry-pick": true,
	}
	for _, command := range got {
		if !command.ReadOnly {
			t.Errorf("probe issued non-read-only command: %#v", command)
		}
		if len(command.Args) > 0 && mutating[command.Args[0]] {
			t.Errorf("probe issued mutating Git command: %#v", command)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("probe commands differ from the exact inspection allowlist\ngot:  %#v\nwant: %#v", got, want)
	}
}

type optionalProbeFile struct {
	exists  bool
	content []byte
}

func snapshotOptionalProbeFile(t *testing.T, path string) optionalProbeFile {
	t.Helper()
	content, err := os.ReadFile(path)
	switch {
	case err == nil:
		return optionalProbeFile{exists: true, content: content}
	case errors.Is(err, os.ErrNotExist):
		return optionalProbeFile{}
	default:
		t.Fatalf("read optional file %s: %v", path, err)
		return optionalProbeFile{}
	}
}

func assertOptionalProbeFileEqual(t *testing.T, name string, before, after optionalProbeFile) {
	t.Helper()
	if before.exists != after.exists || !bytes.Equal(before.content, after.content) {
		t.Errorf("%s changed during probe\nbefore: exists=%v content=%q\nafter:  exists=%v content=%q", name, before.exists, before.content, after.exists, after.content)
	}
}

type probeObjectHash struct {
	path string
	hash [sha256.Size]byte
}

func snapshotProbeObjects(t *testing.T, objectsDir string) []probeObjectHash {
	t.Helper()
	objects := []probeObjectHash{}
	err := filepath.WalkDir(objectsDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(objectsDir, path)
		if err != nil {
			return err
		}
		objects = append(objects, probeObjectHash{path: filepath.ToSlash(relative), hash: sha256.Sum256(content)})
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot Git objects: %v", err)
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].path < objects[j].path })
	return objects
}

func runGitSetup(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func runGitFixture(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return output
}

func readProbeFixture(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return contents
}

func assertProbeBytesEqual(t *testing.T, name string, before, after []byte) {
	t.Helper()
	if !bytes.Equal(before, after) {
		t.Errorf("%s changed during probe\nbefore: %q\nafter:  %q", name, before, after)
	}
}
