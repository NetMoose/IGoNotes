package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConflictServiceReadsContentConflictWithoutWritingBase(t *testing.T) {
	base := t.TempDir()
	gitDir := filepath.Join(base, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const (
		baseOID   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		localOID  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		remoteOID = "cccccccccccccccccccccccccccccccccccccccc"
	)
	runner := &conflictRunner{responses: map[string]string{
		"status":   "u UU N... 100644 100644 100644 100644 " + baseOID + " " + localOID + " " + remoteOID + " note.md\x00",
		"unmerged": "100644 " + baseOID + " 1\tnote.md\x00100644 " + localOID + " 2\tnote.md\x00100644 " + remoteOID + " 3\tnote.md\x00",
		"stage":    "100644 " + baseOID + " 1\tnote.md\x00100644 " + localOID + " 2\tnote.md\x00100644 " + remoteOID + " 3\tnote.md\x00",
		"head":     baseOID + "\n",
		"merge":    remoteOID + "\n",
		"managed":  remoteOID + "\n",
		"bases":    baseOID + "\n",
		"attr":     "note.md\x00diff\x00unspecified\x00",
	}, contents: map[int]string{1: "base\n", 2: "local\n", 3: "remote\n"}}
	local := LocalInspection{
		HasRepository: true, RepositoryRoot: base, GitDir: gitDir, CurrentBranch: "main", PendingOperation: "merge",
	}
	service := NewService(runner, conflictPorcelain{local: local})
	op := Operation{
		ID: "0123456789abcdef0123456789abcdef", BaseName: "notes", RepoPath: base, Branch: "main", Kind: OperationSync, State: OperationConflict,
		LocalOID: baseOID, CandidateOID: remoteOID, RemoteOID: remoteOID,
	}

	snapshot, err := service.Conflicts(context.Background(), ConfiguredBase{Name: "notes", Path: base, Branch: "main"}, op)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.HeadOID != baseOID || snapshot.MergeHeadOID != remoteOID || len(snapshot.MergeBaseOIDs) != 1 || snapshot.MergeBaseOIDs[0] != baseOID || snapshot.CanComplete {
		t.Fatalf("snapshot metadata = %#v", snapshot)
	}
	if len(snapshot.Conflicts) != 1 {
		t.Fatalf("conflicts = %#v", snapshot.Conflicts)
	}
	conflict := snapshot.Conflicts[0]
	if conflict.Kind != ConflictContent || conflict.ContentKind != ContentText || conflict.Path != "note.md" || conflict.OriginalPath != "" || conflict.ID != "sha256:34897d696eb9401b7ee8c34c96fe55463fd7016808ae4c9a11af8c0e015b2d39" {
		t.Fatalf("conflict = %#v", conflict)
	}
	if conflict.Base == nil || conflict.Local == nil || conflict.Remote == nil || conflict.Base.Content == nil || conflict.Local.Content == nil || conflict.Remote.Content == nil || *conflict.Base.Content != "base\n" || *conflict.Local.Content != "local\n" || *conflict.Remote.Content != "remote\n" {
		t.Fatalf("stage previews = %#v", conflict)
	}
	if len(runner.calls) != 11 {
		t.Fatalf("runner calls = %d, want 11: %#v", len(runner.calls), runner.calls)
	}
	for _, command := range runner.calls {
		if command.Scope != LocalOperation || !command.ReadOnly {
			t.Fatalf("non-read-only command: %#v", command)
		}
	}
	if !hasArgs(runner.calls, "--no-optional-locks", "status", "--porcelain=v2", "-z", "--untracked-files=no", "--renames") ||
		!hasArgs(runner.calls, "ls-files", "--unmerged", "--stage", "--full-name", "-z") ||
		!hasArgs(runner.calls, "ls-files", "--stage", "--full-name", "-z") ||
		!hasArgs(runner.calls, "check-attr", "-z", "--stdin", "diff") {
		t.Fatalf("missing required inspection command: %#v", runner.calls)
	}
	for _, command := range runner.calls {
		if !strings.Contains(strings.Join(command.Args, "\x00"), "checkout-index") {
			continue
		}
		if len(command.Args) != 8 || !strings.HasPrefix(command.Args[0], "--git-dir=") || !strings.HasPrefix(command.Args[1], "--work-tree=") ||
			strings.Join(command.Args[2:], "\x00") != "--literal-pathspecs\x00checkout-index\x00--temp\x00--stage="+command.Args[5][len("--stage="):]+"\x00-z\x00--stdin" {
			t.Fatalf("checkout arguments = %#v", command.Args)
		}
	}
	for _, entry := range mustReadDir(t, base) {
		if entry.Name() != ".git" {
			t.Fatalf("inspection wrote configured base entry %q", entry.Name())
		}
	}
}

func TestConflictServiceRejectsTruncatedOutput(t *testing.T) {
	base := t.TempDir()
	gitDir := filepath.Join(base, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &conflictRunner{truncated: true}
	service := NewService(runner, conflictPorcelain{local: LocalInspection{
		HasRepository: true, RepositoryRoot: base, GitDir: gitDir, CurrentBranch: "main", PendingOperation: "merge",
	}})
	_, err := service.Conflicts(context.Background(), ConfiguredBase{Name: "notes", Path: base, Branch: "main"}, Operation{ID: "0123456789abcdef0123456789abcdef", BaseName: "notes", RepoPath: base, Branch: "main", Kind: OperationSync, State: OperationConflict, LocalOID: strings.Repeat("a", 40), CandidateOID: strings.Repeat("b", 40), RemoteOID: strings.Repeat("b", 40)})
	if err != ErrRecoveryRequired {
		t.Fatalf("Conflicts() error = %v, want ErrRecoveryRequired", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("truncated output did not reach runner: %#v", runner.calls)
	}
}

func TestConflictServiceRejectsNonConflictOperation(t *testing.T) {
	base := t.TempDir()
	gitDir := filepath.Join(base, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &conflictRunner{}
	service := NewService(runner, conflictPorcelain{local: LocalInspection{
		HasRepository: true, RepositoryRoot: base, GitDir: gitDir, CurrentBranch: "main", PendingOperation: "merge",
	}})
	op := Operation{
		ID: "0123456789abcdef0123456789abcdef", BaseName: "notes", RepoPath: base, Branch: "main", Kind: OperationSync, State: OperationRunning,
		LocalOID: strings.Repeat("a", 40), CandidateOID: strings.Repeat("b", 40), RemoteOID: strings.Repeat("b", 40),
	}
	_, err := service.Conflicts(context.Background(), ConfiguredBase{Name: "notes", Path: base, Branch: "main"}, op)
	if err != ErrRecoveryRequired {
		t.Fatalf("Conflicts() error = %v, want ErrRecoveryRequired", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("non-conflict operation ran Git command: %#v", runner.calls)
	}
}

func TestConflictServiceIgnoresUnrelatedInvalidUTF8RenameEvidence(t *testing.T) {
	base := t.TempDir()
	gitDir := filepath.Join(base, ".git")
	if err := os.Mkdir(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const (
		baseOID   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		localOID  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		remoteOID = "cccccccccccccccccccccccccccccccccccccccc"
	)
	invalid := string([]byte{0xff})
	runner := &conflictRunner{responses: map[string]string{
		"status":   "u UD N... 100644 100644 000000 100644 " + baseOID + " " + localOID + " " + remoteOID + " note.md\x00",
		"unmerged": "100644 " + baseOID + " 1\tnote.md\x00100644 " + localOID + " 2\tnote.md\x00",
		"stage":    "100644 " + baseOID + " 1\tnote.md\x00100644 " + localOID + " 2\tnote.md\x00",
		"head":     localOID + "\n", "merge": remoteOID + "\n", "managed": remoteOID + "\n", "bases": baseOID + "\n",
		"attr": "note.md\x00diff\x00unspecified\x00",
	}, diffByTip: map[string]string{
		localOID:  "M\x00note.md\x00R100\x00" + invalid + "\x00" + invalid + "\x00",
		remoteOID: "D\x00note.md\x00",
	}, contents: map[int]string{1: "base\n", 2: "local\n"}}
	service := NewService(runner, conflictPorcelain{local: LocalInspection{
		HasRepository: true, RepositoryRoot: base, GitDir: gitDir, CurrentBranch: "main", PendingOperation: "merge",
	}})
	snapshot, err := service.Conflicts(context.Background(), ConfiguredBase{Name: "notes", Path: base, Branch: "main"}, Operation{
		ID: "0123456789abcdef0123456789abcdef", BaseName: "notes", RepoPath: base, Branch: "main", Kind: OperationSync, State: OperationConflict,
		LocalOID: localOID, CandidateOID: remoteOID, RemoteOID: remoteOID,
	})
	if err != nil || len(snapshot.Conflicts) != 1 || snapshot.Conflicts[0].Kind != ConflictModifyDelete {
		t.Fatalf("Conflicts() = %#v, %v", snapshot, err)
	}
}

func TestConflictClassifiesLayoutsAndRenameDelete(t *testing.T) {
	stage := func(number int) indexStage {
		return indexStage{Path: "new.md", Mode: "100644", OID: strings.Repeat(string(rune('a'+number)), 40), Stage: number}
	}
	group := func(numbers ...int) conflictGroup {
		stages := make(map[int]indexStage, len(numbers))
		for _, number := range numbers {
			stages[number] = stage(number)
		}
		return conflictGroup{path: "new.md", stages: stages}
	}
	base := []string{strings.Repeat("a", 40)}
	tests := []struct {
		name          string
		group         conflictGroup
		bases         []string
		local, remote []nameStatus
		wantKind      ConflictKind
		wantOriginal  string
		wantErr       error
	}{
		{name: "content", group: group(1, 2, 3), bases: base, wantKind: ConflictContent},
		{name: "add add without base", group: group(2, 3), wantKind: ConflictAddAdd},
		{name: "modify delete local retained", group: group(1, 2), bases: base,
			local: []nameStatus{{Status: 'M', Path: "new.md"}}, remote: []nameStatus{{Status: 'D', Path: "new.md"}}, wantKind: ConflictModifyDelete},
		{name: "modify delete remote retained", group: group(1, 3), bases: base,
			local: []nameStatus{{Status: 'D', Path: "new.md"}}, remote: []nameStatus{{Status: 'M', Path: "new.md"}}, wantKind: ConflictModifyDelete},
		{name: "modify delete lacks retained evidence", group: group(1, 2), bases: base,
			remote: []nameStatus{{Status: 'D', Path: "new.md"}}, wantErr: ErrConflictStateAmbiguous},
		{name: "directory file evidence does not match conflict path", group: group(1, 2), bases: base,
			local: []nameStatus{{Status: 'M', Path: "node~other"}}, remote: []nameStatus{{Status: 'D', Path: "node"}}, wantErr: ErrConflictStateAmbiguous},
		{name: "rename delete local retained", group: group(1, 2), bases: base,
			local: []nameStatus{{Status: 'R', OldPath: "old.md", Path: "new.md"}}, remote: []nameStatus{{Status: 'D', Path: "old.md"}}, wantKind: ConflictRenameDelete, wantOriginal: "old.md"},
		{name: "rename delete remote retained", group: group(1, 3), bases: base,
			local: []nameStatus{{Status: 'D', Path: "old.md"}}, remote: []nameStatus{{Status: 'R', OldPath: "old.md", Path: "new.md"}}, wantKind: ConflictRenameDelete, wantOriginal: "old.md"},
		{name: "conflicting rename evidence", group: group(1, 2), bases: base,
			local: []nameStatus{{Status: 'R', OldPath: "old.md", Path: "new.md"}, {Status: 'R', OldPath: "other.md", Path: "new.md"}}, remote: []nameStatus{{Status: 'D', Path: "old.md"}}, wantErr: ErrConflictStateAmbiguous},
		{name: "rename ignores unrelated filtered records", group: group(1, 2), bases: base,
			local: []nameStatus{{Status: 'C', OldPath: "unrelated.md", Path: "other.md"}, {Status: 'R', OldPath: "old.md", Path: "new.md"}}, remote: []nameStatus{{Status: 'D', Path: "old.md"}, {Status: 'M', Path: "other.md"}}, wantKind: ConflictRenameDelete, wantOriginal: "old.md"},
		{name: "scoped copy evidence is ambiguous", group: group(1, 2), bases: base,
			local: []nameStatus{{Status: 'C', OldPath: "old.md", Path: "new.md"}}, remote: []nameStatus{{Status: 'D', Path: "old.md"}}, wantErr: ErrConflictStateAmbiguous},
		{name: "modify delete with multiple bases", group: group(1, 2), bases: append(base, strings.Repeat("b", 40)), wantErr: ErrConflictStateAmbiguous},
		{name: "content without base is ambiguous", group: group(1, 2, 3), wantErr: ErrConflictStateAmbiguous},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kind, original, err := classifyConflict(test.group, test.bases, test.local, test.remote)
			if err != test.wantErr || kind != test.wantKind || original != test.wantOriginal {
				t.Fatalf("classifyConflict() = %q, %q, %v; want %q, %q, %v", kind, original, err, test.wantKind, test.wantOriginal, test.wantErr)
			}
		})
	}
}

func TestConflictReaderFiltersDiffTreeEvidence(t *testing.T) {
	runner := &conflictRunner{}
	service := NewService(runner, unusedPorcelain{})
	for _, tip := range []string{strings.Repeat("b", 40), strings.Repeat("c", 40)} {
		if _, err := service.conflictNameStatus(context.Background(), t.TempDir(), strings.Repeat("a", 40), tip); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range runner.calls {
		if !hasArgs([]Command{command}, "diff-tree", "--no-commit-id", "-r", "--name-status", "-z", "--find-renames", "--diff-filter=DMRC", strings.Repeat("a", 40), command.Args[len(command.Args)-1]) {
			t.Fatalf("diff-tree arguments = %#v", command.Args)
		}
	}
}

func TestConflictClassifiesUnsupportedPathsAndGitlinks(t *testing.T) {
	oid := strings.Repeat("a", 40)
	_, err := conflictStageGroups(
		[]indexStage{{Path: "missing.md", Mode: "100644", OID: oid, Stage: 2}, {Path: "missing.md", Mode: "100644", OID: oid, Stage: 3}},
		nil,
		[]porcelainEntry{{RecordType: 'u', Path: "missing.md"}},
	)
	if err != ErrConflictStateAmbiguous {
		t.Fatalf("missing full-index stage error = %v, want ErrConflictStateAmbiguous", err)
	}
	_, err = conflictStageGroups(
		[]indexStage{{Path: string([]byte{0xff}), Mode: "100644", OID: oid, Stage: 2}, {Path: string([]byte{0xff}), Mode: "100644", OID: oid, Stage: 3}},
		nil,
		[]porcelainEntry{{RecordType: 'u', Path: string([]byte{0xff})}},
	)
	if err != ErrConflictUnsupported {
		t.Fatalf("invalid UTF-8 error = %v, want ErrConflictUnsupported", err)
	}
	_, err = conflictStageGroups(
		[]indexStage{{Path: "submodule", Mode: "160000", OID: oid, Stage: 1}, {Path: "submodule", Mode: "160000", OID: oid, Stage: 2}},
		nil,
		[]porcelainEntry{{RecordType: 'u', Path: "submodule"}},
	)
	if err != ErrConflictStateAmbiguous {
		t.Fatalf("gitlink error = %v, want ErrConflictStateAmbiguous", err)
	}
}

func TestConflictReaderRequiresMatchingPorcelainXY(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, test := range []struct {
		name   string
		stages []int
		xy     string
		wantOK bool
	}{
		{name: "UU", stages: []int{1, 2, 3}, xy: "UU", wantOK: true},
		{name: "AA", stages: []int{2, 3}, xy: "AA", wantOK: true},
		{name: "UD", stages: []int{1, 2}, xy: "UD", wantOK: true},
		{name: "DU", stages: []int{1, 3}, xy: "DU", wantOK: true},
		{name: "wrong xy", stages: []int{1, 2, 3}, xy: "AA"},
		{name: "unsupported xy", stages: []int{1, 2}, xy: "DD"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stages := make([]indexStage, 0, len(test.stages))
			for _, number := range test.stages {
				stages = append(stages, indexStage{Path: "conflict.md", Mode: "100644", OID: oid, Stage: number})
			}
			_, err := conflictStageGroups(stages, stages, []porcelainEntry{{RecordType: 'u', XY: test.xy, Path: "conflict.md"}})
			if (err == nil) != test.wantOK {
				t.Fatalf("conflictStageGroups() error = %v, want success %v", err, test.wantOK)
			}
		})
	}
}

func TestConflictClassifiesActionsFollowMatrix(t *testing.T) {
	tests := []struct {
		name       string
		kind       ConflictKind
		content    ContentKind
		localAlive bool
		want       string
	}{
		{name: "content text", kind: ConflictContent, content: ContentText, want: "local,remote,manual"},
		{name: "content binary", kind: ConflictContent, content: ContentBinary, want: "local,remote,keep_both"},
		{name: "add add text", kind: ConflictAddAdd, content: ContentText, want: "local,remote,manual,keep_both"},
		{name: "add add binary", kind: ConflictAddAdd, content: ContentBinary, want: "local,remote,keep_both"},
		{name: "modify delete local text", kind: ConflictModifyDelete, content: ContentText, localAlive: true, want: "local,manual,delete"},
		{name: "modify delete remote text", kind: ConflictModifyDelete, content: ContentText, want: "remote,manual,delete"},
		{name: "modify delete local binary", kind: ConflictModifyDelete, content: ContentBinary, localAlive: true, want: "local,delete"},
		{name: "rename delete local text", kind: ConflictRenameDelete, content: ContentText, localAlive: true, want: "local,manual,delete"},
		{name: "rename delete remote binary", kind: ConflictRenameDelete, content: ContentBinary, want: "remote,delete"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := strings.Join(conflictActions(test.kind, test.content, test.localAlive), ","); got != test.want {
				t.Fatalf("conflictActions() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestConflictPreviewMaterializesTextAndSuppressesBinary(t *testing.T) {
	for _, test := range []struct {
		name          string
		attribute     string
		contents      map[int]string
		wantKind      ContentKind
		wantTruncated bool
	}{
		{name: "large text", attribute: "unspecified", contents: map[int]string{1: strings.Repeat("a", conflictPreviewLimit+1), 2: "local", 3: "remote"}, wantKind: ContentText, wantTruncated: true},
		{name: "binary attribute", attribute: "unset", contents: map[int]string{1: "base", 2: "local", 3: "remote"}, wantKind: ContentBinary},
		{name: "binary bytes", attribute: "unspecified", contents: map[int]string{1: "base", 2: "local\x00bytes", 3: "remote"}, wantKind: ContentBinary},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			gitDir := filepath.Join(base, ".git")
			if err := os.Mkdir(gitDir, 0o700); err != nil {
				t.Fatal(err)
			}
			const (
				baseOID   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				localOID  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
				remoteOID = "cccccccccccccccccccccccccccccccccccccccc"
			)
			runner := &conflictRunner{responses: map[string]string{
				"status":   "u UU N... 100644 100644 100644 100644 " + baseOID + " " + localOID + " " + remoteOID + " note.md\x00",
				"unmerged": "100644 " + baseOID + " 1\tnote.md\x00100644 " + localOID + " 2\tnote.md\x00100644 " + remoteOID + " 3\tnote.md\x00",
				"stage":    "100644 " + baseOID + " 1\tnote.md\x00100644 " + localOID + " 2\tnote.md\x00100644 " + remoteOID + " 3\tnote.md\x00",
				"head":     baseOID + "\n", "merge": remoteOID + "\n", "managed": remoteOID + "\n", "bases": baseOID + "\n",
				"attr": "note.md\x00diff\x00" + test.attribute + "\x00",
			}, contents: test.contents}
			service := NewService(runner, conflictPorcelain{local: LocalInspection{HasRepository: true, RepositoryRoot: base, GitDir: gitDir, CurrentBranch: "main", PendingOperation: "merge"}})
			snapshot, err := service.Conflicts(context.Background(), ConfiguredBase{Name: "notes", Path: base, Branch: "main"}, Operation{ID: "0123456789abcdef0123456789abcdef", BaseName: "notes", RepoPath: base, Branch: "main", Kind: OperationSync, State: OperationConflict, LocalOID: baseOID, CandidateOID: remoteOID, RemoteOID: remoteOID})
			if err != nil {
				t.Fatal(err)
			}
			conflict := snapshot.Conflicts[0]
			if conflict.ContentKind != test.wantKind || conflict.Base.PreviewTruncated != test.wantTruncated || conflict.Base.Size != int64(len(test.contents[1])) {
				t.Fatalf("preview = %#v", conflict)
			}
			if test.wantKind == ContentBinary && (conflict.Base.Content != nil || conflict.Local.Content != nil || conflict.Remote.Content != nil) {
				t.Fatalf("binary preview leaked content: %#v", conflict)
			}
			if test.wantTruncated && conflict.Base.Content != nil {
				t.Fatalf("oversized text returned content: %#v", conflict.Base)
			}
			for _, command := range runner.calls {
				for _, arg := range command.Args {
					if temp, found := strings.CutPrefix(arg, "--work-tree="); found {
						if _, statErr := os.Stat(temp); !os.IsNotExist(statErr) {
							t.Fatalf("temporary root remains: %q, %v", temp, statErr)
						}
					}
				}
			}
		})
	}
}

func TestConflictPreviewRejectsInvalidUTF8WithinBoundedRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.txt")
	if err := os.WriteFile(path, append([]byte{0xff}, []byte(strings.Repeat("a", conflictPreviewLimit))...), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	content, truncated, binary, err := readConflictPreview(file, info)
	if err != nil || content != nil || truncated || !binary {
		t.Fatalf("readConflictPreview() = %#v, %v, %v, %v", content, truncated, binary, err)
	}
}

func TestConflictIdentityUsesKindPathsAndStageOIDs(t *testing.T) {
	conflict := Conflict{
		Kind: ConflictRenameDelete, Path: "new.md", OriginalPath: "old.md",
		Base:   &ConflictStage{OID: strings.Repeat("a", 40)},
		Local:  &ConflictStage{OID: strings.Repeat("b", 40)},
		Remote: &ConflictStage{OID: strings.Repeat("c", 40)},
	}
	if got, want := conflictIdentity(conflict), "sha256:8fdf82631cb628e85972fd515141872b28a14ad0be933afc6f6da528cd014a55"; got != want {
		t.Fatalf("conflictIdentity() = %q, want %q", got, want)
	}
	conflict.Path = "other.md"
	if conflictIdentity(conflict) == "sha256:8fdf82631cb628e85972fd515141872b28a14ad0be933afc6f6da528cd014a55" {
		t.Fatal("conflict identity does not include path")
	}
}

type conflictPorcelain struct{ local LocalInspection }

func (p conflictPorcelain) Version(context.Context, string) (Version, error)     { return Version{}, nil }
func (p conflictPorcelain) ValidateBranch(context.Context, string, string) error { return nil }
func (p conflictPorcelain) InspectLocal(context.Context, string) (LocalInspection, error) {
	return p.local, nil
}
func (p conflictPorcelain) InspectRemote(context.Context, string, string) (RemoteInspection, error) {
	return RemoteInspection{}, nil
}
func (p conflictPorcelain) HistoryRelation(context.Context, string, string) (string, error) {
	return "", nil
}

type conflictRunner struct {
	responses map[string]string
	diffByTip map[string]string
	contents  map[int]string
	calls     []Command
	truncated bool
}

func (r *conflictRunner) Run(_ context.Context, command Command) (Result, error) {
	r.calls = append(r.calls, command)
	if r.truncated {
		return Result{StdoutTruncated: true}, nil
	}
	if strings.Contains(strings.Join(command.Args, "\x00"), "checkout-index") {
		stage := 0
		for _, arg := range command.Args {
			if strings.HasPrefix(arg, "--stage=") {
				stage = int(arg[len("--stage=")] - '0')
			}
		}
		for _, arg := range command.Args {
			if temp, found := strings.CutPrefix(arg, "--work-tree="); found {
				if err := os.WriteFile(filepath.Join(temp, "temp-stage"), []byte(r.contents[stage]), 0o600); err != nil {
					return Result{}, err
				}
			}
		}
		return Result{Stdout: "temp-stage\tnote.md\x00"}, nil
	}
	switch {
	case startsArgs(command, "diff-tree"):
		return Result{Stdout: r.diffByTip[command.Args[len(command.Args)-1]]}, nil
	case startsArgs(command, "--no-optional-locks", "status"):
		return Result{Stdout: r.responses["status"]}, nil
	case startsArgs(command, "ls-files", "--unmerged"):
		return Result{Stdout: r.responses["unmerged"]}, nil
	case startsArgs(command, "ls-files", "--stage"):
		return Result{Stdout: r.responses["stage"]}, nil
	case hasArgs([]Command{command}, "rev-parse", "--verify", "HEAD^{commit}"):
		return Result{Stdout: r.responses["head"]}, nil
	case hasArgs([]Command{command}, "rev-parse", "--verify", "MERGE_HEAD^{commit}"):
		return Result{Stdout: r.responses["merge"]}, nil
	case hasArgs([]Command{command}, "rev-parse", "--verify", "refs/igonotes/remotes/main^{commit}"):
		return Result{Stdout: r.responses["managed"]}, nil
	case hasArgs([]Command{command}, "merge-base", "--all", "HEAD", "MERGE_HEAD"):
		return Result{Stdout: r.responses["bases"]}, nil
	case hasArgs([]Command{command}, "check-attr", "-z", "--stdin", "diff"):
		return Result{Stdout: r.responses["attr"]}, nil
	default:
		return Result{}, nil
	}
}

func hasArgs(commands []Command, want ...string) bool {
	for _, command := range commands {
		if strings.Join(command.Args, "\x00") == strings.Join(want, "\x00") {
			return true
		}
	}
	return false
}

func startsArgs(command Command, want ...string) bool {
	return len(command.Args) >= len(want) && strings.Join(command.Args[:len(want)], "\x00") == strings.Join(want, "\x00")
}

func mustReadDir(t *testing.T, path string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
