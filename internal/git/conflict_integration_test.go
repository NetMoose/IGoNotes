package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type conflictFixture struct {
	Remote string
	Local  string
	Other  string
	Branch string
}

func newConflictFixture(t *testing.T) *conflictFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	xdgConfig := filepath.Join(home, "config")
	if err := os.MkdirAll(xdgConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	fixture := &conflictFixture{
		Remote: filepath.Join(root, "remote.git"),
		Local:  filepath.Join(root, "local"),
		Other:  filepath.Join(root, "other"),
		Branch: "main",
	}

	runConflictGit(t, root, []string{"init", "--bare", "--initial-branch", fixture.Branch, fixture.Remote}...)
	seed := filepath.Join(root, "seed")
	runConflictGit(t, root, []string{"init", "--initial-branch", fixture.Branch, seed}...)
	configureConflictIdentity(t, seed)
	for path, contents := range conflictBaseFiles() {
		writeConflictFile(t, seed, path, contents)
	}
	conflictCommit(t, seed, "base")
	runConflictGit(t, seed, []string{"remote", "add", "origin", fixture.Remote}...)
	runConflictGit(t, seed, []string{"push", "--no-verify", "origin", "HEAD:refs/heads/" + fixture.Branch}...)

	for _, clone := range []string{fixture.Local, fixture.Other} {
		runConflictGit(t, root, []string{"clone", "--branch", fixture.Branch, fixture.Remote, clone}...)
		configureConflictIdentity(t, clone)
	}
	return fixture
}

func TestConflictFixtureCreatesUnmergedIndex(t *testing.T) {
	fixture := newConflictFixture(t)
	writeConflictFile(t, fixture.Local, "victim.md", []byte("local\n"))
	conflictCommit(t, fixture.Local, "local change")
	writeConflictFile(t, fixture.Other, "victim.md", []byte("other\n"))
	conflictCommit(t, fixture.Other, "other change")
	fixture.pushOther(t)
	captured := fixture.fetchManagedRemoteOID(t)
	fixture.mergeCapturedOID(t, captured)

	assertConflictStages(t, fixture, map[string][]int{"victim.md": {1, 2, 3}})
}

func TestConflictFixtureUU(t *testing.T) {
	for _, test := range []struct {
		name  string
		path  string
		local []byte
		other []byte
	}{
		{name: "text", path: "text-uu.md", local: []byte("local\n"), other: []byte("other\n")},
		{name: "binary", path: "binary-uu.bin", local: []byte{0, 1, 2}, other: []byte{0, 3, 4}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newConflictFixture(t)
			writeConflictFile(t, fixture.Local, test.path, []byte("base\n"))
			conflictCommit(t, fixture.Local, "add conflict base")
			fixture.pushLocal(t)
			fixture.fastForwardOther(t)
			writeConflictFile(t, fixture.Local, test.path, test.local)
			conflictCommit(t, fixture.Local, "local change")
			writeConflictFile(t, fixture.Other, test.path, test.other)
			conflictCommit(t, fixture.Other, "other change")
			fixture.pushOther(t)
			fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

			assertConflictStages(t, fixture, map[string][]int{test.path: {1, 2, 3}})
		})
	}
}

func TestConflictFixtureAA(t *testing.T) {
	for _, test := range []struct {
		name  string
		path  string
		local []byte
		other []byte
	}{
		{name: "text", path: "text-aa.md", local: []byte("local\n"), other: []byte("other\n")},
		{name: "binary", path: "binary-aa.bin", local: []byte{0, 1, 2}, other: []byte{0, 3, 4}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newConflictFixture(t)
			writeConflictFile(t, fixture.Local, test.path, test.local)
			conflictCommit(t, fixture.Local, "local add")
			writeConflictFile(t, fixture.Other, test.path, test.other)
			conflictCommit(t, fixture.Other, "other add")
			fixture.pushOther(t)
			fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

			assertConflictStages(t, fixture, map[string][]int{test.path: {2, 3}})
		})
	}
}

