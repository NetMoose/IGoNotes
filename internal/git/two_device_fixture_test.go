package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"IGoNotes/internal/model"
)

var twoDeviceOperationID atomic.Uint64

// A fatal helper rejection must be observable without failing the parent test.
type e2eHelperRejection struct{}
type e2eHelperReporter struct{}

func (e2eHelperReporter) Helper()      {}
func (e2eHelperReporter) Fatal(...any) { panic(e2eHelperRejection{}) }

func e2eRejected(call func()) (rejected bool) {
	defer func() {
		if value := recover(); value != nil {
			if _, ok := value.(e2eHelperRejection); !ok {
				panic(value)
			}
			rejected = true
		}
	}()
	call()
	return false
}

func TestTwoDeviceRelativeNameRequiresCleanSlashPath(t *testing.T) {
	for _, name := range []string{"note.md", "folder/renamed [two].md", "assets/images/проба.bin", "space \tname.md"} {
		if got := e2eRelativeName(t, name); got != filepath.FromSlash(name) {
			t.Fatal("relative name bytes changed")
		}
	}
	for _, name := range []string{"", ".", "/absolute.md", "../escape.md", "folder/../note.md",
		"./note.md", "folder//note.md", "folder/./note.md", "folder/", ".git/config", "folder/.GIT/config", "folder\\note.md"} {
		t.Run(fmt.Sprintf("invalid_%q", name), func(t *testing.T) {
			if !e2eRejected(func() { e2eRelativeName(e2eHelperReporter{}, name) }) {
				t.Fatal("invalid relative name was accepted")
			}
		})
	}
}

func TestTwoDeviceSyncResultPreservesTrustedOIDUnlessSuccessfulAndValid(t *testing.T) {
	old := strings.Repeat("a", 40)
	for _, entry := range []struct {
		name, oid string
		err       error
		accept    bool
	}{
		{name: "successful SHA1", oid: strings.Repeat("b", 40), accept: true},
		{name: "successful SHA256", oid: strings.Repeat("c", 64), accept: true},
		{name: "empty successful result"},
		{name: "malformed successful result", oid: "not-an-oid"},
		{name: "short successful result", oid: strings.Repeat("d", 39)},
		{name: "failed result with valid OID", oid: strings.Repeat("e", 40), err: errors.New("sync failed")},
	} {
		t.Run(entry.name, func(t *testing.T) {
			d := &twoDevice{trustedRemoteOID: old}
			d.rememberSyncResult(OperationResult{RemoteOID: entry.oid}, entry.err)
			want := old
			if entry.accept {
				want = entry.oid
			}
			if d.trustedRemoteOID != want {
				t.Fatal("sync result incorrectly advanced or cleared trusted OID")
			}
		})
	}
}

func TestTwoDeviceNULFieldsPreservesBytesAndRejectsMalformedRecords(t *testing.T) {
	want := []string{" space \tname\n.md", "проба\xff.md"}
	if got := e2eNULFields(t, strings.Join(want, "\x00")+"\x00"); !reflect.DeepEqual(got, want) {
		t.Fatal("NUL records did not preserve exact bytes")
	}
	if got := e2eNULFields(t, ""); len(got) != 0 {
		t.Fatal("empty output must represent zero records")
	}
	for _, output := range []string{"unterminated", "\x00", "one\x00\x00", "one\x00\x00two\x00"} {
		if !e2eRejected(func() { e2eNULFields(e2eHelperReporter{}, output) }) {
			t.Fatal("malformed NUL records were accepted")
		}
	}
}

func TestTwoDeviceSnapshotsFingerprintExplicitDefaultTemplate(t *testing.T) {
	f := newTwoDeviceFixture(t)
	for _, d := range []*twoDevice{f.one, f.two} {
		if d.snapshot.CommitTemplate != defaultSyncCommitTemplate {
			t.Fatal("device snapshot must explicitly select the default commit template")
		}
		s := d.snapshot
		if s.Fingerprint == twoDeviceFingerprint(s.Name, s.Path, s.URL, s.Branch, "false", "0", "") {
			t.Fatal("device fingerprint omitted the explicit commit template")
		}
	}
}

