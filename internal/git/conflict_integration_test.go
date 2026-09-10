package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"IGoNotes/internal/model"
)

func TestResolveLocalIntegration(t *testing.T) {
	resolveContentConflictIntegration(t, model.GitConflictUseLocal, "local\n")
}

func TestResolveRemoteIntegration(t *testing.T) {
	resolveContentConflictIntegration(t, model.GitConflictUseRemote, "remote\n")
}

func TestResolveManualIntegration(t *testing.T) {
	resolveContentConflictIntegration(t, model.GitConflictManual, "manual\n")
}

func TestResolveDeleteIntegration(t *testing.T) {
	fixture := newConflictFixture(t)
	writeConflictFile(t, fixture.Local, "victim.md", []byte("local\n"))
	conflictCommit(t, fixture.Local, "local modify")
	removeConflictFile(t, fixture.Other, "victim.md")
	conflictCommit(t, fixture.Other, "remote delete")
	fixture.pushOther(t)
	fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))
	service, base, operation := conflictResolver(t, fixture)
	snapshot, err := service.Conflicts(context.Background(), base, operation)
	if err != nil || len(snapshot.Conflicts) != 1 {
		t.Fatalf("Conflicts() = %#v, %v", snapshot, err)
	}
	conflict := snapshot.Conflicts[0]
	request := model.GitConflictResolveRequest{
		OperationID: operation.ID,
		ConflictID:  conflict.ID,
		Path:        conflict.Path,
		Action:      model.GitConflictDelete,
	}
	events := []string{}
	resolved, err := service.ResolveConflict(context.Background(), base, operation, request, callbackOrderingTransaction(fixture.Local, &events))
	if err != nil || !resolved.CanComplete || len(resolved.Conflicts) != 0 {
		t.Fatalf("ResolveConflict() = %#v, %v", resolved, err)
	}
	if !reflect.DeepEqual(events, []string{"mutate", "callback_returned", "unlock"}) {
		t.Fatalf("transaction events = %#v", events)
	}
	assertConflictStages(t, fixture, map[string][]int{})
}

func TestResolveAddAddKeepBothIntegration(t *testing.T) {
	fixture := newConflictFixture(t)
	local := []byte{0, 1, 2, 3, 4}
	remote := []byte{5, 6, 7, 8, 9}
	writeConflictFile(t, fixture.Local, "photo.png", local)
	conflictCommit(t, fixture.Local, "local add image")
	writeConflictFile(t, fixture.Other, "photo.png", remote)
	conflictCommit(t, fixture.Other, "remote add image")
	fixture.pushOther(t)
	fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

	service, base, operation := conflictResolver(t, fixture)
	snapshot, err := service.Conflicts(context.Background(), base, operation)
	if err != nil || len(snapshot.Conflicts) != 1 {
		t.Fatalf("Conflicts() = %#v, %v", snapshot, err)
	}
	conflict := snapshot.Conflicts[0]
	if conflict.Kind != ConflictAddAdd || conflict.Local == nil || conflict.Remote == nil {
		t.Fatalf("add/add conflict = %#v", conflict)
	}
	request := model.GitConflictResolveRequest{
		OperationID: operation.ID, ConflictID: conflict.ID, Path: conflict.Path,
		Action:    model.GitConflictKeepBoth,
		LocalPath: "assets/images/photo-local.png", RemotePath: "assets/images/photo-remote.png",
		LocalOID: conflict.Local.OID, RemoteOID: conflict.Remote.OID,
	}
	resolved, err := service.ResolveConflict(context.Background(), base, operation, request, callbackOrderingTransaction(fixture.Local, new([]string)))
	if err != nil || !resolved.CanComplete || len(resolved.Conflicts) != 0 {
		t.Fatalf("ResolveConflict() = %#v, %v", resolved, err)
	}
	for name, want := range map[string][]byte{
		"assets/images/photo-local.png":  local,
		"assets/images/photo-remote.png": remote,
	} {
		got, err := os.ReadFile(filepath.Join(fixture.Local, name))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s = %v, %v; want %v", name, got, err, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(fixture.Local, "photo.png")); !os.IsNotExist(err) {
		t.Fatalf("unused source remains: %v", err)
	}
	assertConflictStages(t, fixture, map[string][]int{})
	assertStageZeroPaths(t, fixture, "assets/images/photo-local.png", "assets/images/photo-remote.png")
}

