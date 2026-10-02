package docs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Resolve once during package initialization, before any test can change CWD.
// These values are read-only throughout the test run.
var documentationRoot, documentationRootErr = discoverDocumentationRoot()

func discoverDocumentationRoot() (string, error) {
	_, sourceFile, _, _ := runtime.Caller(0)
	initialDirectory, err := os.Getwd()
	if err != nil {
		// An absolute source path can still identify a validated checkout.
		initialDirectory = ""
	}
	return resolveDocumentationRoot(sourceFile, initialDirectory)
}

func resolveDocumentationRoot(sourceFile, initialDirectory string) (string, error) {
	if filepath.IsAbs(sourceFile) {
		root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
		if validDocumentationRoot(root) {
			return root, nil
		}
	}
	// A -trimpath module-relative filename is not a filesystem location.
	// Walk only the initial absolute directory, never a later test's CWD.
	if filepath.IsAbs(initialDirectory) {
		for directory := filepath.Clean(initialDirectory); ; {
			if validDocumentationRoot(directory) {
				return directory, nil
			}
			parent := filepath.Dir(directory)
			if parent == directory {
				break
			}
			directory = parent
		}
	}
	return "", fmt.Errorf("cannot locate IGoNotes documentation checkout from source %q or initial directory %q; start a relocated -trimpath test executable inside the checkout", sourceFile, initialDirectory)
}

func validDocumentationRoot(root string) bool {
	module, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !regexp.MustCompile(`(?m)^module[\t ]+IGoNotes[\t ]*\r?$`).Match(module) {
		return false
	}
	for _, path := range []string{"docs/api.md", "docs/user.md", "docs/developer.md", "site/docs/developer.md"} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	if documentationRootErr != nil {
		t.Fatal(documentationRootErr)
	}
	return documentationRoot
}

func readDocument(t *testing.T, root, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
}

func requireTokens(t *testing.T, data []byte, tokens ...string) {
	t.Helper()
	for _, token := range tokens {
		if !bytes.Contains(data, []byte(token)) {
			t.Errorf("missing documentation token %q", token)
		}
	}
}

func TestRepositoryRootSurvivesWorkingDirectoryChange(t *testing.T) {
	// Resolve for the first time in this test only after leaving the checkout.
	// With -trimpath, runtime.Caller returns a module-relative source path.
	t.Chdir(t.TempDir())
	root := repositoryRoot(t)
	if !filepath.IsAbs(root) {
		t.Fatalf("repository root must remain absolute after chdir: %q", root)
	}
	requireTokens(t, readDocument(t, root, "go.mod"), "module IGoNotes")
	requireTokens(t, readDocument(t, root, "docs/developer.md"), "# Руководство разработчика IGoNotes")
	if got := repositoryRoot(t); got != root {
		t.Fatalf("repository root changed: %q -> %q", root, got)
	}
}

func TestDocumentationRootDiscovery(t *testing.T) {
	checkout := documentationCheckoutFixture(t, "IGoNotes")
	otherCheckout := documentationCheckoutFixture(t, "IGoNotes")
	wrongModule := documentationCheckoutFixture(t, "OtherModule")
	missingDocs := documentationCheckoutFixture(t, "IGoNotes")
	if err := os.Remove(filepath.Join(missingDocs, "docs", "api.md")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, sourceRoot, initialRoot, want string
	}{
		{"absolute source takes priority", checkout, otherCheckout, checkout},
		{"trimmed source uses initial checkout", "IGoNotes", checkout, checkout},
		{"wrong source module falls back", wrongModule, checkout, checkout},
		{"incomplete source falls back", missingDocs, checkout, checkout},
		{"wrong initial module is rejected", "IGoNotes", wrongModule, ""},
		{"incomplete initial checkout is rejected", "IGoNotes", missingDocs, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := resolveDocumentationRoot(filepath.Join(test.sourceRoot, "internal", "docs", "git_docs_test.go"), filepath.Join(test.initialRoot, "docs"))
			if root != test.want || (err != nil) != (test.want == "") {
				t.Fatalf("resolve root = %q, %v; want %q", root, err, test.want)
			}
		})
	}
}

