package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

	beforeConfig := readProbeFixture(t, filepath.Join(working, ".git", "config"))
	beforeRefs := runGitFixture(t, working, "show-ref")
	beforeStatus := runGitFixture(t, working, "status", "--porcelain=v1", "-z")
	beforeRemoteRefs := runGitFixture(t, remote, "show-ref")

	settings := probeSettingsStub{config: model.Config{Bases: []model.Base{{Name: "work", Path: working}}, CurrentBase: "work"}}
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

	assertProbeBytesEqual(t, ".git/config", beforeConfig, readProbeFixture(t, filepath.Join(working, ".git", "config")))
	assertProbeBytesEqual(t, "working refs", beforeRefs, runGitFixture(t, working, "show-ref"))
	assertProbeBytesEqual(t, "working status", beforeStatus, runGitFixture(t, working, "status", "--porcelain=v1", "-z"))
	assertProbeBytesEqual(t, "bare remote refs", beforeRemoteRefs, runGitFixture(t, remote, "show-ref"))
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