func TestResolveRenameDeleteIntegration(t *testing.T) {
	for _, orientation := range []struct {
		name        string
		localDelete bool
		renamed     string
	}{
		{name: "local retained", renamed: "local-renamed.md"},
		{name: "remote retained", localDelete: true, renamed: "remote-renamed.md"},
	} {
		t.Run(orientation.name, func(t *testing.T) {
			for _, test := range []struct {
				name   string
				output string
				manual bool
				delete bool
			}{
				{name: "retained renamed", output: orientation.renamed},
				{name: "alternate", output: "alternate.md"},
				{name: "manual", output: "manual.md", manual: true},
				{name: "delete", delete: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					fixture := newConflictFixture(t)
					if orientation.localDelete {
						removeConflictFile(t, fixture.Local, "victim.md")
						renameConflictFile(t, fixture.Other, "victim.md", orientation.renamed)
					} else {
						renameConflictFile(t, fixture.Local, "victim.md", orientation.renamed)
						removeConflictFile(t, fixture.Other, "victim.md")
					}
					conflictCommit(t, fixture.Local, "local rename delete")
					conflictCommit(t, fixture.Other, "remote rename delete")
					fixture.pushOther(t)
					fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

					service, base, operation := conflictResolver(t, fixture)
					snapshot, err := service.Conflicts(context.Background(), base, operation)
					if err != nil || len(snapshot.Conflicts) != 1 {
						t.Fatalf("Conflicts() = %#v, %v", snapshot, err)
					}
					conflict := snapshot.Conflicts[0]
					if conflict.Kind != ConflictRenameDelete || conflict.OriginalPath != "victim.md" {
						t.Fatalf("rename/delete conflict = %#v", conflict)
					}
					request := model.GitConflictResolveRequest{OperationID: operation.ID, ConflictID: conflict.ID, Path: conflict.Path}
					switch {
					case test.delete:
						request.Action = model.GitConflictDelete
					case test.manual:
						content := "manual text\n"
						request.Action, request.ResultPath, request.Content = model.GitConflictManual, test.output, &content
					case conflict.Local != nil:
						request.Action, request.ResultPath, request.LocalOID = model.GitConflictUseLocal, test.output, conflict.Local.OID
					default:
						request.Action, request.ResultPath, request.RemoteOID = model.GitConflictUseRemote, test.output, conflict.Remote.OID
					}
					resolved, err := service.ResolveConflict(context.Background(), base, operation, request, callbackOrderingTransaction(fixture.Local, new([]string)))
					if err != nil || !resolved.CanComplete || len(resolved.Conflicts) != 0 {
						t.Fatalf("ResolveConflict() = %#v, %v", resolved, err)
					}
					if _, err := os.Lstat(filepath.Join(fixture.Local, "victim.md")); !os.IsNotExist(err) {
						t.Fatalf("original path remains: %v", err)
					}
					if test.delete {
						if _, err := os.Lstat(filepath.Join(fixture.Local, orientation.renamed)); !os.IsNotExist(err) {
							t.Fatalf("renamed path remains after delete: %v", err)
						}
					} else {
						contents, err := os.ReadFile(filepath.Join(fixture.Local, test.output))
						want := "base\n"
						if test.manual {
							want = "manual text\n"
						}
						if err != nil || string(contents) != want {
							t.Fatalf("%s = %q, %v; want %q", test.output, contents, err, want)
						}
						if test.output != orientation.renamed {
							if _, err := os.Lstat(filepath.Join(fixture.Local, orientation.renamed)); !os.IsNotExist(err) {
								t.Fatalf("unused renamed path remains: %v", err)
							}
						}
					}
					assertConflictStages(t, fixture, map[string][]int{})
				})
			}
		})
	}
}