func documentationCheckoutFixture(t *testing.T, module string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"go.mod": "module " + module + "\n", "docs/api.md": "API\n", "docs/user.md": "User\n",
		"docs/developer.md": "Developer\n", "site/docs/developer.md": "Published developer\n",
	} {
		file := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestSiteDocumentationExactlyMirrorsSources(t *testing.T) {
	root := repositoryRoot(t)
	for _, name := range []string{"api", "user", "developer"} {
		t.Run(name, func(t *testing.T) {
			source := readDocument(t, root, "docs/"+name+".md")
			site := readDocument(t, root, "site/docs/"+name+".md")
			want := append([]byte("---\nlayout: default\n---\n\n"), source...)
			if !bytes.Equal(site, want) {
				t.Errorf("site/docs/%s.md must be exact default frontmatter followed by docs/%s.md (including raw wrappers and template variables)", name, name)
			}
		})
	}
}

func TestDocumentationHasNoObsoleteClaims(t *testing.T) {
	root := repositoryRoot(t)
	paths := []string{"README.md", "AGENTS.md"}
	for _, dir := range []string{"docs", "site"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// Archived implementation plans/specifications describe their own
			// point in time, not the current published application contract.
			if entry.IsDir() && entry.Name() == "superpowers" {
				return filepath.SkipDir
			}
			if !entry.IsDir() && strings.HasSuffix(path, ".md") {
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				paths = append(paths, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	obsolete := regexp.MustCompile(`(?im)git[^\n]*(?:не реализован|будущ|скоро|not implemented|coming soon|future)|(?:не реализован|будущ|скоро|not implemented|coming soon|future)[^\n]*git|igonotes\s+start\b|boltdb|/notes/:id|/note\?path=|/api/(?:create|delete|update|upload|load|tree)\b`)
	for _, path := range paths {
		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			if matches := obsolete.FindAll(readDocument(t, root, path), -1); len(matches) != 0 {
				t.Errorf("obsolete documentation claims: %q", matches)
			}
		})
	}
}

func TestGitAPIDocumentationContract(t *testing.T) {
	root := repositoryRoot(t)
	api := readDocument(t, root, "docs/api.md")
	routes := []string{
		"/api/git/probe", "/api/git/config", "/api/git/status", "/api/git/sync", "/api/git/resume",
		"/api/git/conflicts", "/api/git/conflicts/resolve", "/api/git/conflicts/complete", "/api/git/conflicts/abort",
	}
	requireTokens(t, api, routes...)
	requireTokens(t, api, "expected_revision", "revision", "note_changed", "git_conflict_pending", "202", "409", "2.28")
	codes := regexp.MustCompile(`ErrorCode\s*=\s*"([^"]+)"`).FindAllSubmatch(readDocument(t, root, "internal/git/errors.go"), -1)
	if len(codes) == 0 {
		t.Fatal("no Git error codes found")
	}
	for _, code := range codes {
		requireTokens(t, api, string(code[1]))
	}
	agents := readDocument(t, root, "AGENTS.md")
	requireTokens(t, agents, routes...)
	requireTokens(t, agents, "[x] Синхронизация с Git", "expected_revision", "revision", "note_changed", "changed_paths", "paused", "needs_reconnect", "develop", "master", "pages")
}

func TestGitDeveloperArchitectureAndVerification(t *testing.T) {
	root := repositoryRoot(t)
	developer := readDocument(t, root, "docs/developer.md")
	requireTokens(t, developer,
		"internal/git/", "CommandRunner", "argv", "shell", "GIT_TERMINAL_PROMPT=0", "EOF",
		"CREATE_SUSPENDED", "JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE", "ResumeThread", "process group",
		"Service", "GitManager", "SettingsService", "FIFO", "SettingsService.mu", "NoteService.baseMu",
		"repository/SQLite", "fetch", "reindex", "push", "refs/igonotes/backups/", "2.28",
		"IGONOTES_REQUIRE_GIT_INTEGRATION=1", "bare", "go-git", "--no-verify", "pre-push", "commit hooks",
		"contents: read", "contents: write", "publish", "checkout", "amd64", "arm64")
	for _, path := range []string{"docs/developer.md", "AGENTS.md"} {
		t.Run(path, func(t *testing.T) {
			requireTokens(t, readDocument(t, root, path), "npm --prefix web run verify", "make test", "make test-git", "make test-race", "make vet", "make verify")
		})
	}
	user := readDocument(t, root, "docs/user.md")
	requireTokens(t, user, "Git 2.28", "refs/igonotes/backups/", "force-push", "{{base}}", "{{branch}}", "{{date}}", "{{datetime}}", "{{count}}", "{% raw %}", "{% endraw %}")
	requireTokens(t, readDocument(t, root, "docs/api.md"), "{{base}}", "{{branch}}", "{{date}}", "{{datetime}}", "{{count}}", "{% raw %}", "{% endraw %}")
}
