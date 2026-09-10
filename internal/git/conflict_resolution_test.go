package git

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"IGoNotes/internal/model"
)

func TestCheckoutConflictStage(t *testing.T) {
	base := t.TempDir()
	gitDir := filepath.Join(base, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	runner := &resolutionRunner{stageContents: map[int]string{2: "local\n"}}
	service := NewService(runner, unusedPorcelain{})

	name, err := service.checkoutConflictStage(context.Background(), gitDir, temp, "note.md", 2)
	if err != nil || name != "stage-2" {
		t.Fatalf("checkoutConflictStage() = %q, %v", name, err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %#v", runner.calls)
	}
	command := runner.calls[0]
	if command.Dir != temp || command.Scope != LocalOperation || !command.ReadOnly ||
		!reflect.DeepEqual(command.Args, []string{"--git-dir=" + gitDir, "--work-tree=" + temp, "--literal-pathspecs", "checkout-index", "--temp", "--stage=2", "-z", "--stdin"}) {
		t.Fatalf("checkout command = %#v", command)
	}
	if input := runner.checkoutInputs[0]; string(input) != "note.md\x00" {
		t.Fatalf("checkout stdin = %q", input)
	}
}

func TestCheckoutConflictStageRejectsMismatchedSourceMapping(t *testing.T) {
	base := t.TempDir()
	gitDir := filepath.Join(base, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &resolutionRunner{stageContents: map[int]string{2: "local\n"}, checkoutPath: "other.md"}
	service := NewService(runner, unusedPorcelain{})

	if _, err := service.checkoutConflictStage(context.Background(), gitDir, t.TempDir(), "note.md", 2); err != ErrRecoveryRequired {
		t.Fatalf("checkoutConflictStage() error = %v, want ErrRecoveryRequired", err)
	}
	if len(runner.checkoutInputs) != 1 || string(runner.checkoutInputs[0]) != "note.md\x00" {
		t.Fatalf("checkout stdin = %#v", runner.checkoutInputs)
	}
}

func TestCheckoutConflictStageRejectsInvalidTemporaryMapping(t *testing.T) {
	base := t.TempDir()
	gitDir := filepath.Join(base, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &resolutionRunner{stageContents: map[int]string{2: "local\n"}, checkoutTempPath: "/outside"}
	service := NewService(runner, unusedPorcelain{})

	if _, err := service.checkoutConflictStage(context.Background(), gitDir, t.TempDir(), "note.md", 2); err != ErrRecoveryRequired {
		t.Fatalf("checkoutConflictStage() error = %v, want ErrRecoveryRequired", err)
	}
}

func TestResolveConflictUsesLiteralPaths(t *testing.T) {
	const (
		baseOID   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		localOID  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		remoteOID = "cccccccccccccccccccccccccccccccccccccccc"
	)
	basePath := t.TempDir()
	gitDir := filepath.Join(basePath, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(basePath, "note.md"), []byte("conflict marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &resolutionRunner{
		stageContents: map[int]string{1: "base\n", 2: "local\n", 3: "remote\n"},
		baseOID:       baseOID,
		localOID:      localOID,
		remoteOID:     remoteOID,
	}
	service := NewService(runner, conflictPorcelain{local: LocalInspection{
		HasRepository: true, RepositoryRoot: basePath, GitDir: gitDir, CurrentBranch: "main", PendingOperation: "merge",
	}})
	operation := Operation{
		ID: "0123456789abcdef0123456789abcdef", BaseName: "notes", RepoPath: basePath, Branch: "main", Kind: OperationSync, State: OperationConflict,
		LocalOID: localOID, CandidateOID: remoteOID, RemoteOID: remoteOID,
	}
	conflict := Conflict{
		Kind: ConflictContent, Path: "note.md",
		Base:   &ConflictStage{OID: baseOID, Mode: "100644"},
		Local:  &ConflictStage{OID: localOID, Mode: "100644"},
		Remote: &ConflictStage{OID: remoteOID, Mode: "100644"},
	}
	request := model.GitConflictResolveRequest{
		OperationID: operation.ID, ConflictID: conflictIdentity(conflict), Path: "note.md",
		Action: model.GitConflictUseLocal, ResultPath: "resolved.md", LocalOID: localOID,
	}
	transactionCalls := 0
	snapshot, err := service.ResolveConflict(context.Background(), ConfiguredBase{Name: "notes", Path: basePath, Branch: "main"}, operation, request,
		func(_ context.Context, mutate func(string) error) error {
			transactionCalls++
			return mutate(basePath)
		})
	if err != nil {
		t.Fatal(err)
	}
	if transactionCalls != 1 || !snapshot.CanComplete || len(snapshot.Conflicts) != 0 {
		t.Fatalf("resolved snapshot = %#v, transaction calls = %d", snapshot, transactionCalls)
	}
	contents, err := os.ReadFile(filepath.Join(basePath, "resolved.md"))
	if err != nil || string(contents) != "local\n" {
		t.Fatalf("resolved contents = %q, %v", contents, err)
	}
	if len(runner.addCalls) != 1 {
		t.Fatalf("literal staging calls = %#v", runner.addCalls)
	}
	add := runner.addCalls[0]
	if add.Dir != basePath || add.Scope != LocalOperation || add.ReadOnly ||
		!reflect.DeepEqual(add.Args, []string{"--literal-pathspecs", "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"}) {
		t.Fatalf("literal staging command = %#v", add)
	}
	if len(runner.addInputs) != 1 || string(runner.addInputs[0]) != "note.md\x00resolved.md\x00" {
		t.Fatalf("literal staging stdin = %#v", runner.addInputs)
	}
}

func TestResolveConflictRejectsStaleOperationID(t *testing.T) {
	service := NewService(&resolutionRunner{}, unusedPorcelain{})
	called := false
	_, err := service.ResolveConflict(context.Background(), ConfiguredBase{}, Operation{}, model.GitConflictResolveRequest{OperationID: "stale"},
		func(_ context.Context, mutate func(string) error) error {
			called = true
			return mutate(t.TempDir())
		})
	if err != ErrConflictStale || called {
		t.Fatalf("ResolveConflict() = %v, transaction called = %t", err, called)
	}
}

func TestResolveConflictTreatsKnownMismatchedIdentityAsStale(t *testing.T) {
	service, base, operation, request := resolutionResolveFixture(t)
	request.ConflictID = "another-conflict"
	if _, err := service.ResolveConflict(context.Background(), base, operation, request, directResolutionTransaction(base.Path)); err != ErrConflictStale {
		t.Fatalf("ResolveConflict() error = %v, want ErrConflictStale", err)
	}

	service, base, operation, request = resolutionResolveFixture(t)
	request.Path = "another.md"
	if _, err := service.ResolveConflict(context.Background(), base, operation, request, directResolutionTransaction(base.Path)); err != ErrConflictStale {
		t.Fatalf("ResolveConflict() error = %v, want ErrConflictStale", err)
	}

	service, base, operation, request = resolutionResolveFixture(t)
	request.LocalOID = "another-oid"
	if _, err := service.ResolveConflict(context.Background(), base, operation, request, directResolutionTransaction(base.Path)); err != ErrConflictStale {
		t.Fatalf("ResolveConflict() error = %v, want ErrConflictStale", err)
	}
}

func TestResolveConflictVerifiesDestinationsBeforeMaterializingStages(t *testing.T) {
	service, base, operation, request := resolutionResolveFixture(t)
	if err := os.WriteFile(filepath.Join(base.Path, request.ResultPath), []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := service.runner.(*resolutionRunner)
	if _, err := service.ResolveConflict(context.Background(), base, operation, request, directResolutionTransaction(base.Path)); err != ErrConflictStale {
		t.Fatalf("ResolveConflict() error = %v, want ErrConflictStale", err)
	}
	if len(runner.checkoutInputs) != 6 {
		t.Fatalf("stage checkout happened before destination verification: %#v", runner.checkoutInputs)
	}
}

func TestResolveConflictRejectsMismatchedCheckoutBlobBeforeMutation(t *testing.T) {
	service, base, operation, request := resolutionResolveFixture(t)
	runner := service.runner.(*resolutionRunner)
	runner.hashOID = "dddddddddddddddddddddddddddddddddddddddd"

	if _, err := service.ResolveConflict(context.Background(), base, operation, request, directResolutionTransaction(base.Path)); err != ErrConflictStale {
		t.Fatalf("ResolveConflict() error = %v, want ErrConflictStale", err)
	}
	if _, err := os.Lstat(filepath.Join(base.Path, request.ResultPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination was mutated before OID validation: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(base.Path, "note.md"))
	if err != nil || string(contents) != "conflict marker\n" {
		t.Fatalf("owned source was mutated before OID validation: %q, %v", contents, err)
	}
}

func TestMaterializeResolutionWritesReadsSymlinkStage(t *testing.T) {
	base := t.TempDir()
	gitDir := filepath.Join(base, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &resolutionRunner{stageSymlinks: map[int]string{3: "target/path"}}
	service := NewService(runner, unusedPorcelain{})

	writes, err := service.materializeResolutionWrites(context.Background(), gitDir, "note.md", []resolutionWrite{{Path: "resolved", Stage: 3, Mode: "120000"}})
	if writes != nil {
		defer writes.Close()
	}
	if err != nil || writes == nil || len(writes.writes) != 1 || string(writes.writes[0].data) != "target/path" {
		t.Fatalf("materializeResolutionWrites() = %#v, %v", writes, err)
	}
}

func TestMaterializeResolutionWritesKeepsRegularStageInTempRoot(t *testing.T) {
	base := t.TempDir()
	gitDir := filepath.Join(base, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := string(bytes.Repeat([]byte("regular stage"), 32*1024))
	runner := &resolutionRunner{stageContents: map[int]string{2: content}}
	service := NewService(runner, unusedPorcelain{})

	writes, err := service.materializeResolutionWrites(context.Background(), gitDir, "note.md", []resolutionWrite{{Path: "resolved", Stage: 2, Mode: "100644"}})
	if err != nil || writes == nil {
		t.Fatalf("materializeResolutionWrites() error = %v", err)
	}
	defer writes.Close()
	if len(writes.writes) != 1 || writes.writes[0].source == "" || len(writes.writes[0].data) != 0 {
		t.Fatalf("materialized regular write = %#v", writes.writes)
	}
	reader, err := writes.open(writes.writes[0])
	if err != nil {
		t.Fatal(err)
	}
	actual, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(actual) != content {
		t.Fatalf("temp-root stage content = %d bytes, %v, %v", len(actual), readErr, closeErr)
	}
}

func resolutionResolveFixture(t *testing.T) (*Service, ConfiguredBase, Operation, model.GitConflictResolveRequest) {
	t.Helper()
	const (
		baseOID   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		localOID  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		remoteOID = "cccccccccccccccccccccccccccccccccccccccc"
	)
	basePath := t.TempDir()
	gitDir := filepath.Join(basePath, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(basePath, "note.md"), []byte("conflict marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &resolutionRunner{
		stageContents: map[int]string{1: "base\n", 2: "local\n", 3: "remote\n"},
		baseOID:       baseOID,
		localOID:      localOID,
		remoteOID:     remoteOID,
	}
	service := NewService(runner, conflictPorcelain{local: LocalInspection{
		HasRepository: true, RepositoryRoot: basePath, GitDir: gitDir, CurrentBranch: "main", PendingOperation: "merge",
	}})
	base := ConfiguredBase{Name: "notes", Path: basePath, Branch: "main"}
	operation := Operation{
		ID: "0123456789abcdef0123456789abcdef", BaseName: "notes", RepoPath: basePath, Branch: "main", Kind: OperationSync, State: OperationConflict,
		LocalOID: localOID, CandidateOID: remoteOID, RemoteOID: remoteOID,
	}
	conflict := Conflict{
		Kind: ConflictContent, Path: "note.md",
		Base:   &ConflictStage{OID: baseOID, Mode: "100644"},
		Local:  &ConflictStage{OID: localOID, Mode: "100644"},
		Remote: &ConflictStage{OID: remoteOID, Mode: "100644"},
	}
	request := model.GitConflictResolveRequest{
		OperationID: operation.ID, ConflictID: conflictIdentity(conflict), Path: "note.md",
		Action: model.GitConflictUseLocal, ResultPath: "resolved.md", LocalOID: localOID,
	}
	return service, base, operation, request
}

func directResolutionTransaction(path string) WorktreeTransaction {
	return func(_ context.Context, mutate func(string) error) error { return mutate(path) }
}

func TestWriteConflictEntry(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	t.Run("creates regular executable with parents", func(t *testing.T) {
		if err := writeConflictEntry(root, "nested/entry", "100755", bytes.NewBufferString("contents")); err != nil {
			t.Fatal(err)
		}
		info, err := root.Lstat("nested/entry")
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
			t.Fatalf("entry mode = %v, want regular 0755", info.Mode())
		}
		contents, err := root.ReadFile("nested/entry")
		if err != nil || string(contents) != "contents" {
			t.Fatalf("entry contents = %q, %v", contents, err)
		}
	})

	t.Run("creates symlink from exact target bytes", func(t *testing.T) {
		target := "nested/entry"
		if err := writeConflictEntry(root, "link", "120000", bytes.NewBufferString(target)); err != nil {
			t.Fatal(err)
		}
		info, err := root.Lstat("link")
		if err != nil {
			t.Fatal(err)
		}
		actual, err := root.Readlink("link")
		if err != nil || info.Mode()&os.ModeSymlink == 0 || actual != target {
			t.Fatalf("link = mode %v target %q error %v", info.Mode(), actual, err)
		}
	})

	t.Run("preserves an unowned final symlink", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("unchanged"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := root.Symlink(outside, "replace"); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := writeConflictEntry(root, "replace", "100644", bytes.NewBufferString("replacement")); !errors.Is(err, os.ErrExist) {
			t.Fatalf("writeConflictEntry() error = %v, want os.ErrExist", err)
		}
		info, err := root.Lstat("replace")
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("replacement info = %v, %v", info, err)
		}
		outsideContents, err := os.ReadFile(outside)
		if err != nil || string(outsideContents) != "unchanged" {
			t.Fatalf("outside contents = %q, %v", outsideContents, err)
		}
	})

	t.Run("preserves a symlink that appears after destination verification", func(t *testing.T) {
		if err := verifyResolutionDestinationOwnership(root, nil, []resolutionWrite{{Path: "late", Mode: "100644"}}); err != nil {
			t.Fatal(err)
		}
		if err := root.Symlink("nested/entry", "late"); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := writeConflictEntry(root, "late", "100644", bytes.NewBufferString("replacement")); !errors.Is(err, os.ErrExist) {
			t.Fatalf("writeConflictEntry() error = %v, want os.ErrExist", err)
		}
		info, err := root.Lstat("late")
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("late entry = %v, %v", info, err)
		}
	})

	t.Run("rejects gitlinks", func(t *testing.T) {
		if err := writeConflictEntry(root, "gitlink", "160000", bytes.NewBufferString("ignored")); err == nil {
			t.Fatal("writeConflictEntry() succeeded for gitlink")
		}
	})

	t.Run("matching existing output is retained and executable mode is corrected", func(t *testing.T) {
		if err := writeConflictEntry(root, "retry", "100644", bytes.NewBufferString("same")); err != nil {
			t.Fatal(err)
		}
		if err := writeConflictEntry(root, "retry", "100755", bytes.NewBufferString("same")); err != nil {
			t.Fatalf("writeConflictEntry() retry error = %v", err)
		}
		info, err := root.Lstat("retry")
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("retry mode = %v, %v", info.Mode(), err)
		}
	})
}

func TestWriteConflictEntryStreamsRegularContent(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	content := bytes.Repeat([]byte("stream"), 32*1024)
	if err := writeConflictEntry(root, "streamed", "100644", &maxReadReader{data: content, max: 32 * 1024}); err != nil {
		t.Fatalf("writeConflictEntry() error = %v", err)
	}
	actual, err := root.ReadFile("streamed")
	if err != nil || !bytes.Equal(actual, content) {
		t.Fatalf("streamed entry = %d bytes, %v", len(actual), err)
	}
}

func TestWriteConflictEntryStreamsExistingComparison(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	content := bytes.Repeat([]byte("compare"), 32*1024)
	if err := root.WriteFile("existing", content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeConflictEntry(root, "existing", "100755", &maxReadReader{data: content, max: 32 * 1024}); err != nil {
		t.Fatalf("writeConflictEntry() error = %v", err)
	}
	info, err := root.Lstat("existing")
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("existing mode = %v, %v", info.Mode(), err)
	}
}

type maxReadReader struct {
	data []byte
	max  int
}

func (r *maxReadReader) Read(buffer []byte) (int, error) {
	if len(buffer) > r.max {
		return 0, errors.New("reader was asked to buffer too much")
	}
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	count := len(buffer)
	if count > len(r.data) {
		count = len(r.data)
	}
	copy(buffer, r.data[:count])
	r.data = r.data[count:]
	return count, nil
}

func TestCleanConflictPath(t *testing.T) {
	for _, value := range []string{
		"note.md",
		"notes/idea [final].md",
		"-leading/space name",
		".gitignore",
		"literal\\backslash.txt",
	} {
		t.Run("accept "+value, func(t *testing.T) {
			got, err := cleanConflictPath(value, "result_path")
			if err != nil || got != value {
				t.Fatalf("cleanConflictPath(%q) = %q, %v", value, got, err)
			}
		})
	}

	for _, value := range []string{
		"",
		"\x00",
		"/absolute.md",
		".",
		"../escape.md",
		"notes/../../escape.md",
		"notes//idea.md",
		"notes/./idea.md",
		"notes/idea.md/",
		".git",
		"notes/.git/config",
		"notes/.GiT/config",
	} {
		t.Run("reject "+strings.ReplaceAll(value, "\x00", "NUL"), func(t *testing.T) {
			_, err := cleanConflictPath(value, "result_path")
			if err == nil {
				t.Fatalf("cleanConflictPath(%q) succeeded", value)
			}
			field, ok := err.(*SafeError)
			if !ok || field.Field != "result_path" || field == ErrConflictUnsupported {
				t.Fatalf("cleanConflictPath(%q) error = %#v", value, err)
			}
		})
	}
}

func TestValidateResolution(t *testing.T) {
	conflict := resolutionTestConflict()
	content := "merged\n"
	valid := []struct {
		name    string
		request model.GitConflictResolveRequest
	}{
		{"local", resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "local.md", LocalOID: "local-oid"})},
		{"remote", resolutionRequest(model.GitConflictUseRemote, model.GitConflictResolveRequest{ResultPath: "remote.md", RemoteOID: "remote-oid"})},
		{"manual", resolutionRequest(model.GitConflictManual, model.GitConflictResolveRequest{ResultPath: "manual.md", Content: &content})},
		{"keep both", resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{LocalPath: "local.md", RemotePath: "remote.md", LocalOID: "local-oid", RemoteOID: "remote-oid"})},
		{"delete", resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{})},
	}
	for _, test := range valid {
		t.Run("accept "+test.name, func(t *testing.T) {
			if err := validateResolution(conflict, test.request); err != nil {
				t.Fatalf("validateResolution() error = %v", err)
			}
		})
	}

	t.Run("ignores non-empty differing operation identity for manager", func(t *testing.T) {
		request := resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{})
		request.OperationID = "another-operation"
		if err := validateResolution(conflict, request); err != nil {
			t.Fatalf("validateResolution() error = %v", err)
		}
	})

	t.Run("does not require operation identity at manager boundary", func(t *testing.T) {
		request := resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{})
		request.OperationID = ""
		if err := validateResolution(conflict, request); err != nil {
			t.Fatalf("validateResolution() error = %v", err)
		}
	})

	for _, test := range []struct {
		name    string
		request model.GitConflictResolveRequest
	}{
		{"conflict ID", resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{ConflictID: "changed"})},
		{"path", resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{Path: "changed.md"})},
		{"local OID", resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "note.md", LocalOID: "changed"})},
		{"remote OID", resolutionRequest(model.GitConflictUseRemote, model.GitConflictResolveRequest{ResultPath: "note.md", RemoteOID: "changed"})},
	} {
		t.Run("stale "+test.name, func(t *testing.T) {
			if err := validateResolution(conflict, test.request); err != ErrConflictStale {
				t.Fatalf("validateResolution() error = %v, want ErrConflictStale", err)
			}
		})
	}

	for _, test := range []struct {
		name    string
		request model.GitConflictResolveRequest
		field   string
	}{
		{"action not offered", resolutionRequest(model.GitConflictAction("unknown"), model.GitConflictResolveRequest{}), "action"},
		{"local missing result", resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{LocalOID: "local-oid"}), "result_path"},
		{"remote missing result", resolutionRequest(model.GitConflictUseRemote, model.GitConflictResolveRequest{RemoteOID: "remote-oid"}), "result_path"},
		{"manual missing result", resolutionRequest(model.GitConflictManual, model.GitConflictResolveRequest{Content: &content}), "result_path"},
		{"manual missing content", resolutionRequest(model.GitConflictManual, model.GitConflictResolveRequest{ResultPath: "manual.md"}), "content"},
		{"keep both missing local path", resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{RemotePath: "remote.md", LocalOID: "local-oid", RemoteOID: "remote-oid"}), "local_path"},
		{"keep both missing remote path", resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{LocalPath: "local.md", LocalOID: "local-oid", RemoteOID: "remote-oid"}), "remote_path"},
		{"keep both same path", resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{LocalPath: "same.md", RemotePath: "same.md", LocalOID: "local-oid", RemoteOID: "remote-oid"}), "remote_path"},
		{"local content", resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "note.md", LocalOID: "local-oid", Content: &content}), "content"},
		{"remote local source", resolutionRequest(model.GitConflictUseRemote, model.GitConflictResolveRequest{ResultPath: "note.md", RemoteOID: "remote-oid", LocalPath: "local.md"}), "local_path"},
		{"manual remote source", resolutionRequest(model.GitConflictManual, model.GitConflictResolveRequest{ResultPath: "manual.md", Content: &content, RemoteOID: "remote-oid"}), "remote_oid"},
		{"keep both result", resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{ResultPath: "result.md", LocalPath: "local.md", RemotePath: "remote.md", LocalOID: "local-oid", RemoteOID: "remote-oid"}), "result_path"},
		{"delete output", resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{ResultPath: "result.md"}), "result_path"},
		{"delete source", resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{LocalOID: "local-oid"}), "local_oid"},
		{"invalid result path", resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "../escape.md", LocalOID: "local-oid"}), "result_path"},
		{"invalid keep both path", resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{LocalPath: "local.md", RemotePath: ".git/config", LocalOID: "local-oid", RemoteOID: "remote-oid"}), "remote_path"},
	} {
		t.Run("reject "+test.name, func(t *testing.T) {
			assertConflictField(t, validateResolution(conflict, test.request), test.field)
		})
	}

	t.Run("selected stage must exist", func(t *testing.T) {
		conflict := resolutionTestConflict()
		conflict.Local = nil
		request := resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "note.md", LocalOID: "local-oid"})
		assertConflictField(t, validateResolution(conflict, request), "action")
	})
}