func TestResolvePathspecLiteralIntegration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pathspec-looking filenames are not supported on Windows")
	}
	fixture := newConflictFixture(t)
	name := ":(glob)*.md"
	writeConflictFile(t, fixture.Local, name, []byte("local glob\n"))
	conflictCommit(t, fixture.Local, "local literal glob")
	writeConflictFile(t, fixture.Other, name, []byte("remote glob\n"))
	conflictCommit(t, fixture.Other, "remote literal glob")
	fixture.pushOther(t)
	fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

	service, base, operation := conflictResolver(t, fixture)
	snapshot, err := service.Conflicts(context.Background(), base, operation)
	if err != nil || len(snapshot.Conflicts) != 1 {
		t.Fatalf("Conflicts() = %#v, %v", snapshot, err)
	}
	conflict := snapshot.Conflicts[0]
	request := model.GitConflictResolveRequest{
		OperationID: operation.ID, ConflictID: conflict.ID, Path: conflict.Path,
		Action: model.GitConflictUseLocal, ResultPath: name, LocalOID: conflict.Local.OID,
	}
	resolved, err := service.ResolveConflict(context.Background(), base, operation, request, callbackOrderingTransaction(fixture.Local, new([]string)))
	if err != nil || !resolved.CanComplete || len(resolved.Conflicts) != 0 {
		t.Fatalf("ResolveConflict() = %#v, %v", resolved, err)
	}
	for _, contents := range [][]byte{
		mustReadConflictFile(t, filepath.Join(fixture.Local, "victim.md")),
		runConflictGit(t, fixture.Local, "show", ":victim.md"),
	} {
		if string(contents) != "base\n" {
			t.Fatalf("victim changed to %q", contents)
		}
	}
}

func TestResolveSymlinkIntegration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires Unix symlink support")
	}
	fixture := newConflictFixture(t)
	for _, test := range []struct {
		dir    string
		target string
		label  string
	}{
		{dir: fixture.Local, target: "local-target", label: "local symlink"},
		{dir: fixture.Other, target: "remote-target", label: "remote symlink"},
	} {
		if err := os.Remove(filepath.Join(test.dir, "victim.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(test.target, filepath.Join(test.dir, "victim.md")); err != nil {
			t.Fatal(err)
		}
		conflictCommit(t, test.dir, test.label)
	}
	fixture.pushOther(t)
	fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

	service, base, operation := conflictResolver(t, fixture)
	snapshot, err := service.Conflicts(context.Background(), base, operation)
	if err != nil || len(snapshot.Conflicts) != 1 {
		t.Fatalf("Conflicts() = %#v, %v", snapshot, err)
	}
	conflict := snapshot.Conflicts[0]
	if conflict.ContentKind != ContentBinary || conflict.Local == nil || conflict.Local.Mode != "120000" {
		t.Fatalf("symlink conflict = %#v", conflict)
	}
	request := model.GitConflictResolveRequest{
		OperationID: operation.ID, ConflictID: conflict.ID, Path: conflict.Path,
		Action: model.GitConflictUseLocal, ResultPath: "resolved-link", LocalOID: conflict.Local.OID,
	}
	resolved, err := service.ResolveConflict(context.Background(), base, operation, request, callbackOrderingTransaction(fixture.Local, new([]string)))
	if err != nil || !resolved.CanComplete {
		t.Fatalf("ResolveConflict() = %#v, %v", resolved, err)
	}
	target, err := os.Readlink(filepath.Join(fixture.Local, "resolved-link"))
	if err != nil || target != "local-target" {
		t.Fatalf("resolved symlink target = %q, %v", target, err)
	}
}