func TestConflictFixtureModifyDelete(t *testing.T) {
	for _, test := range []struct {
		name         string
		localDelete  bool
		expectedPath string
		expected     []int
	}{
		{name: "local modifies other deletes", expectedPath: "victim.md", expected: []int{1, 2}},
		{name: "local deletes other modifies", localDelete: true, expectedPath: "victim.md", expected: []int{1, 3}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newConflictFixture(t)
			if test.localDelete {
				removeConflictFile(t, fixture.Local, "victim.md")
				writeConflictFile(t, fixture.Other, "victim.md", []byte("other\n"))
			} else {
				writeConflictFile(t, fixture.Local, "victim.md", []byte("local\n"))
				removeConflictFile(t, fixture.Other, "victim.md")
			}
			conflictCommit(t, fixture.Local, "local modify delete")
			conflictCommit(t, fixture.Other, "other modify delete")
			fixture.pushOther(t)
			fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

			assertConflictStages(t, fixture, map[string][]int{test.expectedPath: test.expected})
		})
	}
}

func TestConflictFixtureRenameDelete(t *testing.T) {
	for _, test := range []struct {
		name        string
		localDelete bool
		renamed     string
		expected    []int
	}{
		{name: "local renames other deletes", renamed: "local-renamed.md", expected: []int{1, 2}},
		{name: "local deletes other renames", localDelete: true, renamed: "other-renamed.md", expected: []int{1, 3}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newConflictFixture(t)
			if test.localDelete {
				removeConflictFile(t, fixture.Local, "victim.md")
				renameConflictFile(t, fixture.Other, "victim.md", test.renamed)
			} else {
				renameConflictFile(t, fixture.Local, "victim.md", test.renamed)
				removeConflictFile(t, fixture.Other, "victim.md")
			}
			conflictCommit(t, fixture.Local, "local rename delete")
			conflictCommit(t, fixture.Other, "other rename delete")
			fixture.pushOther(t)
			captured := fixture.fetchManagedRemoteOID(t)
			base := strings.TrimSpace(string(runConflictGit(t, fixture.Local, []string{"merge-base", "HEAD", captured}...)))
			fixture.mergeCapturedOID(t, captured)

			assertConflictStages(t, fixture, map[string][]int{test.renamed: test.expected})
			if test.localDelete {
				assertRenameDeleteEvidence(t, fixture.Other, base, captured, "victim.md", test.renamed, fixture.Local, base, "HEAD")
			} else {
				assertRenameDeleteEvidence(t, fixture.Local, base, "HEAD", "victim.md", test.renamed, fixture.Other, base, captured)
			}
		})
	}
}

func TestConflictFixtureIgnoresHostGlobalConfig(t *testing.T) {
	hostHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostHome, ".gitconfig"), []byte("[user]\n\tname = hostile host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", hostHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(hostHome, "xdg"))
	fixture := newConflictFixture(t)

	output, err := runConflictGitResult(fixture.Local, []string{"config", "--global", "--get", "user.name"})
	if err == nil || strings.TrimSpace(string(output)) != "" {
		t.Fatalf("fixture read host global Git config: output %q, error %v", output, err)
	}
}

func TestConflictFixtureMultiplePaths(t *testing.T) {
	fixture := newConflictFixture(t)
	writeConflictFile(t, fixture.Local, "victim.md", []byte("local victim\n"))
	writeConflictFile(t, fixture.Local, "added.md", []byte("local added\n"))
	removeConflictFile(t, fixture.Local, "space name.md")
	conflictCommit(t, fixture.Local, "local multiple conflicts")
	writeConflictFile(t, fixture.Other, "victim.md", []byte("other victim\n"))
	writeConflictFile(t, fixture.Other, "added.md", []byte("other added\n"))
	writeConflictFile(t, fixture.Other, "space name.md", []byte("other space\n"))
	conflictCommit(t, fixture.Other, "other multiple conflicts")
	fixture.pushOther(t)
	fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

	assertConflictStages(t, fixture, map[string][]int{
		"victim.md":     {1, 2, 3},
		"added.md":      {2, 3},
		"space name.md": {1, 3},
	})
}