func TestBuildResolutionPlan(t *testing.T) {
	content := "manual bytes\x00remain exact"
	for _, test := range []struct {
		name     string
		request  model.GitConflictResolveRequest
		conflict Conflict
		want     resolutionPlan
	}{
		{
			name:     "local",
			request:  resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "local.md", LocalOID: "local-oid"}),
			conflict: resolutionTestConflict(),
			want:     resolutionPlan{RemovePaths: []string{"note.md", "old.md"}, Writes: []resolutionWrite{{Path: "local.md", Stage: 2, OID: "local-oid", Mode: "100755"}}, StagePaths: []string{"local.md", "note.md", "old.md"}},
		},
		{
			name:     "remote",
			request:  resolutionRequest(model.GitConflictUseRemote, model.GitConflictResolveRequest{ResultPath: "remote.md", RemoteOID: "remote-oid"}),
			conflict: resolutionTestConflict(),
			want:     resolutionPlan{RemovePaths: []string{"note.md", "old.md"}, Writes: []resolutionWrite{{Path: "remote.md", Stage: 3, OID: "remote-oid", Mode: "120000"}}, StagePaths: []string{"note.md", "old.md", "remote.md"}},
		},
		{
			name:     "manual uses local mode and exact data",
			request:  resolutionRequest(model.GitConflictManual, model.GitConflictResolveRequest{ResultPath: "manual.md", Content: &content}),
			conflict: resolutionTestConflict(),
			want:     resolutionPlan{RemovePaths: []string{"note.md", "old.md"}, Writes: []resolutionWrite{{Path: "manual.md", Mode: "100755", Data: []byte(content)}}, StagePaths: []string{"manual.md", "note.md", "old.md"}},
		},
		{
			name:     "manual falls back to remote mode",
			request:  resolutionRequest(model.GitConflictManual, model.GitConflictResolveRequest{ResultPath: "manual.md", Content: &content}),
			conflict: Conflict{ID: "conflict-id", Path: "note.md", Actions: []string{"manual"}, Remote: &ConflictStage{OID: "remote-oid", Mode: "120000"}},
			want:     resolutionPlan{RemovePaths: []string{"note.md"}, Writes: []resolutionWrite{{Path: "manual.md", Mode: "120000", Data: []byte(content)}}, StagePaths: []string{"manual.md", "note.md"}},
		},
		{
			name:     "manual defaults mode",
			request:  resolutionRequest(model.GitConflictManual, model.GitConflictResolveRequest{ResultPath: "manual.md", Content: &content}),
			conflict: Conflict{ID: "conflict-id", Path: "note.md", Actions: []string{"manual"}},
			want:     resolutionPlan{RemovePaths: []string{"note.md"}, Writes: []resolutionWrite{{Path: "manual.md", Mode: "100644", Data: []byte(content)}}, StagePaths: []string{"manual.md", "note.md"}},
		},
		{
			name:     "keep both",
			request:  resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{LocalPath: "local.md", RemotePath: "remote.md", LocalOID: "local-oid", RemoteOID: "remote-oid"}),
			conflict: resolutionTestConflict(),
			want:     resolutionPlan{RemovePaths: []string{"note.md", "old.md"}, Writes: []resolutionWrite{{Path: "local.md", Stage: 2, OID: "local-oid", Mode: "100755"}, {Path: "remote.md", Stage: 3, OID: "remote-oid", Mode: "120000"}}, StagePaths: []string{"local.md", "note.md", "old.md", "remote.md"}},
		},
		{
			name:     "delete",
			request:  resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{}),
			conflict: resolutionTestConflict(),
			want:     resolutionPlan{RemovePaths: []string{"note.md", "old.md"}, Writes: []resolutionWrite{}, StagePaths: []string{"note.md", "old.md"}},
		},
		{
			name:     "reused source is not removed",
			request:  resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "note.md", LocalOID: "local-oid"}),
			conflict: resolutionTestConflict(),
			want:     resolutionPlan{RemovePaths: []string{"old.md"}, Writes: []resolutionWrite{{Path: "note.md", Stage: 2, OID: "local-oid", Mode: "100755"}}, StagePaths: []string{"note.md", "old.md"}},
		},
		{
			name:     "only reused source has an explicit empty removal list",
			request:  resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "note.md", LocalOID: "local-oid"}),
			conflict: Conflict{ID: "conflict-id", Path: "note.md", Actions: []string{"local"}, Local: &ConflictStage{OID: "local-oid", Mode: "100755"}},
			want:     resolutionPlan{RemovePaths: []string{}, Writes: []resolutionWrite{{Path: "note.md", Stage: 2, OID: "local-oid", Mode: "100755"}}, StagePaths: []string{"note.md"}},
		},
		{
			name:     "sorts unsorted keep both inputs bytewise",
			request:  resolutionRequest(model.GitConflictKeepBoth, model.GitConflictResolveRequest{Path: "z-source.md", LocalPath: "z-output.md", RemotePath: "a-output.md", LocalOID: "local-oid", RemoteOID: "remote-oid"}),
			conflict: Conflict{ID: "conflict-id", Path: "z-source.md", OriginalPath: "a-source.md", Actions: []string{"keep_both"}, Local: &ConflictStage{OID: "local-oid", Mode: "100755"}, Remote: &ConflictStage{OID: "remote-oid", Mode: "120000"}},
			want:     resolutionPlan{RemovePaths: []string{"a-source.md", "z-source.md"}, Writes: []resolutionWrite{{Path: "a-output.md", Stage: 3, OID: "remote-oid", Mode: "120000"}, {Path: "z-output.md", Stage: 2, OID: "local-oid", Mode: "100755"}}, StagePaths: []string{"a-output.md", "a-source.md", "z-output.md", "z-source.md"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := buildResolutionPlan(test.conflict, []Conflict{test.conflict}, test.request)
			if err != nil {
				t.Fatalf("buildResolutionPlan() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("buildResolutionPlan() = %#v, want %#v", got, test.want)
			}
		})
	}

	for _, test := range []struct {
		name     string
		conflict Conflict
		request  model.GitConflictResolveRequest
	}{
		{"missing local stage", Conflict{ID: "conflict-id", Path: "note.md", Actions: []string{"local"}}, resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "note.md"})},
		{"missing selected mode", Conflict{ID: "conflict-id", Path: "note.md", Actions: []string{"remote"}, Remote: &ConflictStage{OID: "remote-oid"}}, resolutionRequest(model.GitConflictUseRemote, model.GitConflictResolveRequest{ResultPath: "note.md", RemoteOID: "remote-oid"})},
		{"unsupported selected mode", Conflict{ID: "conflict-id", Path: "note.md", Actions: []string{"local"}, Local: &ConflictStage{OID: "local-oid", Mode: "160000"}}, resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "note.md", LocalOID: "local-oid"})},
	} {
		t.Run("reject "+test.name, func(t *testing.T) {
			if _, err := buildResolutionPlan(test.conflict, []Conflict{test.conflict}, test.request); err == nil {
				t.Fatal("buildResolutionPlan() succeeded")
			}
		})
	}

	t.Run("rejects output collision with another conflict", func(t *testing.T) {
		conflict := resolutionTestConflict()
		other := Conflict{ID: "other", Path: "taken.md", OriginalPath: "legacy.md"}
		request := resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "taken.md", LocalOID: "local-oid"})
		if _, err := buildResolutionPlan(conflict, []Conflict{conflict, other}, request); err == nil {
			t.Fatal("buildResolutionPlan() succeeded")
		}
	})

	t.Run("rejects output collision with another conflict original path", func(t *testing.T) {
		conflict := resolutionTestConflict()
		other := Conflict{ID: "other", Path: "other.md", OriginalPath: "taken.md"}
		request := resolutionRequest(model.GitConflictUseLocal, model.GitConflictResolveRequest{ResultPath: "taken.md", LocalOID: "local-oid"})
		if _, err := buildResolutionPlan(conflict, []Conflict{conflict, other}, request); err == nil {
			t.Fatal("buildResolutionPlan() succeeded")
		}
	})

	t.Run("rejects source collision with another conflict", func(t *testing.T) {
		conflict := resolutionTestConflict()
		conflict.OriginalPath = "taken.md"
		other := Conflict{ID: "other", Path: "taken.md"}
		request := resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{})
		if _, err := buildResolutionPlan(conflict, []Conflict{conflict, other}, request); err == nil {
			t.Fatal("buildResolutionPlan() succeeded")
		}
	})

	t.Run("rejects source collision with another conflict original path", func(t *testing.T) {
		conflict := resolutionTestConflict()
		conflict.OriginalPath = "taken.md"
		other := Conflict{ID: "other", Path: "other.md", OriginalPath: "taken.md"}
		request := resolutionRequest(model.GitConflictDelete, model.GitConflictResolveRequest{})
		if _, err := buildResolutionPlan(conflict, []Conflict{conflict, other}, request); err == nil {
			t.Fatal("buildResolutionPlan() succeeded")
		}
	})
}