func TestCompleteConflictIntegration(t *testing.T) {
	fixture := newConflictFixture(t)
	writeConflictFile(t, fixture.Local, "victim.md", []byte("local\n"))
	conflictCommit(t, fixture.Local, "local conflict")
	writeConflictFile(t, fixture.Other, "victim.md", []byte("remote\n"))
	conflictCommit(t, fixture.Other, "remote conflict")
	fixture.pushOther(t)
	fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

	service, base, original := conflictResolver(t, fixture)
	base.URL = fixture.Remote
	snapshot, err := service.Conflicts(context.Background(), base, original)
	if err != nil || len(snapshot.Conflicts) != 1 {
		t.Fatalf("Conflicts() = %#v, %v", snapshot, err)
	}
	conflict := snapshot.Conflicts[0]
	_, err = service.ResolveConflict(context.Background(), base, original, model.GitConflictResolveRequest{
		OperationID: original.ID, ConflictID: conflict.ID, Path: conflict.Path,
		Action: model.GitConflictUseLocal, ResultPath: "resolved.md", LocalOID: conflict.Local.OID,
	}, callbackOrderingTransaction(fixture.Local, new([]string)))
	if err != nil {
		t.Fatal(err)
	}

	hook := filepath.Join(fixture.Local, ".git", "hooks", "pre-push")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	completion := Operation{
		ID: "fedcba9876543210fedcba9876543210", BaseName: base.Name, RepoPath: base.Path, Branch: base.Branch,
		Kind: OperationConflictComplete, State: OperationRunning,
		LocalOID: original.LocalOID, CandidateOID: original.CandidateOID, RemoteOID: original.RemoteOID,
	}
	events := []string{}
	var checkpoints []Checkpoint
	transaction := func(_ context.Context, mutate func(string) error) error {
		events = append(events, "mutate")
		if err := mutate(fixture.Local); err != nil {
			return err
		}
		events = append(events, "reindex")
		writeConflictFile(t, fixture.Local, "late.md", []byte("not in merge commit\n"))
		events = append(events, "unlock")
		return nil
	}
	pushOID, err := service.CompleteConflict(context.Background(), base, completion, transaction, func(_ context.Context, checkpoint Checkpoint) error {
		checkpoints = append(checkpoints, checkpoint)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"mutate", "reindex", "unlock"}) {
		t.Fatalf("transaction events = %#v", events)
	}

	parents := strings.Fields(string(runConflictGit(t, fixture.Local, "rev-list", "--parents", "-n", "1", pushOID)))
	if !reflect.DeepEqual(parents, []string{pushOID, original.LocalOID, original.RemoteOID}) {
		t.Fatalf("merge parents = %#v", parents)
	}
	remoteOID := strings.TrimSpace(string(runConflictGit(t, fixture.Local, "--git-dir="+fixture.Remote, "rev-parse", "--verify", "refs/heads/"+fixture.Branch+"^{commit}")))
	trustedOID := strings.TrimSpace(string(runConflictGit(t, fixture.Local, "rev-parse", "--verify", managedRemoteRef(fixture.Branch)+"^{commit}")))
	if remoteOID != pushOID || trustedOID != pushOID {
		t.Fatalf("remote OID = %q, trusted OID = %q, want %q", remoteOID, trustedOID, pushOID)
	}
	if output, err := runConflictGitResult(fixture.Remote, []string{"show", "refs/heads/" + fixture.Branch + ":late.md"}); err == nil {
		t.Fatalf("late worktree edit was pushed: %q", output)
	}
	paths, err := parseNULPaths(Result{Stdout: string(runConflictGit(t, fixture.Local, "diff-tree", "-r", "--no-commit-id", "--name-only", "-z", original.LocalOID, pushOID))})
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 5 || checkpoints[1].PushOID != pushOID || !reflect.DeepEqual(checkpoints[1].ChangedPaths, paths) ||
		checkpoints[4].RemoteOID != pushOID || checkpoints[4].CandidateOID != pushOID {
		t.Fatalf("completion checkpoints = %#v", checkpoints)
	}
}