func TestConflictFixtureLiteralFilenames(t *testing.T) {
	fixture := newConflictFixture(t)
	paths := []string{"victim.md", "space name.md", "brackets[one].md", "-leading-dash.md"}
	if runtime.GOOS != "windows" {
		paths = append(paths, ":(glob)*.md")
	}
	for _, path := range paths {
		writeConflictFile(t, fixture.Local, path, []byte("local "+path+"\n"))
		writeConflictFile(t, fixture.Other, path, []byte("other "+path+"\n"))
	}
	conflictCommit(t, fixture.Local, "local literal names")
	conflictCommit(t, fixture.Other, "other literal names")
	fixture.pushOther(t)
	fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

	want := make(map[string][]int, len(paths))
	for _, path := range paths {
		want[path] = []int{1, 2, 3}
	}
	assertConflictStages(t, fixture, want)
}

func TestConflictFixtureControlCharacterFilenames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("control-character filenames are not supported on Windows")
	}
	fixture := newConflictFixture(t)
	paths := conflictControlFilenameProbe(t, fixture.Local)
	for _, path := range paths {
		writeConflictFile(t, fixture.Local, path, []byte("base "+path+"\n"))
	}
	conflictCommit(t, fixture.Local, "add control-name base")
	fixture.pushLocal(t)
	fixture.fastForwardOther(t)
	for _, path := range paths {
		writeConflictFile(t, fixture.Local, path, []byte("local "+path+"\n"))
		writeConflictFile(t, fixture.Other, path, []byte("other "+path+"\n"))
	}
	conflictCommit(t, fixture.Local, "local control names")
	conflictCommit(t, fixture.Other, "other control names")
	fixture.pushOther(t)
	fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

	want := make(map[string][]int, len(paths))
	for _, path := range paths {
		want[path] = []int{1, 2, 3}
	}
	assertConflictStages(t, fixture, want)
}

func (f *conflictFixture) pushOther(t *testing.T) {
	t.Helper()
	runConflictGit(t, f.Other, []string{"push", "--no-verify", "origin", "HEAD:refs/heads/" + f.Branch}...)
}

func (f *conflictFixture) pushLocal(t *testing.T) {
	t.Helper()
	runConflictGit(t, f.Local, []string{"push", "--no-verify", "origin", "HEAD:refs/heads/" + f.Branch}...)
}

func (f *conflictFixture) fetchManagedRemoteOID(t *testing.T) string {
	t.Helper()
	managed := managedRemoteRef(f.Branch)
	runConflictGit(t, f.Local, []string{"fetch", "--no-tags", "origin", "+refs/heads/" + f.Branch + ":" + managed}...)
	return strings.TrimSpace(string(runConflictGit(t, f.Local, []string{"rev-parse", "--verify", managed + "^{commit}"}...)))
}

func (f *conflictFixture) mergeCapturedOID(t *testing.T, oid string) {
	t.Helper()
	args := []string{"merge", "--no-commit", "--no-ff", oid}
	output, err := runConflictGitResult(f.Local, args)
	if err == nil {
		t.Fatalf("git %q unexpectedly merged %q: %s", args, oid, output)
	}
}

func (f *conflictFixture) fastForwardOther(t *testing.T) {
	t.Helper()
	captured := f.fetchManagedRemoteOIDFor(t, f.Other)
	runConflictGit(t, f.Other, []string{"merge", "--ff-only", captured}...)
}

func (f *conflictFixture) fetchManagedRemoteOIDFor(t *testing.T, dir string) string {
	t.Helper()
	managed := managedRemoteRef(f.Branch)
	runConflictGit(t, dir, []string{"fetch", "--no-tags", "origin", "+refs/heads/" + f.Branch + ":" + managed}...)
	return strings.TrimSpace(string(runConflictGit(t, dir, []string{"rev-parse", "--verify", managed + "^{commit}"}...)))
}