type resolutionRunner struct {
	stageContents    map[int]string
	stageSymlinks    map[int]string
	checkoutPath     string
	checkoutTempPath string
	checkoutInputs   [][]byte
	baseOID          string
	localOID         string
	remoteOID        string
	hashOID          string
	resolved         bool
	calls            []Command
	addCalls         []Command
	addInputs        [][]byte
}

func (r *resolutionRunner) Run(_ context.Context, command Command) (Result, error) {
	r.calls = append(r.calls, command)
	if strings.Contains(strings.Join(command.Args, "\x00"), "checkout-index") {
		input, err := io.ReadAll(command.Stdin)
		if err != nil {
			return Result{}, err
		}
		r.checkoutInputs = append(r.checkoutInputs, input)
		stage := 0
		for _, arg := range command.Args {
			if value, found := strings.CutPrefix(arg, "--stage="); found {
				stage = int(value[0] - '0')
			}
		}
		for _, arg := range command.Args {
			if worktree, found := strings.CutPrefix(arg, "--work-tree="); found {
				name := "stage-" + valueForStage(stage)
				if target, symlink := r.stageSymlinks[stage]; symlink {
					if err := os.WriteFile(filepath.Join(worktree, name), []byte(target), 0o600); err != nil {
						return Result{}, err
					}
				} else if err := os.WriteFile(filepath.Join(worktree, name), []byte(r.stageContents[stage]), 0o600); err != nil {
					return Result{}, err
				}
			}
		}
		path := "note.md"
		if r.checkoutPath != "" {
			path = r.checkoutPath
		}
		tempPath := "stage-" + valueForStage(stage)
		if r.checkoutTempPath != "" {
			tempPath = r.checkoutTempPath
		}
		return Result{Stdout: tempPath + "\t" + path + "\x00"}, nil
	}
	if reflect.DeepEqual(command.Args, []string{"--literal-pathspecs", "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"}) {
		input, err := io.ReadAll(command.Stdin)
		if err != nil {
			return Result{}, err
		}
		r.addCalls = append(r.addCalls, command)
		r.addInputs = append(r.addInputs, input)
		r.resolved = true
		return Result{}, nil
	}
	if reflect.DeepEqual(command.Args, []string{"hash-object", "--stdin"}) {
		if _, err := io.ReadAll(command.Stdin); err != nil {
			return Result{}, err
		}
		oid := r.hashOID
		if oid == "" {
			oid = r.localOID
		}
		return Result{Stdout: oid + "\n"}, nil
	}
	if startsArgs(command, "--no-optional-locks", "status") {
		if r.resolved {
			return Result{}, nil
		}
		return Result{Stdout: "u UU N... 100644 100644 100644 100644 " + r.baseOID + " " + r.localOID + " " + r.remoteOID + " note.md\x00"}, nil
	}
	if startsArgs(command, "ls-files", "--unmerged") {
		if r.resolved {
			return Result{}, nil
		}
		return Result{Stdout: "100644 " + r.baseOID + " 1\tnote.md\x00100644 " + r.localOID + " 2\tnote.md\x00100644 " + r.remoteOID + " 3\tnote.md\x00"}, nil
	}
	if startsArgs(command, "ls-files", "--stage") {
		if r.resolved {
			return Result{}, nil
		}
		return Result{Stdout: "100644 " + r.baseOID + " 1\tnote.md\x00100644 " + r.localOID + " 2\tnote.md\x00100644 " + r.remoteOID + " 3\tnote.md\x00"}, nil
	}
	if hasArgs([]Command{command}, "rev-parse", "--verify", "HEAD^{commit}") {
		return Result{Stdout: r.localOID + "\n"}, nil
	}
	if hasArgs([]Command{command}, "rev-parse", "--verify", "MERGE_HEAD^{commit}") || hasArgs([]Command{command}, "rev-parse", "--verify", "refs/igonotes/remotes/main^{commit}") {
		return Result{Stdout: r.remoteOID + "\n"}, nil
	}
	if hasArgs([]Command{command}, "merge-base", "--all", "HEAD", "MERGE_HEAD") {
		return Result{Stdout: r.baseOID + "\n"}, nil
	}
	if hasArgs([]Command{command}, "check-attr", "-z", "--stdin", "diff") {
		if r.resolved {
			return Result{}, nil
		}
		return Result{Stdout: "note.md\x00diff\x00unspecified\x00"}, nil
	}
	return Result{}, nil
}

func valueForStage(stage int) string {
	return string(rune('0' + stage))
}

func resolutionTestConflict() Conflict {
	return Conflict{
		ID:           "conflict-id",
		Path:         "note.md",
		OriginalPath: "old.md",
		Local:        &ConflictStage{OID: "local-oid", Mode: "100755"},
		Remote:       &ConflictStage{OID: "remote-oid", Mode: "120000"},
		Actions:      []string{"local", "remote", "manual", "keep_both", "delete"},
	}
}

func resolutionRequest(action model.GitConflictAction, request model.GitConflictResolveRequest) model.GitConflictResolveRequest {
	request.OperationID = "operation-id"
	if request.ConflictID == "" {
		request.ConflictID = "conflict-id"
	}
	if request.Path == "" {
		request.Path = "note.md"
	}
	request.Action = action
	return request
}

func assertConflictField(t *testing.T, err error, want string) {
	t.Helper()
	field, ok := err.(*SafeError)
	if !ok || field.Field != want || field == ErrConflictUnsupported {
		t.Fatalf("error = %#v, want fresh field error for %q", err, want)
	}
}