type twoDeviceFixture struct {
	runner         *CommandRunner
	client         *Client
	service        *Service
	parent, remote string
	one, two       *twoDevice
}

type twoDevice struct {
	snapshot         ConfiguredBase
	root             *os.Root
	trustedRemoteOID string
	operation        Operation
}

func newTwoDeviceFixture(t *testing.T) *twoDeviceFixture {
	t.Helper()
	runner := requireGit(t)
	parent := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		t.Setenv(key, parent)
	}
	config := "[user]\n\tname = Two Device Test\n\temail = two-device@example.invalid\n[commit]\n\tgpgsign = false\n[init]\n\tdefaultBranch = main\n[core]\n\tautocrlf = false\n"
	if err := os.WriteFile(filepath.Join(parent, ".gitconfig"), []byte(config), 0o600); err != nil {
		t.Fatal("cannot create test Git configuration")
	}
	f := &twoDeviceFixture{runner: runner, parent: parent, remote: filepath.Join(parent, "bare remote удалённый.git")}
	f.client = NewClient(runner)
	f.service = NewService(runner, f.client)
	if err := os.Mkdir(f.remote, 0o700); err != nil {
		t.Fatal("cannot create bare remote directory")
	}
	f.git(t, f.remote, "init", "--bare", "--initial-branch=main")
	f.one = f.device(t, "one")
	f.two = f.device(t, "two")
	return f
}

func (f *twoDeviceFixture) device(t *testing.T, name string) *twoDevice {
	t.Helper()
	path := filepath.Join(f.parent, "device "+name+" заметки")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal("cannot create device directory")
	}
	canonical, err := canonicalDirectory(path)
	if err != nil {
		t.Fatal("cannot canonicalize device directory")
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		t.Fatal("cannot open device root")
	}
	t.Cleanup(func() { root.Close() })
	s := ConfiguredBase{Name: name, Path: canonical, URL: f.remote, Branch: "main",
		CommitTemplate: defaultSyncCommitTemplate}
	s.Fingerprint = twoDeviceFingerprint(s.Name, s.Path, s.URL, s.Branch,
		strconv.FormatBool(s.AutoSync), strconv.Itoa(s.IntervalMinutes), s.CommitTemplate)
	s.RemoteFingerprint = twoDeviceFingerprint(s.Path, s.URL, s.Branch)
	return &twoDevice{snapshot: s, root: root}
}