func configureConflictIdentity(t *testing.T, dir string) {
	t.Helper()
	runConflictGit(t, dir, []string{"config", "user.name", "IGoNotes Conflict Test"}...)
	runConflictGit(t, dir, []string{"config", "user.email", "igonotes-conflict@example.invalid"}...)
}

func conflictCommit(t *testing.T, dir, message string) {
	t.Helper()
	runConflictGit(t, dir, []string{"add", "--all", "--", "."}...)
	runConflictGit(t, dir, []string{"commit", "-m", message}...)
}

func writeConflictFile(t *testing.T, dir, path string, contents []byte) {
	t.Helper()
	fullPath := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func removeConflictFile(t *testing.T, dir, path string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, path)); err != nil {
		t.Fatal(err)
	}
}

func renameConflictFile(t *testing.T, dir, oldPath, newPath string) {
	t.Helper()
	if err := os.Rename(filepath.Join(dir, oldPath), filepath.Join(dir, newPath)); err != nil {
		t.Fatal(err)
	}
}

func conflictBaseFiles() map[string][]byte {
	files := map[string][]byte{
		"victim.md":        []byte("base\n"),
		"space name.md":    []byte("base space\n"),
		"brackets[one].md": []byte("base brackets\n"),
		"-leading-dash.md": []byte("base dash\n"),
	}
	if runtime.GOOS != "windows" {
		files[":(glob)*.md"] = []byte("base glob\n")
	}
	return files
}

func conflictControlFilenameProbe(t *testing.T, dir string) []string {
	t.Helper()
	paths := []string{"tab\tname.md", "newline\nname.md"}
	for _, path := range paths {
		fullPath := filepath.Join(dir, path)
		if err := os.WriteFile(fullPath, []byte("probe\n"), 0o600); err != nil {
			t.Skipf("filesystem rejects control-character filename %q: %v", path, err)
		}
		if err := os.Remove(fullPath); err != nil {
			t.Skipf("filesystem cannot remove control-character filename %q: %v", path, err)
		}
	}
	return paths
}

func runConflictGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	output, err := runConflictGitResult(dir, args)
	if err != nil {
		t.Fatalf("git %q in %q: %v\n%s", args, dir, err, output)
	}
	return output
}

func runConflictGitResult(dir string, args []string) ([]byte, error) {
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = conflictGitEnvironment(os.Environ())
	return command.CombinedOutput()
}

func assertConflictStages(t *testing.T, fixture *conflictFixture, want map[string][]int) {
	t.Helper()
	stages, err := parseIndexStagesZ(runConflictGit(t, fixture.Local, []string{"ls-files", "--unmerged", "--stage", "--full-name", "-z"}...))
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string][]int)
	for _, stage := range stages {
		got[stage.Path] = append(got[stage.Path], stage.Stage)
	}
	for _, values := range got {
		sort.Ints(values)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unmerged stages = %#v, want %#v", got, want)
	}
}

func assertRenameDeleteEvidence(t *testing.T, retainedDir, base, retainedTip, oldPath, path, deletedDir, deletedBase, deletedTip string) {
	t.Helper()
	assertDiffTreeRecord(t, retainedDir, base, retainedTip, nameStatus{Status: 'R', OldPath: oldPath, Path: path})
	assertDiffTreeRecord(t, deletedDir, deletedBase, deletedTip, nameStatus{Status: 'D', Path: oldPath})
}

func assertDiffTreeRecord(t *testing.T, dir, base, tip string, want nameStatus) {
	t.Helper()
	args := []string{"diff-tree", "-r", "--no-commit-id", "--name-status", "-z", "--find-renames", base, tip}
	entries, err := parseNameStatusZ(runConflictGit(t, dir, args...))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Status == want.Status && entry.OldPath == want.OldPath && entry.Path == want.Path {
			return
		}
	}
	t.Fatalf("name-status record %#v missing from %#v", want, entries)
}

func conflictGitEnvironment(environment []string) []string {
	clean := make([]string, 0, len(environment)+3)
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found && strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			continue
		}
		clean = append(clean, entry)
	}
	return append(clean, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
}