func TestCompleteConflictRejectsRemoteAdvanceWithoutLosingLocalMerge(t *testing.T) {
	fixture := newConflictFixture(t)
	writeConflictFile(t, fixture.Local, "victim.md", []byte("local\n"))
	conflictCommit(t, fixture.Local, "local conflict")
	writeConflictFile(t, fixture.Other, "victim.md", []byte("remote\n"))
	conflictCommit(t, fixture.Other, "remote conflict")
	fixture.pushOther(t)
	fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

	service, base, original := conflictResolver(t, fixture)
	base.URL = fixture.Remote
	snapshot, err := service.Conflicts(context.Background(), base, original)
	if err != nil || len(snapshot.Conflicts) != 1 {
		t.Fatalf("Conflicts() = %#v, %v", snapshot, err)
	}
	conflict := snapshot.Conflicts[0]
	_, err = service.ResolveConflict(context.Background(), base, original, model.GitConflictResolveRequest{
		OperationID: original.ID, ConflictID: conflict.ID, Path: conflict.Path,
		Action: model.GitConflictUseLocal, ResultPath: "resolved.md", LocalOID: conflict.Local.OID,
	}, callbackOrderingTransaction(fixture.Local, new([]string)))
	if err != nil {
		t.Fatal(err)
	}

	completion := Operation{
		ID: "fedcba9876543210fedcba9876543210", BaseName: base.Name, RepoPath: base.Path, Branch: base.Branch,
		Kind: OperationConflictComplete, State: OperationRunning,
		LocalOID: original.LocalOID, CandidateOID: original.CandidateOID, RemoteOID: original.RemoteOID,
	}
	var checkpoints []Checkpoint
	_, err = service.CompleteConflict(context.Background(), base, completion, func(_ context.Context, mutate func(string) error) error {
		if err := mutate(fixture.Local); err != nil {
			return err
		}
		writeConflictFile(t, fixture.Other, "remote-advance.md", []byte("advance\n"))
		conflictCommit(t, fixture.Other, "advance remote before push")
		fixture.pushOther(t)
		return nil
	}, func(_ context.Context, checkpoint Checkpoint) error {
		checkpoints = append(checkpoints, checkpoint)
		return nil
	})
	if !isPushRejected(err) {
		t.Fatalf("CompleteConflict() error = %#v, want push rejection", err)
	}
	localMerge := strings.TrimSpace(string(runConflictGit(t, fixture.Local, "rev-parse", "--verify", "HEAD^{commit}")))
	parents := strings.Fields(string(runConflictGit(t, fixture.Local, "rev-list", "--parents", "-n", "1", localMerge)))
	if !reflect.DeepEqual(parents, []string{localMerge, original.LocalOID, original.RemoteOID}) {
		t.Fatalf("local merge parents = %#v", parents)
	}
	remoteOID := strings.TrimSpace(string(runConflictGit(t, fixture.Local, "--git-dir="+fixture.Remote, "rev-parse", "--verify", "refs/heads/"+fixture.Branch+"^{commit}")))
	trustedOID := strings.TrimSpace(string(runConflictGit(t, fixture.Local, "rev-parse", "--verify", managedRemoteRef(fixture.Branch)+"^{commit}")))
	if remoteOID == localMerge || trustedOID != original.RemoteOID {
		t.Fatalf("remote OID = %q, trusted OID = %q, local merge = %q", remoteOID, trustedOID, localMerge)
	}
	if len(checkpoints) != 4 || checkpoints[3].Stage != StageConflictPushing || checkpoints[3].PushOID != localMerge || checkpoints[3].RemoteOID != original.RemoteOID {
		t.Fatalf("push-rejection checkpoints = %#v", checkpoints)
	}
}

