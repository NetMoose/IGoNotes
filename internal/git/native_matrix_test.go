package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
)

func requireGit(t *testing.T) *CommandRunner {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		if os.Getenv("IGONOTES_REQUIRE_GIT_INTEGRATION") == "1" {
			t.Fatal("required native Git is unavailable")
		}
		t.Skip("native Git is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), helperTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		t.Fatal("native Git version command failed")
	}
	requireNativeGitVersion(t, string(output), os.Getenv("IGONOTES_REQUIRE_GIT_INTEGRATION") == "1")
	runner := NewCommandRunner()
	runner.executable = path
	return runner
}

type nativeGitVersionReporter interface {
	Helper()
	Fatal(...any)
	Skip(...any)
}

func requireNativeGitVersion(t nativeGitVersionReporter, output string, required bool) {
	t.Helper()
	version, err := parseGitVersion(output)
	if err != nil {
		t.Fatal("native Git must report a supported version (>=2.28)")
		return
	}
	if !version.Supported() {
		if required {
			t.Fatal("required native Git must be >=2.28")
		} else {
			t.Skip("installed native Git is unsupported (requires >=2.28)")
		}
	}
}

type nativeGitVersionObservation struct{ action string }

func (*nativeGitVersionObservation) Helper()        {}
func (o *nativeGitVersionObservation) Fatal(...any) { o.action = "fail" }
func (o *nativeGitVersionObservation) Skip(...any)  { o.action = "skip" }

func TestNativeGitVersionRequirementBehavior(t *testing.T) {
	for _, entry := range []struct{ name, output, optional, required string }{
		{"minimum supported", "git version 2.28.0", "", ""},
		{"newer major", "git version 3.0.0", "", ""},
		{"installed unsupported", "git version 2.27.9", "skip", "fail"},
		{"installed old major", "git version 1.99.0", "skip", "fail"},
		{"malformed version", "invalid version", "fail", "fail"},
	} {
		t.Run(entry.name, func(t *testing.T) {
			for _, required := range []bool{false, true} {
				t.Run(fmt.Sprintf("required=%t", required), func(t *testing.T) {
					observation := &nativeGitVersionObservation{}
					requireNativeGitVersion(observation, entry.output, required)
					want := entry.optional
					if required {
						want = entry.required
					}
					if observation.action != want {
						t.Fatalf("version gate action = %q, want %q", observation.action, want)
					}
				})
			}
		})
	}
}

func TestNativeGitVersionIsSupported(t *testing.T) { requireGit(t) }

type nativeFixture struct {
	runner               *CommandRunner
	parent, root, remote string
}

func newNativeFixture(t *testing.T) nativeFixture {
	t.Helper()
	runner := requireGit(t)
	parent := t.TempDir()
	// Isolate personal config as well as repository-specific runner environment.
	t.Setenv("HOME", parent)
	t.Setenv("USERPROFILE", parent)
	t.Setenv("XDG_CONFIG_HOME", parent)
	f := nativeFixture{runner: runner, parent: parent,
		root:   filepath.Join(parent, "root проба # % [notes]"),
		remote: filepath.Join(parent, "bare remote удалённый # % [repo].git")}
	for _, dir := range []string{f.root, f.remote} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal("cannot create native fixture directory")
		}
	}
	f.git(t, f.remote, "init", "--bare", "--initial-branch=main")
	f.git(t, f.root, "init", "--initial-branch=main")
	f.git(t, f.root, "config", "user.name", "Native Matrix")
	f.git(t, f.root, "config", "user.email", "native@example.invalid")
	f.git(t, f.root, "remote", "add", "origin", f.remote)
	return f
}

func (f nativeFixture) git(t *testing.T, dir string, args ...string) Result {
	t.Helper()
	argv := []string{"-c", "core.autocrlf=false", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + filepath.Join(f.parent, "empty-hooks")}
	argv = append(argv, args...)
	scope := LocalOperation
	if args[0] == "push" || args[0] == "clone" {
		scope = NetworkOperation
	}
	result, err := f.runner.Run(context.Background(), Command{Dir: dir, Args: argv, Scope: scope})
	if err != nil {
		t.Fatalf("native Git %s failed: %v", args[0], err)
	}
	if result.StdoutTruncated || result.StderrTruncated {
		t.Fatal("native Git output was truncated")
	}
	return result
}

