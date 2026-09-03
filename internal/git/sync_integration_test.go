package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func seedRemoteAdvance(t *testing.T, fixture *connectFixture, path, contents string) string {
	t.Helper()
	seed := filepath.Join(filepath.Dir(fixture.root), "seed")
	fullPath := filepath.Join(seed, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.git(seed, "add", "--all", "--", ".")
	fixture.git(seed, "commit", "-m", "remote advance")
	fixture.git(seed, "push", "origin", "HEAD:refs/heads/main")
	return fixture.git(seed, "rev-parse", "HEAD")
}

func TestSyncNoChangesCreatesNoCommit(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	before := fixture.git(fixture.root, "rev-list", "--count", "HEAD")
	result, err := runSync(t, fixture, nil, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after := fixture.git(fixture.root, "rev-list", "--count", "HEAD"); after != before {
		t.Fatalf("commit count = %s, want %s", after, before)
	}
	if len(result.ChangedPaths) != 0 || result.Ahead != 0 || result.Behind != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestSyncCommitsAllNonignoredChanges(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write(".gitignore", "ignored.tmp\n")
	fixture.write("note.md", "note\n")
	fixture.write("other.txt", "other\n")
	fixture.write("ignored.tmp", "ignored\n")
	result, err := runSync(t, fixture, nil, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".gitignore", "note.md", "other.txt"}
	if !reflect.DeepEqual(result.ChangedPaths, want) {
		t.Fatalf("ChangedPaths = %#v, want %#v", result.ChangedPaths, want)
	}
	if got := fixture.git(fixture.root, "log", "-1", "--format=%s"); got != "sync notes main 2026-09-03 2026-09-03T14:05:06+02:00 3" {
		t.Fatalf("commit message = %q", got)
	}
	if output, err := runFixtureGit(fixture.root, "cat-file", "-e", "HEAD:ignored.tmp"); err == nil {
		t.Fatalf("ignored file was committed: %q, %v", output, err)
	}
}

func TestSyncPropagatesAssetsOtherFilesAndDeletes(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("assets/images/image.png", "image")
	fixture.write("data.json", "{}\n")
	if err := os.Remove(filepath.Join(fixture.root, "remote.md")); err != nil {
		t.Fatal(err)
	}
	result, err := runSync(t, fixture, nil, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"assets/images/image.png", "data.json", "remote.md"}
	if !reflect.DeepEqual(result.ChangedPaths, want) {
		t.Fatalf("ChangedPaths = %#v, want %#v", result.ChangedPaths, want)
	}
	verify := filepath.Join(filepath.Dir(fixture.root), "verify")
	fixture.git(filepath.Dir(fixture.root), "clone", "--branch", "main", fixture.remote, verify)
	for _, path := range []string{"assets/images/image.png", "data.json"} {
		if _, err := os.Stat(filepath.Join(verify, path)); err != nil {
			t.Fatalf("remote clone missing %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(verify, "remote.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted path remains: %v", err)
	}
}

func TestSyncFastForwardsRemoteOnlyChange(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	remote := seedRemoteAdvance(t, fixture, "remote-only.md", "remote only\n")
	result, err := runSync(t, fixture, nil, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.HeadOID != remote || result.PushOID != remote || fixture.git(fixture.root, "rev-parse", "HEAD") != remote {
		t.Fatalf("result = %#v, remote = %q", result, remote)
	}
}

func TestSyncCreatesMergeCommitForDivergence(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("local.md", "local\n")
	fixture.git(fixture.root, "add", "--all", "--", ".")
	fixture.git(fixture.root, "commit", "-m", "existing local")
	remote := seedRemoteAdvance(t, fixture, "remote-only.md", "remote\n")
	result, err := runSync(t, fixture, nil, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	parents := strings.Fields(fixture.git(fixture.root, "rev-list", "--parents", "-n", "1", result.HeadOID))
	if len(parents) != 3 {
		t.Fatalf("merge parents = %#v", parents)
	}
	assertAncestor(t, fixture, remote, result.HeadOID)
}

func TestSyncReindexesBeforeWorktreeUnlock(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("local.md", "local\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	reindexed := false
	runner.before = func(command Command) error {
		if len(command.Args) > 0 && command.Args[0] == "push" && !reindexed {
			t.Fatal("push began before transaction reindex/unlock")
		}
		return nil
	}
	transaction := func(_ context.Context, mutate func(string) error) error {
		err := mutate(fixture.root)
		reindexed = true
		return err
	}
	if _, err := runSync(t, fixture, runner, options, transaction, nil); err != nil {
		t.Fatal(err)
	}
	if !reindexed {
		t.Fatal("transaction did not reindex")
	}
}

func TestSyncLeavesEditsCreatedDuringPushForNextCycle(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("first.md", "first\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	runner.before = func(command Command) error {
		if len(command.Args) > 0 && command.Args[0] == "push" {
			fixture.write("during-push.md", "next cycle\n")
		}
		return nil
	}
	result, err := runSync(t, fixture, runner, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status := fixture.git(fixture.root, "status", "--porcelain", "--", "during-push.md"); status == "" {
		t.Fatal("edit created during push was consumed")
	}
	if output, err := runFixtureGit(fixture.root, "cat-file", "-e", result.PushOID+":during-push.md"); err == nil {
		t.Fatalf("captured push commit contains late edit: %q, %v", output, err)
	}
}

func TestSyncRetriesOneNonFastForwardPush(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("local.md", "local\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	advanced := false
	runner.before = func(command Command) error {
		if !advanced && len(command.Args) > 0 && command.Args[0] == "push" {
			advanced = true
			seedRemoteAdvance(t, fixture, "race.md", "race\n")
		}
		return nil
	}
	result, err := runSync(t, fixture, runner, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(servicePushes(runner.commands)); got != 2 {
		t.Fatalf("push count = %d, want 2", got)
	}
	if result.RemoteOID != fixture.remoteOID() || fixture.git(fixture.root, "show", "HEAD:race.md") != "race" {
		t.Fatalf("retry result = %#v", result)
	}
	if want := []string{"local.md", "race.md"}; !reflect.DeepEqual(result.ChangedPaths, want) {
		t.Fatalf("retry ChangedPaths = %#v, want %#v", result.ChangedPaths, want)
	}
}

func TestSyncAcceptsBranchAdvanceBetweenProbeAndFetch(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	runner := &interceptRunner{delegate: NewCommandRunner()}
	advanced := false
	runner.after = func(command Command, result Result, err error) (Result, error) {
		if !advanced && reflect.DeepEqual(command.Args,
			[]string{"ls-remote", "--exit-code", "--heads", "origin", "refs/heads/main"}) {
			advanced = true
			seedRemoteAdvance(t, fixture, "between.md", "between\n")
		}
		return result, err
	}
	result, err := runSync(t, fixture, runner, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !advanced || result.RemoteOID != fixture.remoteOID() || fixture.git(fixture.root, "show", "HEAD:between.md") != "between" {
		t.Fatalf("result = %#v, advanced = %v", result, advanced)
	}
}

func TestSyncRechecksOriginURLsImmediatelyBeforePush(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("local.md", "local\n")
	malicious := filepath.Join(filepath.Dir(fixture.root), "malicious.git")
	fixture.git(filepath.Dir(fixture.root), "init", "--bare", "--initial-branch", "main", malicious)
	runner := &interceptRunner{delegate: NewCommandRunner()}
	injected := false
	_, err := runSync(t, fixture, runner, options, nil, func(_ context.Context, checkpoint Checkpoint) error {
		if checkpoint.Stage == StagePushing && !injected {
			fixture.git(fixture.root, "config", "remote.origin.pushurl", malicious)
			injected = true
		}
		return nil
	})
	assertSafeCode(t, err, CodeOriginMismatch)
	if !injected {
		t.Fatal("push URL was not changed at the pre-push checkpoint")
	}
	if len(servicePushes(runner.commands)) != 0 {
		t.Fatal("push ran after push URL changed")
	}
	if output, refErr := runFixtureGit(filepath.Dir(fixture.root), "--git-dir", malicious, "show-ref"); refErr == nil || output != "" {
		t.Fatalf("malicious remote received refs: %q, %v", output, refErr)
	}
}

func TestSyncDetectsDeletedRemoteBranchBeforeWorktreeMutation(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.git(filepath.Dir(fixture.root), "--git-dir", fixture.remote, "update-ref", "-d", "refs/heads/main")
	called := false
	_, err := runSync(t, fixture, nil, options, func(_ context.Context, mutate func(string) error) error {
		called = true
		return mutate(fixture.root)
	}, nil)
	assertSafeCode(t, err, CodeBranchDeleted)
	if called {
		t.Fatal("worktree mutated after deleted branch")
	}
}

func forceRewriteRemote(t *testing.T, fixture *connectFixture) string {
	t.Helper()
	rewrite := filepath.Join(filepath.Dir(fixture.root), "rewrite")
	if err := os.Mkdir(rewrite, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.git(rewrite, "init", "--initial-branch", "main")
	fixture.git(rewrite, "config", "user.name", "IGoNotes Test")
	fixture.git(rewrite, "config", "user.email", "igonotes@example.invalid")
	if err := os.WriteFile(filepath.Join(rewrite, "rewritten.md"), []byte("rewrite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.git(rewrite, "add", "--all", "--", ".")
	fixture.git(rewrite, "commit", "-m", "rewritten")
	fixture.git(rewrite, "push", "--force", fixture.remote, "HEAD:refs/heads/main")
	return fixture.git(rewrite, "rev-parse", "HEAD")
}

func TestSyncDetectsRemoteHistoryRewriteBeforeMerge(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	forceRewriteRemote(t, fixture)
	called := false
	_, err := runSync(t, fixture, nil, options, func(_ context.Context, mutate func(string) error) error {
		called = true
		return mutate(fixture.root)
	}, nil)
	assertSafeCode(t, err, CodeRemoteHistoryRewritten)
	if called {
		t.Fatal("worktree callback ran for rewritten history")
	}
}

func TestSyncDoesNotAdvanceTrustedOIDOnRewrite(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	trusted := options.LastRemoteOID
	forceRewriteRemote(t, fixture)
	var published []string
	_, err := runSync(t, fixture, nil, options, nil, func(_ context.Context, checkpoint Checkpoint) error {
		published = append(published, checkpoint.RemoteOID)
		return nil
	})
	assertSafeCode(t, err, CodeRemoteHistoryRewritten)
	if got := fixture.git(fixture.root, "rev-parse", managedRemoteRef("main")); got != trusted {
		t.Fatalf("managed OID = %q, want %q", got, trusted)
	}
	for _, oid := range published {
		if oid != "" && oid != trusted {
			t.Fatalf("untrusted OID published: %q", oid)
		}
	}
}

func TestSyncRecognizesPreviouslyAcceptedTimedOutPush(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("local.md", "local\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	runner.after = func(command Command, result Result, err error) (Result, error) {
		if len(command.Args) > 0 && command.Args[0] == "push" && err == nil {
			return Result{}, &SafeError{Code: CodeTimedOut, Message: "Git command timed out"}
		}
		return result, err
	}
	first, err := runSync(t, fixture, runner, options, nil, nil)
	assertSafeCode(t, err, CodeTimedOut)
	count := fixture.git(fixture.root, "rev-list", "--count", "HEAD")
	options.Operation.ID = "11111111111111111111111111111111"
	second, err := runSync(t, fixture, nil, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after := fixture.git(fixture.root, "rev-list", "--count", "HEAD"); after != count {
		t.Fatalf("duplicate commit created: count %s -> %s", count, after)
	}
	if second.RemoteOID != first.PushOID || second.PushOID != first.PushOID {
		t.Fatalf("recovered results first=%#v second=%#v", first, second)
	}
}

func TestSyncRechecksRemoteOIDImmediatelyBeforePush(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("local.md", "local\n")
	advanced := false
	transaction := func(_ context.Context, mutate func(string) error) error {
		err := mutate(fixture.root)
		if err == nil && !advanced {
			advanced = true
			seedRemoteAdvance(t, fixture, "race.md", "race\n")
		}
		return err
	}
	runner := &interceptRunner{delegate: NewCommandRunner()}
	result, err := runSync(t, fixture, runner, options, transaction, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(servicePushes(runner.commands)); got != 1 {
		t.Fatalf("stale push was attempted; push count = %d, want 1", got)
	}
	if result.RemoteOID != fixture.remoteOID() {
		t.Fatalf("result = %#v", result)
	}
}

func TestSyncConflictReindexesAndSkipsPush(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	fixture.write("remote.md", "local conflict\n")
	seedRemoteAdvance(t, fixture, "remote.md", "remote conflict\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	reindexed := false
	transaction := func(_ context.Context, mutate func(string) error) error {
		err := mutate(fixture.root)
		reindexed = true
		return err
	}
	result, err := runSync(t, fixture, runner, options, transaction, nil)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !reflect.DeepEqual(conflict.Paths, []string{"remote.md"}) {
		t.Fatalf("Sync() error = %#v, result = %#v", err, result)
	}
	if !reindexed {
		t.Fatal("conflict was not reindexed")
	}
	if len(servicePushes(runner.commands)) != 0 {
		t.Fatal("push ran after conflict")
	}
}

func TestSyncRejectsOrphanedUnmergedIndexBeforeAdd(t *testing.T) {
	fixture, options, operation := makeSyncConflict(t)
	if err := os.Remove(filepath.Join(fixture.root, ".git", "MERGE_HEAD")); err != nil {
		t.Fatal(err)
	}
	options = syncOptions(fixture, operation.RemoteOID)
	options.Operation.ID = "22222222222222222222222222222222"
	indexBefore := fixture.git(fixture.root, "ls-files", "--stage")
	statusBefore := fixture.git(fixture.root, "status", "--porcelain=v1")
	contentsBefore, err := os.ReadFile(filepath.Join(fixture.root, "remote.md"))
	if err != nil {
		t.Fatal(err)
	}
	runner := &interceptRunner{delegate: NewCommandRunner()}

	_, err = runSync(t, fixture, runner, options, nil, nil)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !reflect.DeepEqual(conflict.Paths, []string{"remote.md"}) {
		t.Fatalf("Sync() error = %#v, conflict = %#v", err, conflict)
	}
	if !errorHasSafeCode(err, CodeOperationInterrupted) {
		t.Fatalf("Sync() error = %#v, want operation_interrupted context", err)
	}
	for _, command := range runner.commands {
		if len(command.Args) == 0 {
			continue
		}
		switch command.Args[0] {
		case "add", "commit", "merge", "push":
			t.Fatalf("mutation ran for orphaned unmerged index: %#v", command)
		}
	}
	if indexAfter := fixture.git(fixture.root, "ls-files", "--stage"); indexAfter != indexBefore {
		t.Fatalf("index changed:\n%s\nwant:\n%s", indexAfter, indexBefore)
	}
	if statusAfter := fixture.git(fixture.root, "status", "--porcelain=v1"); statusAfter != statusBefore {
		t.Fatalf("worktree status changed: %q -> %q", statusBefore, statusAfter)
	}
	contentsAfter, err := os.ReadFile(filepath.Join(fixture.root, "remote.md"))
	if err != nil || !reflect.DeepEqual(contentsAfter, contentsBefore) {
		t.Fatalf("worktree contents changed: %q -> %q, %v", contentsBefore, contentsAfter, err)
	}
}

func TestSyncPreExistingMergeConflictUsesWorktreeHandoff(t *testing.T) {
	fixture, options, operation := makeSyncConflict(t)
	options = syncOptions(fixture, operation.RemoteOID)
	options.Operation.ID = "33333333333333333333333333333333"
	runner := &interceptRunner{delegate: NewCommandRunner()}
	callbackCalled := false
	gatePublished := false
	handoffInsideCallback := false
	transaction := func(_ context.Context, mutate func(string) error) error {
		callbackCalled = true
		inside := true
		err := mutate(fixture.root)
		var conflict *ConflictError
		if errors.As(err, &conflict) {
			gatePublished = true
			handoffInsideCallback = inside
		}
		inside = false
		return err
	}

	result, err := runSync(t, fixture, runner, options, transaction, nil)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !reflect.DeepEqual(conflict.Paths, []string{"remote.md"}) ||
		!reflect.DeepEqual(result.ConflictPaths, []string{"remote.md"}) {
		t.Fatalf("Sync() = %#v, %#v", result, err)
	}
	if !callbackCalled || !gatePublished || !handoffInsideCallback {
		t.Fatalf("handoff callback=%v gate=%v inside=%v", callbackCalled, gatePublished, handoffInsideCallback)
	}
	assertNoSyncNetworkOrWorktreeMutation(t, runner.commands)
}

func TestSyncPreExistingResolvedMergeUsesTypedWorktreeHandoff(t *testing.T) {
	fixture, options, operation := makeSyncConflict(t)
	fixture.git(fixture.root, "add", "--all", "--", ".")
	options = syncOptions(fixture, operation.RemoteOID)
	options.Operation.ID = "44444444444444444444444444444444"
	runner := &interceptRunner{delegate: NewCommandRunner()}
	callbackCalled := false
	transaction := func(_ context.Context, mutate func(string) error) error {
		callbackCalled = true
		return mutate(fixture.root)
	}

	_, err := runSync(t, fixture, runner, options, transaction, nil)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || len(conflict.Paths) != 0 || !errorHasSafeCode(err, CodeOperationInterrupted) {
		t.Fatalf("Sync() error = %#v, conflict = %#v", err, conflict)
	}
	if !callbackCalled {
		t.Fatal("pre-existing resolved merge did not enter worktree callback")
	}
	assertNoSyncNetworkOrWorktreeMutation(t, runner.commands)
}

func TestSyncPreExistingNonMergeOperationSkipsWorktreeAndNetwork(t *testing.T) {
	fixture, options := preparedSyncFixture(t)
	head := fixture.git(fixture.root, "rev-parse", "HEAD")
	marker := filepath.Join(fixture.root, ".git", "CHERRY_PICK_HEAD")
	if err := os.WriteFile(marker, []byte(head+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &interceptRunner{delegate: NewCommandRunner()}
	callbackCalled := false
	_, err := runSync(t, fixture, runner, options, func(_ context.Context, mutate func(string) error) error {
		callbackCalled = true
		return mutate(fixture.root)
	}, nil)
	assertSafeCode(t, err, CodeRepositoryLocked)
	if callbackCalled {
		t.Fatal("non-merge pending operation entered worktree callback")
	}
	assertNoSyncNetworkOrWorktreeMutation(t, runner.commands)
}

func assertNoSyncNetworkOrWorktreeMutation(t *testing.T, commands []Command) {
	t.Helper()
	for _, command := range commands {
		if command.Scope == NetworkOperation {
			t.Fatalf("unexpected network command: %#v", command)
		}
		if len(command.Args) == 0 {
			continue
		}
		switch command.Args[0] {
		case "fetch", "add", "commit", "merge", "push":
			t.Fatalf("unexpected sync mutation: %#v", command)
		}
	}
}

func TestSyncSHA256UsesFullExactOIDs(t *testing.T) {
	probe := t.TempDir()
	if output, err := runFixtureGit(probe, "init", "--object-format=sha256", "--initial-branch", "main"); err != nil {
		lower := strings.ToLower(output)
		if strings.Contains(lower, "unknown option") || strings.Contains(lower, "unsupported") || strings.Contains(lower, "not support") {
			t.Skipf("installed Git does not support SHA-256 repositories: %v (%s)", err, output)
		}
		t.Fatalf("could not probe SHA-256 repository support: %v (%s)", err, output)
	}
	fixture := newConnectFixtureWithObjectFormat(t, "sha256")
	fixture.seedRemote()
	fixture.git(fixture.root, "init", "--object-format=sha256", "--initial-branch", "main")
	fixture.git(fixture.root, "config", "user.name", "IGoNotes Test")
	fixture.git(fixture.root, "config", "user.email", "igonotes@example.invalid")
	connected, err := fixture.initialize(fixture.options(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	options := syncOptions(fixture, connected.RemoteOID)
	fixture.write("sha256.md", "sha256\n")
	runner := &interceptRunner{delegate: NewCommandRunner()}
	result, err := runSync(t, fixture, runner, options, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.PushOID) != 64 || len(result.RemoteOID) != 64 {
		t.Fatalf("SHA-256 result = %#v", result)
	}
	pushes := servicePushes(runner.commands)
	if len(pushes) != 1 || pushes[0].Args[len(pushes[0].Args)-1] != result.PushOID+":refs/heads/main" {
		t.Fatalf("SHA-256 pushes = %#v", pushes)
	}
	recovered, err := runRecovery(t, fixture, nil, recoveryOptions(options, nil))
	if err != nil || recovered.HeadOID != result.PushOID || recovered.RemoteOID != result.PushOID {
		t.Fatalf("SHA-256 recovery = %#v, %v", recovered, err)
	}
}
