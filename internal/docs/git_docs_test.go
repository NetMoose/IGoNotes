package docs

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate documentation test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
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