func twoDeviceFingerprint(values ...string) string {
	h := sha256.New()
	for _, value := range values {
		_ = binary.Write(h, binary.BigEndian, uint64(len(value)))
		_, _ = io.WriteString(h, value)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (d *twoDevice) begin(kind OperationKind) {
	s := d.snapshot
	now := time.Now().UTC()
	d.operation = Operation{ID: fmt.Sprintf("%032x", twoDeviceOperationID.Add(1)),
		BaseName: s.Name, RepoPath: s.Path, Branch: s.Branch,
		ConfigFingerprint: s.Fingerprint, RemoteFingerprint: s.RemoteFingerprint,
		Kind: kind, State: OperationRunning, Stage: StageQueued, CreatedAt: now, UpdatedAt: now}
}

func (d *twoDevice) progress(_ context.Context, c Checkpoint) error {
	d.operation.Stage, d.operation.BackupRef = c.Stage, c.BackupRef
	d.operation.LocalOID, d.operation.CandidateOID = c.LocalOID, c.CandidateOID
	d.operation.RemoteOID, d.operation.PushOID = c.RemoteOID, c.PushOID
	d.operation.ChangedPaths = append([]string(nil), c.ChangedPaths...)
	d.operation.ConflictPaths = append([]string(nil), c.ConflictPaths...)
	d.operation.UpdatedAt = time.Now().UTC()
	return nil
}

func (d *twoDevice) worktree(_ context.Context, mutate func(string) error) error {
	path, err := canonicalDirectory(d.snapshot.Path)
	if err != nil || path != d.snapshot.Path {
		return &SafeError{Code: CodeRepositoryRoot, Message: "Device callback path is not canonical"}
	}
	return mutate(path)
}

func (f *twoDeviceFixture) initialize(t *testing.T, d *twoDevice, emptyRemote bool) {
	t.Helper()
	ctx := context.Background()
	local, err := f.client.InspectLocal(ctx, d.snapshot.Path)
	if err != nil || local.HasRepository || !local.IdentityConfigured {
		t.Fatal("initial device inspection failed")
	}
	remote, err := f.client.InspectRemote(ctx, d.snapshot.Path, d.snapshot.URL)
	if err != nil || remote.Empty != emptyRemote || !emptyRemote && !validObjectID(remote.Branches["main"]) {
		t.Fatal("initial remote inspection failed")
	}
	d.begin(OperationInitialize)
	probe := model.GitProbeResponse{Base: d.snapshot.Name, EmptyRemote: emptyRemote,
		RepositoryRootMatches: true,
		IdentityConfigured:    true, CanConfigure: true, WorkingTreeClean: local.WorkingTreeClean,
		RequiredMutations: model.GitRequiredMutations{CreateRepository: true, AddOrigin: true, CreateBranch: emptyRemote}}
	if !emptyRemote {
		probe.RemoteBranches = []string{"main"}
	}
	result, err := f.service.Initialize(ctx, InitializeOptions{Snapshot: d.snapshot, Operation: d.operation,
		Probe: probe, Confirmations: model.GitConfirmations{CreateRepository: true, ReplaceOrigin: true,
			CreateBranch: true, MergeHistories: true}}, d.worktree, d.progress)
	if err != nil {
		t.Fatalf("device %s initialization failed (error type %T)", d.snapshot.Name, err)
	}
	f.accept(t, d, result)
}

func (f *twoDeviceFixture) syncAttempt(d *twoDevice) (OperationResult, error) {
	d.begin(OperationSync)
	result, err := f.service.Sync(context.Background(), SyncOptions{
		Snapshot: d.snapshot, Operation: d.operation, LastRemoteOID: d.trustedRemoteOID}, d.worktree, d.progress)
	d.rememberSyncResult(result, err)
	return result, err
}

func (d *twoDevice) rememberSyncResult(result OperationResult, err error) {
	if err == nil && result.RemoteOID != "" && validObjectID(result.RemoteOID) {
		d.trustedRemoteOID = result.RemoteOID
	}
}

func (f *twoDeviceFixture) sync(t *testing.T, d *twoDevice) {
	t.Helper()
	result, err := f.syncAttempt(d)
	if err != nil {
		t.Fatalf("device %s sync failed at stage %s (error type %T)", d.snapshot.Name, d.operation.Stage, err)
	}
	f.accept(t, d, result)
}

func (f *twoDeviceFixture) accept(t *testing.T, d *twoDevice, result OperationResult) {
	t.Helper()
	if d.operation.Stage != StageCompleted || !validObjectID(result.RemoteOID) ||
		result.HeadOID != result.RemoteOID || result.PushOID != result.RemoteOID || len(result.ConflictPaths) != 0 {
		t.Fatalf("device %s did not complete successfully", d.snapshot.Name)
	}
	d.trustedRemoteOID = result.RemoteOID
}

func (f *twoDeviceFixture) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	scope := LocalOperation
	if args[0] == "clone" {
		scope = NetworkOperation
	}
	result, err := f.runner.Run(context.Background(), Command{Dir: dir, Args: args,
		Scope: scope, Secrets: []string{f.remote}})
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		t.Fatalf("two-device Git %s failed (error type %T)", args[0], err)
	}
	return result.Stdout
}

func e2eRelativeName(t interface {
	Helper()
	Fatal(...any)
}, name string) string {
	t.Helper()
	if !filepath.IsLocal(name) || strings.Contains(name, "\\") || name == "." || path.Clean(name) != name {
		t.Fatal("device file name must stay inside its root")
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if strings.EqualFold(part, ".git") || part == ".." {
			t.Fatal("device file name targets a forbidden component")
		}
	}
	return filepath.FromSlash(name)
}

func (d *twoDevice) write(t *testing.T, name string, contents []byte) {
	t.Helper()
	name = e2eRelativeName(t, name)
	if err := d.root.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal("cannot create device file parent")
	}
	if err := d.root.WriteFile(name, contents, 0o600); err != nil {
		t.Fatal("cannot write device file")
	}
}