func TestAbortConflictIntegration(t *testing.T) {
	fixture := newConflictFixture(t)
	writeConflictFile(t, fixture.Local, "victim.md", []byte("local\n"))
	conflictCommit(t, fixture.Local, "local conflict")
	writeConflictFile(t, fixture.Other, "victim.md", []byte("remote\n"))
	conflictCommit(t, fixture.Other, "remote conflict")
	fixture.pushOther(t)
	fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

	service, base, original := conflictResolver(t, fixture)
	runner := &interceptRunner{delegate: NewCommandRunner()}
	service = NewService(runner, NewClient(runner))
	abort := original
	abort.Kind = OperationConflictAbort
	abort.State = OperationRunning
	abort.Stage = StageConflictAborting
	abort.ConflictPaths = []string{"victim.md"}
	var checkpoints []Checkpoint
	events := []string{}
	err := service.AbortConflict(context.Background(), base, abort, func(_ context.Context, mutate func(string) error) error {
		events = append(events, "mutate")
		if err := mutate(fixture.Local); err != nil {
			return err
		}
		events = append(events, "reindex", "unlock")
		return nil
	}, func(_ context.Context, checkpoint Checkpoint) error {
		checkpoints = append(checkpoints, checkpoint)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"mutate", "reindex", "unlock"}) {
		t.Fatalf("transaction events = %#v", events)
	}
	if head := strings.TrimSpace(string(runConflictGit(t, fixture.Local, "rev-parse", "HEAD^{commit}"))); head != original.LocalOID {
		t.Fatalf("HEAD = %q, want %q", head, original.LocalOID)
	}
	if _, err := os.Stat(filepath.Join(fixture.Local, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("MERGE_HEAD remains after abort: %v", err)
	}
	if len(checkpoints) != 1 || checkpoints[0].Stage != StageConflictAborting || !reflect.DeepEqual(checkpoints[0].ChangedPaths, []string{"victim.md"}) ||
		!reflect.DeepEqual(checkpoints[0].ConflictPaths, abort.ConflictPaths) {
		t.Fatalf("abort checkpoints = %#v", checkpoints)
	}
	for _, command := range runner.commands {
		if command.Scope == NetworkOperation {
			t.Fatalf("AbortConflict() used a network command: %#v", command)
		}
	}
	assertConflictStages(t, fixture, map[string][]int{})
}

func TestAbortConflictRejectsInvalidIdentityAndPreconditions(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Operation)
	}{
		{name: "identity", mutate: func(operation *Operation) { operation.BaseName = "other" }},
		{name: "head", mutate: func(operation *Operation) { operation.LocalOID = operation.CandidateOID }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newConflictFixture(t)
			writeConflictFile(t, fixture.Local, "victim.md", []byte("local\n"))
			conflictCommit(t, fixture.Local, "local conflict")
			writeConflictFile(t, fixture.Other, "victim.md", []byte("remote\n"))
			conflictCommit(t, fixture.Other, "remote conflict")
			fixture.pushOther(t)
			fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

			service, base, operation := conflictResolver(t, fixture)
			operation.Kind = OperationConflictAbort
			operation.State = OperationRunning
			test.mutate(&operation)
			called := false
			err := service.AbortConflict(context.Background(), base, operation, func(_ context.Context, mutate func(string) error) error {
				called = true
				return mutate(fixture.Local)
			}, func(context.Context, Checkpoint) error { return nil })
			if err != ErrRecoveryRequired || called {
				t.Fatalf("AbortConflict() = %v, transaction called = %t", err, called)
			}
			if _, err := os.Stat(filepath.Join(fixture.Local, ".git", "MERGE_HEAD")); err != nil {
				t.Fatalf("MERGE_HEAD changed: %v", err)
			}
		})
	}
}

