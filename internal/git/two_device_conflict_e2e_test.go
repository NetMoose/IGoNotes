package git

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"IGoNotes/internal/model"
)

func TestTwoDeviceEndToEndConflictSurvivesRestartAndLosesNoData(t *testing.T) {
	f := newTwoDeviceFixture(t)
	one, two := f.one, f.two
	ctx := context.Background()
	for name, content := range map[string]string{"conflict.md": "base\n", "partial.md": "partial base\n"} {
		one.write(t, name, []byte(content))
	}
	f.initialize(t, one, true)
	f.initialize(t, two, false)
	baseOID := two.trustedRemoteOID
	one.write(t, "conflict.md", []byte("from one\n"))
	one.write(t, "partial.md", []byte("partial one\n"))
	f.sync(t, one)
	oneOID := one.trustedRemoteOID
	audit := &interceptRunner{delegate: f.runner}
	f.service = NewService(audit, NewClient(audit))
	two.write(t, "conflict.md", []byte("from two\n"))
	two.write(t, "partial.md", []byte("partial two\n"))
	_, err := f.syncAttempt(two)
	var conflictErr *ConflictError
	if !errors.As(err, &conflictErr) || !reflect.DeepEqual(conflictErr.Paths, []string{"conflict.md", "partial.md"}) {
		t.Fatal("sync did not return the exact sorted conflict paths")
	}
	original := two.operation
	original.State = OperationFailed
	headOID := strings.TrimSpace(f.git(t, two.snapshot.Path, "rev-parse", "HEAD"))
	twoOID := original.LocalOID
	assertNoPush := func() {
		t.Helper()
		for _, command := range audit.commands {
			if slices.Contains(command.Args, "push") {
				t.Fatal("conflict attempted push before resolution")
			}
		}
		if strings.TrimSpace(f.git(t, f.remote, "rev-parse", "refs/heads/main")) != oneOID ||
			f.git(t, f.remote, "show", "main:conflict.md") != "from one\n" ||
			f.git(t, f.remote, "show", "main:partial.md") != "partial one\n" {
			t.Fatal("unresolved conflict changed the remote")
		}
	}
	assertNoPush()
	if strings.TrimSpace(f.git(t, two.snapshot.Path, "rev-parse", "MERGE_HEAD")) != oneOID {
		t.Fatal("conflict lost MERGE_HEAD")
	}
	for name, wants := range map[string][]string{"conflict.md": {"base\n", "from two\n", "from one\n"}, "partial.md": {"partial base\n", "partial two\n", "partial one\n"}} {
		for i, want := range wants {
			if f.git(t, two.snapshot.Path, "show", ":"+strconv.Itoa(i+1)+":"+name) != want {
				t.Fatal("index stage lost original side bytes")
			}
		}
	}
	snapshot, err := f.service.Conflicts(ctx, two.snapshot, original)
	if original.Stage != StageMerging || original.LocalOID != headOID || original.RemoteOID != oneOID || original.CandidateOID != oneOID {
		t.Fatalf("conflict checkpoint did not freeze both side commits: stage=%s localIsPreSnapshot=%v localMatchesHead=%v remoteMatchesOne=%v candidateMatchesOne=%v originalCheckpointInspectionRejected=%v", original.Stage, original.LocalOID == baseOID, original.LocalOID == headOID, original.RemoteOID == oneOID, original.CandidateOID == oneOID, errors.Is(err, ErrRecoveryRequired))
	}
	if err != nil || len(snapshot.Conflicts) != 2 || snapshot.CanComplete {
		t.Fatal("cannot inspect both conflicts")
	}
	for i, c := range snapshot.Conflicts {
		name := []string{"conflict.md", "partial.md"}[i]
		if c.Path != name || c.Base == nil || c.Local == nil || c.Remote == nil {
			t.Fatal("conflict stages missing")
		}
	}
	resolve := func(service *Service, c Conflict, content string) ConflictSnapshot {
		t.Helper()
		got, err := service.ResolveConflict(ctx, two.snapshot, original, model.GitConflictResolveRequest{
			Base: two.snapshot.Name, OperationID: original.ID, ConflictID: c.ID, Path: c.Path,
			Action: model.GitConflictManual, ResultPath: c.Path, Content: &content}, two.worktree)
		if err != nil {
			t.Fatalf("manual resolution failed (error type %T)", err)
		}
		return got
	}
	partial := "partial one\npartial two\n"
	remaining := resolve(f.service, snapshot.Conflicts[1], partial)
	if remaining.CanComplete || len(remaining.Conflicts) != 1 || remaining.Conflicts[0].ID != snapshot.Conflicts[0].ID {
		t.Fatal("partial resolution changed the remaining conflict identity")
	}
	assertNoPush()
	// FETCH_HEAD is mutable and is deliberately made unrelated to the frozen remote.
	f.git(t, two.snapshot.Path, "update-ref", "FETCH_HEAD", baseOID)
	runner := NewCommandRunner()
	audit = &interceptRunner{delegate: runner}
	client := NewClient(audit)
	service := NewService(audit, client)
	recovered, err := service.RecoverLocal(ctx, RecoveryOptions{Snapshot: two.snapshot, Operation: &original})
	conflictErr = nil
	if !errors.As(err, &conflictErr) || !reflect.DeepEqual(conflictErr.Paths, []string{"conflict.md"}) ||
		!recovered.Blocking || recovered.ConflictState != RecoveryConflict || recovered.HeadOID != twoOID ||
		recovered.MergeHeadOID != oneOID || recovered.RemoteOID != oneOID {
		t.Fatal("fresh local recovery did not preserve the frozen conflict")
	}
	restarted, err := service.Conflicts(ctx, two.snapshot, original)
	if err != nil || len(restarted.Conflicts) != 1 || !reflect.DeepEqual(restarted.Conflicts[0], remaining.Conflicts[0]) {
		t.Fatal("restart changed the remaining conflict or its stage OIDs")
	}
	two.assertContents(t, "partial.md", []byte(partial))
	if f.git(t, two.snapshot.Path, "show", ":0:partial.md") != partial {
		t.Fatal("restart lost the stage-zero partial resolution")
	}
	assertNoPush()
	merged := "from one\nfrom two\n"
	complete := resolve(service, restarted.Conflicts[0], merged)
	if !complete.CanComplete || len(complete.Conflicts) != 0 {
		t.Fatal("final resolution cannot complete")
	}
	assertNoPush()
	var checkpoints []Checkpoint
	transactions := 0
	pushOID, err := service.CompleteConflict(ctx, two.snapshot, original,
		func(ctx context.Context, mutate func(string) error) error {
			transactions++
			return two.worktree(ctx, mutate)
		},
		func(ctx context.Context, c Checkpoint) error {
			checkpoints = append(checkpoints, c)
			return two.progress(ctx, c)
		})
	if err != nil || transactions != 1 || !validObjectID(pushOID) || len(checkpoints) == 0 {
		t.Fatalf("completion failed (error type %T)", err)
	}
	if two.operation.ID != original.ID || two.operation.Stage != StageCompleted || two.operation.PushOID != pushOID ||
		strings.TrimSpace(f.git(t, f.remote, "rev-parse", "refs/heads/main")) != pushOID {
		t.Fatal("completion did not checkpoint and push the exact merge OID")
	}
	pushes := 0
	for _, command := range audit.commands {
		if slices.Contains(command.Args, "push") {
			pushes++
			if !slices.Contains(command.Args, pushOID+":refs/heads/main") {
				t.Fatal("completion pushed a mutable ref instead of the exact OID")
			}
		}
	}
	if pushes != 1 {
		t.Fatal("completion did not perform exactly one push")
	}
	parents := strings.Fields(f.git(t, two.snapshot.Path, "rev-list", "--parents", "-n", "1", pushOID))
	if !reflect.DeepEqual(parents, []string{pushOID, twoOID, oneOID}) {
		t.Fatal("merge does not have the exact two parents")
	}
	for _, c := range checkpoints {
		if c.LocalOID != twoOID || c.BackupRef != original.BackupRef {
			t.Fatal("completion lost original checkpoint provenance")
		}
	}
	two.trustedRemoteOID = pushOID
	f.sync(t, one)
	f.assertConverged(t, map[string][]byte{"conflict.md": []byte(merged), "partial.md": []byte(partial)})
	for _, d := range []*twoDevice{one, two} {
		for oid, want := range map[string]string{oneOID: "from one\n", twoOID: "from two\n"} {
			f.git(t, d.snapshot.Path, "merge-base", "--is-ancestor", oid, "HEAD")
			if f.git(t, d.snapshot.Path, "show", oid+":conflict.md") != want {
				t.Fatal("reachable side commit lost bytes")
			}
			partialWant := "partial one\n"
			if oid == twoOID {
				partialWant = "partial two\n"
			}
			if f.git(t, d.snapshot.Path, "show", oid+":partial.md") != partialWant {
				t.Fatal("reachable side commit lost partial bytes")
			}
		}
	}
	if !strings.Contains(f.git(t, two.snapshot.Path, "reflog", "--format=%H", "HEAD"), twoOID) {
		t.Fatal("local side commit disappeared from the reflog")
	}
}