func probeNativeName(t *testing.T, root, name string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal("cannot create probe parent")
	}
	if err := os.WriteFile(path, []byte("probe"), 0o600); err != nil {
		var pathErr *os.PathError
		class := err
		if errors.As(err, &pathErr) {
			class = pathErr.Err
		}
		if errors.Is(class, syscall.EINVAL) || errors.Is(class, syscall.ENAMETOOLONG) || runtime.GOOS == "windows" && errors.Is(class, syscall.Errno(123)) {
			t.Skipf("filesystem rejects relative name %q (error class %T: %v)", name, class, class)
		}
		t.Fatalf("filename probe %q failed (error class %T)", name, class)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("cannot remove filename probe %q", name)
	}
}

func writeNativeFile(t *testing.T, root, name string, contents []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal("cannot create file parent")
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("cannot write relative name %q", name)
	}
}

func (f nativeFixture) publish(t *testing.T, message string) {
	t.Helper()
	f.git(t, f.root, "add", "--all", "--", ".")
	f.git(t, f.root, "commit", "-m", message)
	f.git(t, f.root, "push", "--set-upstream", "origin", "main")
}

func (f nativeFixture) clone(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(f.parent, name)
	f.git(t, f.parent, "clone", "--", f.remote, dir)
	return dir
}

func (f nativeFixture) assertIndex(t *testing.T, dir string, names ...string) {
	t.Helper()
	want := append([]string(nil), names...)
	sort.Strings(want)
	output := f.git(t, dir, "ls-files", "-z").Stdout
	expected := strings.Join(want, "\x00")
	if len(want) > 0 {
		expected += "\x00"
	}
	if output != expected {
		t.Fatal("ls-files -z did not preserve exact filename bytes")
	}
}

func assertNativeContents(t *testing.T, root, name string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("roundtrip changed relative file %q", name)
	}
}

func TestNativeGitRunnerPreservesUnusualPaths(t *testing.T) {
	names := []string{
		"space name.md", "unicode-пример.md", "-leading-dash.md", "dollar-$(not-a-command).md",
		"semi;colon & ampersand.md", "single'quote.md", ":(glob)*.md", "brackets/[draft] #1%.md",
		"tab\tname.md", "line\nbreak.md", "assets/images/image #1.bin", "data/settings.json",
	}
	for i, name := range names {
		t.Run(fmt.Sprintf("%02d_%s", i, name), func(t *testing.T) {
			f := newNativeFixture(t)
			probeNativeName(t, f.root, name)
			contents := []byte("# exact bytes\nпривет\n\x00\xff\x01\r\n")
			writeNativeFile(t, f.root, name, contents)
			f.publish(t, "unusual path")
			f.assertIndex(t, f.root, name)
			clone := f.clone(t, "clone копия # % [one]")
			f.assertIndex(t, clone, name)
			assertNativeContents(t, clone, name, contents)
		})
	}
}

func TestNativeGitRunnerHonorsIgnoreAssetsAndDeletes(t *testing.T) {
	f := newNativeFixture(t)
	files := map[string][]byte{
		".gitignore": []byte("ignored-secret\n"), "space name.md": []byte("# note\n"),
		"assets/images/image #1.bin": {0, 1, 255, 13, 10}, "data/settings.json": []byte("{\"enabled\":true}\n"),
	}
	for name, contents := range files {
		probeNativeName(t, f.root, name)
		writeNativeFile(t, f.root, name, contents)
	}
	writeNativeFile(t, f.root, "ignored-secret", []byte("local secret"))
	f.publish(t, "notes assets and ignored local secret")
	names := []string{".gitignore", "space name.md", "assets/images/image #1.bin", "data/settings.json"}
	f.assertIndex(t, f.root, names...)
	clone := f.clone(t, "clone начальный # % [two]")
	f.assertIndex(t, clone, names...)
	for name, contents := range files {
		assertNativeContents(t, clone, name, contents)
	}
	assertNativeContents(t, f.root, "ignored-secret", []byte("local secret"))
	if _, err := os.Stat(filepath.Join(clone, "ignored-secret")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ignored secret reached clone")
	}
	deleted := []string{"space name.md", "assets/images/image #1.bin"}
	for _, name := range deleted {
		if err := os.Remove(filepath.Join(f.root, filepath.FromSlash(name))); err != nil {
			t.Fatalf("cannot delete %q", name)
		}
	}
	f.publish(t, "delete note and binary asset")
	f.assertIndex(t, f.root, ".gitignore", "data/settings.json")
	fresh := f.clone(t, "fresh clone свежий # % [three]")
	f.assertIndex(t, fresh, ".gitignore", "data/settings.json")
	for _, name := range append(deleted, "ignored-secret") {
		if _, err := os.Stat(filepath.Join(fresh, filepath.FromSlash(name))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("deleted or ignored file %q reached fresh clone", name)
		}
	}
	assertNativeContents(t, fresh, "data/settings.json", files["data/settings.json"])
	assertNativeContents(t, f.root, "ignored-secret", []byte("local secret"))
}