func (d *twoDevice) rename(t *testing.T, old, new string) {
	t.Helper()
	if err := d.root.Rename(e2eRelativeName(t, old), e2eRelativeName(t, new)); err != nil {
		t.Fatal("cannot rename device file")
	}
}

func (d *twoDevice) remove(t *testing.T, name string) {
	t.Helper()
	if err := d.root.Remove(e2eRelativeName(t, name)); err != nil {
		t.Fatal("cannot remove device file")
	}
}

func (d *twoDevice) assertContents(t *testing.T, name string, want []byte) {
	t.Helper()
	got, err := d.root.ReadFile(e2eRelativeName(t, name))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("exact bytes differ for relative file %q", name)
	}
}

// Git's NUL records are byte strings: do not trim whitespace or normalize names.
func e2eNULFields(t interface {
	Helper()
	Fatal(...any)
}, output string) []string {
	t.Helper()
	if output == "" {
		return nil
	}
	if !strings.HasSuffix(output, "\x00") {
		t.Fatal("Git output lacks terminal NUL")
	}
	fields := strings.Split(output[:len(output)-1], "\x00")
	for _, field := range fields {
		if field == "" {
			t.Fatal("Git output contains an empty NUL record")
		}
	}
	return fields
}

func (f *twoDeviceFixture) assertConverged(t *testing.T, want map[string][]byte, deleted ...string) {
	t.Helper()
	clonePath := filepath.Join(f.parent, "fresh clone копия")
	f.git(t, f.parent, "clone", "--branch", "main", "--", f.remote, clonePath)
	root, err := os.OpenRoot(clonePath)
	if err != nil {
		t.Fatal("cannot open fresh clone root")
	}
	defer root.Close()
	clone := &twoDevice{snapshot: ConfiguredBase{Name: "fresh clone", Path: clonePath}, root: root}
	devices := []*twoDevice{f.one, f.two, clone}
	head := f.git(t, f.one.snapshot.Path, "rev-parse", "--verify", "HEAD")
	if !validObjectID(strings.TrimSuffix(head, "\n")) || head != f.git(t, f.remote, "rev-parse", "--verify", "refs/heads/main") {
		t.Fatal("device HEAD differs from bare remote main")
	}
	var names []string
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	var baseline []string
	for _, d := range devices {
		path := d.snapshot.Path
		if f.git(t, path, "rev-parse", "--verify", "HEAD") != head {
			t.Fatalf("%s HEAD did not converge", d.snapshot.Name)
		}
		tree := e2eNULFields(t, f.git(t, path, "ls-tree", "-r", "-z", "--full-tree", "HEAD"))
		var gotNames []string
		for _, record := range tree {
			metadata, name, ok := strings.Cut(record, "\t")
			fields := strings.Split(metadata, " ")
			if !ok || len(fields) != 3 || fields[1] != "blob" || !validObjectID(fields[2]) {
				t.Fatal("malformed recursive tree record")
			}
			gotNames = append(gotNames, name)
			blob := f.git(t, path, "cat-file", "blob", fields[2])
			if contents, exists := want[name]; !exists || !bytes.Equal([]byte(blob), contents) {
				t.Fatalf("blob bytes differ for relative file %q", name)
			}
		}
		sort.Strings(gotNames)
		if !reflect.DeepEqual(gotNames, names) {
			t.Fatal("recursive tree file names differ")
		}
		if baseline == nil {
			baseline = tree
		} else if !reflect.DeepEqual(tree, baseline) {
			t.Fatal("recursive tree modes, names or blob OIDs did not converge")
		}
		if len(e2eNULFields(t, f.git(t, path, "status", "--porcelain=v1", "-z", "--untracked-files=all"))) != 0 {
			t.Fatalf("%s worktree is not clean", d.snapshot.Name)
		}
		for name, contents := range want {
			d.assertContents(t, name, contents)
		}
		for _, name := range deleted {
			if _, err := d.root.Stat(e2eRelativeName(t, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("deleted relative file %q remains", name)
			}
		}
	}
}