func TestConflictRestartIntegration(t *testing.T) {
	newRestartConflict := func(t *testing.T) (*conflictFixture, *Service, ConfiguredBase, Operation) {
		t.Helper()
		fixture := newConflictFixture(t)
		for _, path := range []string{"victim.md", "space name.md"} {
			writeConflictFile(t, fixture.Local, path, []byte("local\n"))
		}
		conflictCommit(t, fixture.Local, "local conflicts")
		for _, path := range []string{"victim.md", "space name.md"} {
			writeConflictFile(t, fixture.Other, path, []byte("remote\n"))
		}
		conflictCommit(t, fixture.Other, "remote conflicts")
		fixture.pushOther(t)
		fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))
		service, base, operation := conflictResolver(t, fixture)
		base.URL = fixture.Remote
		base.Fingerprint = "config-v1"
		base.RemoteFingerprint = "remote-v1"
		operation.ConfigFingerprint = base.Fingerprint
		operation.RemoteFingerprint = base.RemoteFingerprint
		return fixture, service, base, operation
	}

	recover := func(t *testing.T, service *Service, base ConfiguredBase, operation *Operation, want ConflictRecoveryState, blocking bool) RecoveryResult {
		t.Helper()
		runner := &interceptRunner{delegate: NewCommandRunner()}
		service = NewService(runner, NewClient(runner))
		result, _ := service.RecoverLocal(context.Background(), RecoveryOptions{Snapshot: base, Operation: operation})
		if result.ConflictState != want || result.Blocking != blocking {
			t.Fatalf("RecoverLocal() = %#v, want state %q blocking %t", result, want, blocking)
		}
		assertRecoveryLocalReadOnly(t, runner.commands)
		return result
	}

	resolveAll := func(t *testing.T, fixture *conflictFixture) {
		t.Helper()
		for _, path := range []string{"victim.md", "space name.md"} {
			writeConflictFile(t, fixture.Local, path, []byte("resolved\n"))
			runConflictGit(t, fixture.Local, "add", "--", path)
		}
	}

	t.Run("partial and all staged merges preserve recovery decisions", func(t *testing.T) {
		fixture, service, base, operation := newRestartConflict(t)
		writeConflictFile(t, fixture.Local, "victim.md", []byte("resolved\n"))
		runConflictGit(t, fixture.Local, "add", "--", "victim.md")
		recover(t, service, base, &operation, RecoveryConflict, true)
		writeConflictFile(t, fixture.Local, "space name.md", []byte("resolved\n"))
		runConflictGit(t, fixture.Local, "add", "--", "space name.md")
		recover(t, service, base, &operation, RecoveryCanComplete, true)
	})

	for _, test := range []struct {
		name  string
		stage Stage
		state ConflictRecoveryState
	}{
		{name: "crash after merge commit", stage: StageConflictCommitted, state: RecoveryNeedsReindex},
		{name: "crash after reindex", stage: StageConflictReindexed, state: RecoveryPushPending},
		{name: "crash during push", stage: StageConflictPushing, state: RecoveryPushUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, service, base, original := newRestartConflict(t)
			resolveAll(t, fixture)
			runConflictGit(t, fixture.Local, "commit", "--no-edit")
			operation := original
			operation.Kind = OperationConflictComplete
			operation.State = OperationRunning
			operation.Stage = test.stage
			operation.PushOID = strings.TrimSpace(string(runConflictGit(t, fixture.Local, "rev-parse", "HEAD")))
			recovered := recover(t, service, base, &operation, test.state, false)
			if recovered.PushOID != operation.PushOID {
				t.Fatalf("PushOID = %q, want %q", recovered.PushOID, operation.PushOID)
			}
		})
	}

	t.Run("completed abort before journal finish", func(t *testing.T) {
		fixture, service, base, operation := newRestartConflict(t)
		runConflictGit(t, fixture.Local, "merge", "--abort")
		operation.Kind = OperationConflictAbort
		operation.State = OperationRunning
		operation.Stage = StageConflictAborting
		recover(t, service, base, &operation, RecoveryAborted, false)
	})

	t.Run("unrelated external commit is ambiguous", func(t *testing.T) {
		fixture, service, base, original := newRestartConflict(t)
		resolveAll(t, fixture)
		runConflictGit(t, fixture.Local, "commit", "--no-edit")
		operation := original
		operation.Kind = OperationConflictComplete
		operation.State = OperationRunning
		operation.Stage = StageConflictCommitted
		operation.PushOID = strings.TrimSpace(string(runConflictGit(t, fixture.Local, "rev-parse", "HEAD")))
		writeConflictFile(t, fixture.Local, "external.md", []byte("external\n"))
		conflictCommit(t, fixture.Local, "external commit")
		recover(t, service, base, &operation, RecoveryAmbiguous, true)
	})

	t.Run("untouched index lock is retained", func(t *testing.T) {
		fixture, service, base, operation := newRestartConflict(t)
		lock := filepath.Join(fixture.Local, ".git", "index.lock")
		if err := os.WriteFile(lock, []byte("owned elsewhere\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		recover(t, service, base, &operation, RecoveryLocked, true)
		if contents, err := os.ReadFile(lock); err != nil || string(contents) != "owned elsewhere\n" {
			t.Fatalf("index lock = %q, %v", contents, err)
		}
	})
}

func resolveContentConflictIntegration(t *testing.T, action model.GitConflictAction, want string) {
	t.Helper()
	fixture := newConflictFixture(t)
	writeConflictFile(t, fixture.Local, "victim.md", []byte("local\n"))
	writeConflictFile(t, fixture.Local, "other.md", []byte("local other\n"))
	conflictCommit(t, fixture.Local, "local conflicts")
	writeConflictFile(t, fixture.Other, "victim.md", []byte("remote\n"))
	writeConflictFile(t, fixture.Other, "other.md", []byte("remote other\n"))
	conflictCommit(t, fixture.Other, "remote conflicts")
	fixture.pushOther(t)
	fixture.mergeCapturedOID(t, fixture.fetchManagedRemoteOID(t))

	service, base, operation := conflictResolver(t, fixture)
	snapshot, err := service.Conflicts(context.Background(), base, operation)
	if err != nil || len(snapshot.Conflicts) != 2 {
		t.Fatalf("Conflicts() = %#v, %v", snapshot, err)
	}
	var conflict Conflict
	for _, candidate := range snapshot.Conflicts {
		if candidate.Path == "victim.md" {
			conflict = candidate
			break
		}
	}
	if conflict.ID == "" {
		t.Fatal("victim conflict missing")
	}
	request := model.GitConflictResolveRequest{
		OperationID: operation.ID,
		ConflictID:  conflict.ID,
		Path:        conflict.Path,
		Action:      action,
		ResultPath:  "resolved.md",
	}
	switch action {
	case model.GitConflictUseLocal:
		request.LocalOID = conflict.Local.OID
	case model.GitConflictUseRemote:
		request.RemoteOID = conflict.Remote.OID
	case model.GitConflictManual:
		request.Content = &want
	}
	events := []string{}
	resolved, err := service.ResolveConflict(context.Background(), base, operation, request, callbackOrderingTransaction(fixture.Local, &events))
	if err != nil || len(resolved.Conflicts) != 1 || resolved.Conflicts[0].Path != "other.md" {
		t.Fatalf("ResolveConflict() = %#v, %v", resolved, err)
	}
	if !reflect.DeepEqual(events, []string{"mutate", "callback_returned", "unlock"}) {
		t.Fatalf("transaction events = %#v", events)
	}
	contents, err := os.ReadFile(filepath.Join(fixture.Local, "resolved.md"))
	if err != nil || string(contents) != want {
		t.Fatalf("resolved contents = %q, %v", contents, err)
	}
	assertConflictStages(t, fixture, map[string][]int{"other.md": {2, 3}})
}

func conflictResolver(t *testing.T, fixture *conflictFixture) (*Service, ConfiguredBase, Operation) {
	t.Helper()
	head := strings.TrimSpace(string(runConflictGit(t, fixture.Local, "rev-parse", "--verify", "HEAD^{commit}")))
	remote := strings.TrimSpace(string(runConflictGit(t, fixture.Local, "rev-parse", "--verify", "MERGE_HEAD^{commit}")))
	base := ConfiguredBase{Name: "notes", Path: fixture.Local, Branch: fixture.Branch}
	operation := Operation{
		ID: "0123456789abcdef0123456789abcdef", BaseName: base.Name, RepoPath: base.Path, Branch: base.Branch,
		Kind: OperationSync, State: OperationConflict, LocalOID: head, CandidateOID: remote, RemoteOID: remote,
	}
	runner := NewCommandRunner()
	return NewService(runner, NewClient(runner)), base, operation
}

func callbackOrderingTransaction(path string, events *[]string) WorktreeTransaction {
	// This only models transaction callback ordering; Task11 owns note-index reindexing.
	return func(_ context.Context, mutate func(string) error) error {
		*events = append(*events, "mutate")
		defer func() { *events = append(*events, "unlock") }()
		if err := mutate(path); err != nil {
			return err
		}
		*events = append(*events, "callback_returned")
		return nil
	}
}

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

func assertStageZeroPaths(t *testing.T, fixture *conflictFixture, paths ...string) {
	t.Helper()
	stages, err := parseIndexStagesZ(runConflictGit(t, fixture.Local, "ls-files", "--stage", "--full-name", "-z"))
	if err != nil {
		t.Fatal(err)
	}
	actual := make(map[string]struct{}, len(stages))
	for _, stage := range stages {
		if stage.Stage == 0 {
			actual[stage.Path] = struct{}{}
		}
	}
	for _, path := range paths {
		if _, found := actual[path]; !found {
			t.Fatalf("stage-0 path %q missing from %#v", path, actual)
		}
	}
}

func mustReadConflictFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
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
