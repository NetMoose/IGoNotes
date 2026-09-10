package git

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"IGoNotes/internal/model"
)

type resolutionWrite struct {
	Path  string
	Field string
	Stage int
	OID   string
	Mode  string
	Data  []byte
}

type resolutionPlan struct {
	RemovePaths []string
	Writes      []resolutionWrite
	StagePaths  []string
}

type materializedResolutionWrite struct {
	resolutionWrite
	source string
	data   []byte
	skip   bool
}

type materializedResolutionWrites struct {
	root   *os.Root
	temp   string
	writes []materializedResolutionWrite
}

func (m *materializedResolutionWrites) Close() {
	if m == nil {
		return
	}
	if m.root != nil {
		_ = m.root.Close()
	}
	if m.temp != "" {
		_ = os.RemoveAll(m.temp)
	}
}

func (m *materializedResolutionWrites) open(write materializedResolutionWrite) (io.ReadCloser, error) {
	if write.source == "" {
		return io.NopCloser(bytes.NewReader(write.data)), nil
	}
	return m.root.Open(write.source)
}

func (s *Service) ResolveConflict(
	ctx context.Context,
	base ConfiguredBase,
	operation Operation,
	request model.GitConflictResolveRequest,
	worktree WorktreeTransaction,
) (ConflictSnapshot, error) {
	if request.OperationID == "" || request.OperationID != operation.ID {
		return ConflictSnapshot{Conflicts: []Conflict{}}, ErrConflictStale
	}
	if worktree == nil {
		return ConflictSnapshot{Conflicts: []Conflict{}}, ErrRecoveryRequired
	}

	snapshot, err := s.Conflicts(ctx, base, operation)
	if err != nil {
		return ConflictSnapshot{Conflicts: []Conflict{}}, err
	}
	conflict, ok, known := conflictForResolution(snapshot.Conflicts, request)
	if !ok {
		if known {
			return ConflictSnapshot{Conflicts: []Conflict{}}, ErrConflictStale
		}
		return ConflictSnapshot{Conflicts: []Conflict{}}, ErrConflictNotFound
	}
	plan, err := buildResolutionPlan(conflict, snapshot.Conflicts, request)
	if err != nil {
		return ConflictSnapshot{Conflicts: []Conflict{}}, err
	}

	var resolved ConflictSnapshot
	err = runWorktree(ctx, worktree, func(canonicalPath string) error {
		if canonicalPath != base.Path || !filepath.IsAbs(canonicalPath) {
			return ErrConflictStale
		}
		inside, err := s.Conflicts(ctx, base, operation)
		if err != nil {
			return err
		}
		current, ok, _ := conflictForResolution(inside.Conflicts, request)
		if !ok {
			return ErrConflictStale
		}
		currentPlan, err := buildResolutionPlan(current, inside.Conflicts, request)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(plan, currentPlan) {
			return ErrConflictStale
		}
		local, err := s.porcelain.InspectLocal(ctx, canonicalPath)
		if err != nil || validConflictInspection(base, local) != nil {
			return ErrRecoveryRequired
		}

		root, err := os.OpenRoot(canonicalPath)
		if err != nil {
			return ErrRecoveryRequired
		}
		defer root.Close()
		sources, err := resolutionSources(current)
		if err != nil {
			return err
		}
		if err := verifyResolutionDestinations(ctx, root, s.runner, canonicalPath, sources, currentPlan.Writes); err != nil {
			return err
		}
		writes, err := s.materializeResolutionWrites(ctx, local.GitDir, current.Path, currentPlan.Writes)
		if err != nil {
			return err
		}
		defer writes.Close()
		if err := s.verifyResolutionWriteOIDs(ctx, canonicalPath, writes); err != nil {
			return err
		}
		if err := writeNonSourceResolutionOutputs(root, sources, writes); err != nil {
			return err
		}
		if err := removeResolutionSources(root, currentPlan); err != nil {
			return err
		}
		if err := writeSourceResolutionOutputs(root, sources, writes); err != nil {
			return err
		}
		if err := s.stageConflictResolution(ctx, canonicalPath, currentPlan.StagePaths); err != nil {
			return err
		}
		resolved, err = s.Conflicts(ctx, base, operation)
		return err
	})
	if err != nil {
		return ConflictSnapshot{Conflicts: []Conflict{}}, err
	}
	return resolved, nil
}

func conflictForResolution(conflicts []Conflict, request model.GitConflictResolveRequest) (Conflict, bool, bool) {
	known := false
	for _, conflict := range conflicts {
		if conflict.ID == request.ConflictID && conflict.Path == request.Path {
			return conflict, true, true
		}
		if conflict.ID == request.ConflictID || conflict.Path == request.Path {
			known = true
		}
	}
	return Conflict{}, false, known
}

func (s *Service) materializeResolutionWrites(ctx context.Context, gitDir, sourcePath string, writes []resolutionWrite) (*materializedResolutionWrites, error) {
	temp, err := os.MkdirTemp("", "igonotes-conflict-resolution-")
	if err != nil {
		return nil, ErrRecoveryRequired
	}
	if err := os.Chmod(temp, 0o700); err != nil {
		_ = os.RemoveAll(temp)
		return nil, ErrRecoveryRequired
	}
	tempRoot, err := os.OpenRoot(temp)
	if err != nil {
		_ = os.RemoveAll(temp)
		return nil, ErrRecoveryRequired
	}
	entries := &materializedResolutionWrites{root: tempRoot, temp: temp, writes: make([]materializedResolutionWrite, 0, len(writes))}
	fail := func(err error) (*materializedResolutionWrites, error) {
		entries.Close()
		return nil, err
	}
	for _, write := range writes {
		entry := materializedResolutionWrite{resolutionWrite: write}
		if write.Stage == 0 {
			entry.data = append([]byte(nil), write.Data...)
			entries.writes = append(entries.writes, entry)
			continue
		}
		name, err := s.checkoutConflictStageEntry(ctx, gitDir, temp, sourcePath, write.Stage, write.Mode)
		if err != nil {
			return fail(err)
		}
		info, err := tempRoot.Lstat(name)
		if err != nil {
			return fail(ErrRecoveryRequired)
		}
		if write.Mode == "120000" {
			if info.Mode()&os.ModeSymlink == 0 {
				return fail(ErrRecoveryRequired)
			}
			target, err := tempRoot.Readlink(name)
			if err != nil || len(target) > conflictLinkTargetLimit || strings.IndexByte(target, 0) >= 0 {
				return fail(ErrRecoveryRequired)
			}
			entry.data = []byte(target)
		} else {
			if !info.Mode().IsRegular() {
				return fail(ErrRecoveryRequired)
			}
			entry.source = name
		}
		entries.writes = append(entries.writes, entry)
	}
	return entries, nil
}

const conflictLinkTargetLimit = 1 << 20

func (s *Service) verifyResolutionWriteOIDs(ctx context.Context, repo string, writes *materializedResolutionWrites) error {
	for _, write := range writes.writes {
		if write.Stage == 0 {
			continue
		}
		content, err := writes.open(write)
		if err != nil {
			return ErrRecoveryRequired
		}
		oid, hashErr := s.hashConflictContent(ctx, repo, content)
		closeErr := content.Close()
		if hashErr != nil || closeErr != nil {
			return ErrRecoveryRequired
		}
		if oid != write.OID {
			return ErrConflictStale
		}
	}
	return nil
}

func (s *Service) hashConflictContent(ctx context.Context, repo string, content io.Reader) (string, error) {
	result, err := s.runLocalInput(ctx, repo, true, content, "hash-object", "--stdin")
	if err != nil || result.StdoutTruncated || result.StderrTruncated || !strings.HasSuffix(result.Stdout, "\n") {
		return "", ErrRecoveryRequired
	}
	oid := strings.TrimSuffix(result.Stdout, "\n")
	if !validObjectID(oid) {
		return "", ErrRecoveryRequired
	}
	return oid, nil
}

func verifyResolutionDestinations(ctx context.Context, root *os.Root, runner Runner, canonicalRepoPath string, sources []string, writes []resolutionWrite) error {
	stageZero, err := resolutionStageZero(ctx, runner, canonicalRepoPath)
	if err != nil {
		return err
	}
	owned := resolutionSourceSet(sources)
	for _, write := range writes {
		_, err := verifyResolutionDestinationWithStageZero(ctx, root, runner, canonicalRepoPath, write, owned, stageZero)
		if err != nil {
			return err
		}
	}
	return nil
}

func verifyResolutionDestination(ctx context.Context, root *os.Root, runner Runner, canonicalRepoPath string, write resolutionWrite, owned map[string]struct{}) error {
	stageZero, err := resolutionStageZero(ctx, runner, canonicalRepoPath)
	if err != nil {
		return err
	}
	_, err = verifyResolutionDestinationWithStageZero(ctx, root, runner, canonicalRepoPath, write, owned, stageZero)
	return err
}

func resolutionStageZero(ctx context.Context, runner Runner, canonicalRepoPath string) (map[string]struct{}, error) {
	if runner == nil || !filepath.IsAbs(canonicalRepoPath) {
		return nil, ErrRecoveryRequired
	}
	result, err := runner.Run(ctx, Command{
		Dir: canonicalRepoPath, Args: []string{"ls-files", "--stage", "--full-name", "-z"}, Scope: LocalOperation, ReadOnly: true,
	})
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		return nil, ErrRecoveryRequired
	}
	stages, err := parseIndexStagesZ([]byte(result.Stdout))
	if err != nil {
		return nil, ErrRecoveryRequired
	}
	stageZero := make(map[string]struct{}, len(stages))
	for _, stage := range stages {
		if stage.Stage == 0 {
			stageZero[stage.Path] = struct{}{}
		}
	}
	return stageZero, nil
}

func verifyResolutionDestinationWithStageZero(ctx context.Context, root *os.Root, runner Runner, canonicalRepoPath string, write resolutionWrite, owned, stageZero map[string]struct{}) (bool, error) {
	name, err := cleanConflictPath(write.Path, write.Field)
	if err != nil || root == nil || !validResolutionMode(write.Mode) {
		if err != nil {
			return false, err
		}
		return false, ErrRecoveryRequired
	}
	if _, exists := owned[name]; exists {
		return false, nil
	}
	if _, exists := stageZero[name]; exists {
		return false, ErrConflictStale
	}
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, ErrRecoveryRequired
	}

	expected, err := resolutionWriteOID(ctx, runner, canonicalRepoPath, write)
	if err != nil {
		return false, err
	}
	var actual string
	if write.Mode == "120000" {
		if info.Mode()&os.ModeSymlink == 0 {
			return false, resolutionDestinationCollision(write)
		}
		target, err := root.Readlink(name)
		if err != nil {
			return false, ErrRecoveryRequired
		}
		actual, err = hashResolutionLinkTarget(ctx, runner, canonicalRepoPath, bytes.NewReader([]byte(target)))
	} else {
		if !info.Mode().IsRegular() {
			return false, resolutionDestinationCollision(write)
		}
		content, err := root.Open(name)
		if err != nil {
			return false, ErrRecoveryRequired
		}
		actual, err = hashResolutionContent(ctx, runner, canonicalRepoPath, name, content)
		closeErr := content.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}
	if err != nil {
		return false, ErrRecoveryRequired
	}
	if actual != expected {
		return false, resolutionDestinationCollision(write)
	}
	return true, nil
}

func resolutionWriteOID(ctx context.Context, runner Runner, canonicalRepoPath string, write resolutionWrite) (string, error) {
	if write.Stage != 0 {
		if !validObjectID(write.OID) {
			return "", ErrRecoveryRequired
		}
		return write.OID, nil
	}
	return hashResolutionContent(ctx, runner, canonicalRepoPath, write.Path, bytes.NewReader(write.Data))
}

func hashResolutionContent(ctx context.Context, runner Runner, canonicalRepoPath, name string, content io.Reader) (string, error) {
	return hashResolutionInput(ctx, runner, canonicalRepoPath, []string{"hash-object", "--path=" + name, "--stdin"}, content)
}

func hashResolutionLinkTarget(ctx context.Context, runner Runner, canonicalRepoPath string, content io.Reader) (string, error) {
	return hashResolutionInput(ctx, runner, canonicalRepoPath, []string{"hash-object", "--stdin"}, content)
}

func hashResolutionInput(ctx context.Context, runner Runner, canonicalRepoPath string, args []string, content io.Reader) (string, error) {
	result, err := runner.Run(ctx, Command{
		Dir: canonicalRepoPath, Args: args, Scope: LocalOperation, ReadOnly: true, Stdin: content,
	})
	if err != nil || result.StdoutTruncated || result.StderrTruncated || !strings.HasSuffix(result.Stdout, "\n") {
		return "", ErrRecoveryRequired
	}
	oid := strings.TrimSuffix(result.Stdout, "\n")
	if !validObjectID(oid) {
		return "", ErrRecoveryRequired
	}
	return oid, nil
}

func resolutionDestinationCollision(write resolutionWrite) error {
	field := write.Field
	if field == "" {
		field = "result_path"
	}
	return newConflictFieldError("Conflict destination already exists", field)
}

func verifyResolutionDestinationOwnership(root *os.Root, sources []string, writes []resolutionWrite) error {
	owned := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		owned[source] = struct{}{}
	}
	for _, write := range writes {
		_, exists, err := conflictEntryExists(root, write.Path, write.Mode)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if _, isOwned := owned[write.Path]; !isOwned {
			return ErrConflictStale
		}
	}
	return nil
}

func conflictEntryExists(root *os.Root, name, mode string) (bool, bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if mode == "120000" {
		return info.Mode()&os.ModeSymlink != 0, true, nil
	}
	return info.Mode().IsRegular(), true, nil
}

func conflictEntryMatches(root *os.Root, name, mode string, want io.Reader) (bool, bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if mode == "120000" {
		if info.Mode()&os.ModeSymlink == 0 {
			return false, true, nil
		}
		target, err := root.Readlink(name)
		if err != nil {
			return false, true, err
		}
		expected, err := readConflictLinkTarget(want)
		return err == nil && target == string(expected), true, err
	}
	if !info.Mode().IsRegular() {
		return false, true, nil
	}
	actual, err := root.Open(name)
	if err != nil {
		return false, true, err
	}
	match, compareErr := equalConflictContent(actual, want)
	closeErr := actual.Close()
	if compareErr != nil {
		return false, true, compareErr
	}
	return match, true, closeErr
}

func writeNonSourceResolutionOutputs(root *os.Root, sources []string, writes *materializedResolutionWrites) error {
	owned := resolutionSourceSet(sources)
	for _, write := range writes.writes {
		if write.skip {
			continue
		}
		if _, isOwned := owned[write.Path]; isOwned {
			continue
		}
		if err := writeMaterializedConflictEntry(root, writes, write); err != nil {
			return err
		}
	}
	return nil
}

func writeSourceResolutionOutputs(root *os.Root, sources []string, writes *materializedResolutionWrites) error {
	owned := resolutionSourceSet(sources)
	for _, write := range writes.writes {
		if write.skip {
			continue
		}
		if _, isOwned := owned[write.Path]; !isOwned {
			continue
		}
		if err := removeMismatchedSourceResolutionOutput(root, writes, write); err != nil {
			return err
		}
		if err := writeMaterializedConflictEntry(root, writes, write); err != nil {
			return err
		}
	}
	return nil
}

func removeMismatchedSourceResolutionOutput(root *os.Root, writes *materializedResolutionWrites, write materializedResolutionWrite) error {
	content, err := writes.open(write)
	if err != nil {
		return ErrRecoveryRequired
	}
	matches, exists, matchErr := conflictEntryMatches(root, write.Path, write.Mode, content)
	closeErr := content.Close()
	if matchErr != nil || closeErr != nil {
		return ErrRecoveryRequired
	}
	if !exists || matches {
		return nil
	}
	info, err := root.Lstat(write.Path)
	if err != nil || (!info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0) {
		return ErrRecoveryRequired
	}
	if err := root.Remove(write.Path); err != nil {
		return err
	}
	return nil
}

func writeMaterializedConflictEntry(root *os.Root, writes *materializedResolutionWrites, write materializedResolutionWrite) error {
	content, err := writes.open(write)
	if err != nil {
		return ErrRecoveryRequired
	}
	writeErr := writeConflictEntry(root, write.Path, write.Mode, content)
	closeErr := content.Close()
	if writeErr != nil {
		if errors.Is(writeErr, os.ErrExist) {
			return ErrConflictStale
		}
		return writeErr
	}
	if closeErr != nil {
		return ErrRecoveryRequired
	}
	return nil
}

func resolutionSourceSet(sources []string) map[string]struct{} {
	owned := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		owned[source] = struct{}{}
	}
	return owned
}

func removeResolutionSources(root *os.Root, plan resolutionPlan) error {
	remove := make(map[string]struct{}, len(plan.RemovePaths))
	for _, name := range plan.RemovePaths {
		remove[name] = struct{}{}
	}
	names := make([]string, 0, len(remove))
	for name := range remove {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || (!info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0) {
			return ErrRecoveryRequired
		}
		if err := root.Remove(name); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) stageConflictResolution(ctx context.Context, path string, paths []string) error {
	input := make([]byte, 0)
	for _, value := range paths {
		input = append(input, value...)
		input = append(input, 0)
	}
	result, err := s.runLocalInput(ctx, path, false, bytes.NewReader(input),
		"--literal-pathspecs", "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul")
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		return ErrRecoveryRequired
	}
	return nil
}

func writeConflictEntry(root *os.Root, name, mode string, content io.Reader) error {
	if root == nil || !validResolutionMode(mode) {
		return ErrConflictUnsupported
	}
	name, err := cleanConflictPath(name, "path")
	if err != nil {
		return err
	}
	if err := createConflictParents(root, name); err != nil {
		return err
	}
	if mode == "120000" {
		target, err := readConflictLinkTarget(content)
		if err != nil {
			return err
		}
		info, err := root.Lstat(name)
		if err == nil {
			if info.Mode()&os.ModeSymlink == 0 {
				return os.ErrExist
			}
			existing, err := root.Readlink(name)
			if err != nil {
				return err
			}
			if existing == string(target) {
				return nil
			}
			return os.ErrExist
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return root.Symlink(string(target), name)
	}

	info, err := root.Lstat(name)
	if err == nil {
		if !info.Mode().IsRegular() {
			return os.ErrExist
		}
		existing, err := root.Open(name)
		if err != nil {
			return err
		}
		match, compareErr := equalConflictContent(existing, content)
		closeErr := existing.Close()
		if compareErr != nil {
			return compareErr
		}
		if closeErr != nil {
			return closeErr
		}
		if match {
			return root.Chmod(name, conflictFileMode(mode))
		}
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, conflictFileMode(mode))
	if err != nil {
		return err
	}
	_, writeErr := copyConflictContent(file, content)
	chmodErr := file.Chmod(conflictFileMode(mode))
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if chmodErr != nil {
		return chmodErr
	}
	return closeErr
}

func readConflictLinkTarget(content io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(content, conflictLinkTargetLimit+1))
	if err != nil || len(data) > conflictLinkTargetLimit || bytes.IndexByte(data, 0) >= 0 {
		return nil, ErrConflictUnsupported
	}
	return data, nil
}

func equalConflictContent(first, second io.Reader) (bool, error) {
	left := make([]byte, 32*1024)
	right := make([]byte, 32*1024)
	for {
		leftCount, leftErr := first.Read(left)
		rightCount, rightErr := second.Read(right)
		if leftCount != rightCount || !bytes.Equal(left[:leftCount], right[:rightCount]) {
			return false, nil
		}
		if leftErr == io.EOF && rightErr == io.EOF {
			return true, nil
		}
		if leftErr != nil && leftErr != io.EOF {
			return false, leftErr
		}
		if rightErr != nil && rightErr != io.EOF {
			return false, rightErr
		}
		if leftErr == io.EOF || rightErr == io.EOF {
			return false, nil
		}
	}
}

func copyConflictContent(destination io.Writer, source io.Reader) (int64, error) {
	return io.CopyBuffer(destination, source, make([]byte, 32*1024))
}

func createConflictParents(root *os.Root, name string) error {
	parts := strings.Split(filepath.FromSlash(name), string(filepath.Separator))
	for index := 1; index < len(parts); index++ {
		parent := filepath.Join(parts[:index]...)
		info, err := root.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(parent, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = root.Lstat(parent)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return os.ErrExist
		}
	}
	return nil
}

func conflictFileMode(mode string) os.FileMode {
	if mode == "100755" {
		return 0o755
	}
	return 0o644
}

func cleanConflictPath(value, field string) (string, error) {
	if value == "" {
		return "", newConflictFieldError("Conflict path is required", field)
	}
	if strings.IndexByte(value, 0) >= 0 || path.IsAbs(value) || filepath.IsAbs(filepath.FromSlash(value)) || path.Clean(value) != value || !filepath.IsLocal(filepath.FromSlash(value)) {
		return "", newConflictFieldError("Invalid conflict path", field)
	}
	for _, component := range strings.Split(filepath.FromSlash(value), string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." || strings.EqualFold(component, ".git") {
			return "", newConflictFieldError("Invalid conflict path", field)
		}
	}
	return value, nil
}

func validateResolution(conflict Conflict, request model.GitConflictResolveRequest) error {
	if request.ConflictID != conflict.ID || request.Path != conflict.Path {
		return ErrConflictStale
	}
	if !conflictAllows(conflict, request.Action) {
		return newConflictFieldError("Invalid conflict action", "action")
	}

	switch request.Action {
	case model.GitConflictUseLocal:
		if _, err := cleanConflictPath(request.ResultPath, "result_path"); err != nil {
			return err
		}
		if conflict.Local == nil {
			return newConflictFieldError("Local conflict stage is unavailable", "action")
		}
		if request.LocalOID != conflict.Local.OID || request.LocalOID == "" {
			return ErrConflictStale
		}
		return rejectResolutionFields(request, "content", "local_path", "remote_path", "remote_oid")
	case model.GitConflictUseRemote:
		if _, err := cleanConflictPath(request.ResultPath, "result_path"); err != nil {
			return err
		}
		if conflict.Remote == nil {
			return newConflictFieldError("Remote conflict stage is unavailable", "action")
		}
		if request.RemoteOID != conflict.Remote.OID || request.RemoteOID == "" {
			return ErrConflictStale
		}
		return rejectResolutionFields(request, "content", "local_path", "remote_path", "local_oid")
	case model.GitConflictManual:
		if _, err := cleanConflictPath(request.ResultPath, "result_path"); err != nil {
			return err
		}
		if request.Content == nil {
			return newConflictFieldError("Manual conflict content is required", "content")
		}
		return rejectResolutionFields(request, "local_path", "remote_path", "local_oid", "remote_oid")
	case model.GitConflictKeepBoth:
		localPath, err := cleanConflictPath(request.LocalPath, "local_path")
		if err != nil {
			return err
		}
		remotePath, err := cleanConflictPath(request.RemotePath, "remote_path")
		if err != nil {
			return err
		}
		if localPath == remotePath {
			return newConflictFieldError("Conflict output paths must differ", "remote_path")
		}
		if conflict.Local == nil || conflict.Remote == nil {
			return newConflictFieldError("Conflict stage is unavailable", "action")
		}
		if request.LocalOID != conflict.Local.OID || request.LocalOID == "" || request.RemoteOID != conflict.Remote.OID || request.RemoteOID == "" {
			return ErrConflictStale
		}
		return rejectResolutionFields(request, "result_path", "content")
	case model.GitConflictDelete:
		return rejectResolutionFields(request, "result_path", "content", "local_path", "remote_path", "local_oid", "remote_oid")
	default:
		return newConflictFieldError("Invalid conflict action", "action")
	}
}

func conflictAllows(conflict Conflict, action model.GitConflictAction) bool {
	for _, allowed := range conflict.Actions {
		if allowed == string(action) {
			return true
		}
	}
	return false
}

func rejectResolutionFields(request model.GitConflictResolveRequest, fields ...string) error {
	for _, field := range fields {
		var present bool
		switch field {
		case "result_path":
			present = request.ResultPath != ""
		case "content":
			present = request.Content != nil
		case "local_path":
			present = request.LocalPath != ""
		case "remote_path":
			present = request.RemotePath != ""
		case "local_oid":
			present = request.LocalOID != ""
		case "remote_oid":
			present = request.RemoteOID != ""
		}
		if present {
			return newConflictFieldError("Conflict field does not apply to selected action", field)
		}
	}
	return nil
}

func buildResolutionPlan(conflict Conflict, conflicts []Conflict, request model.GitConflictResolveRequest) (resolutionPlan, error) {
	if err := validateResolution(conflict, request); err != nil {
		return resolutionPlan{}, err
	}
	sources, err := resolutionSources(conflict)
	if err != nil {
		return resolutionPlan{}, err
	}

	plan := resolutionPlan{RemovePaths: []string{}, Writes: []resolutionWrite{}}
	switch request.Action {
	case model.GitConflictUseLocal:
		write, err := resolutionStageWrite(request.ResultPath, 2, conflict.Local)
		if err != nil {
			return resolutionPlan{}, err
		}
		plan.Writes = append(plan.Writes, write)
	case model.GitConflictUseRemote:
		write, err := resolutionStageWrite(request.ResultPath, 3, conflict.Remote)
		if err != nil {
			return resolutionPlan{}, err
		}
		plan.Writes = append(plan.Writes, write)
	case model.GitConflictManual:
		mode, err := resolutionManualMode(conflict)
		if err != nil {
			return resolutionPlan{}, err
		}
		plan.Writes = append(plan.Writes, resolutionWrite{Path: request.ResultPath, Mode: mode, Data: []byte(*request.Content)})
	case model.GitConflictKeepBoth:
		local, err := resolutionStageWrite(request.LocalPath, 2, conflict.Local)
		if err != nil {
			return resolutionPlan{}, err
		}
		remote, err := resolutionStageWrite(request.RemotePath, 3, conflict.Remote)
		if err != nil {
			return resolutionPlan{}, err
		}
		local.Field = "local_path"
		remote.Field = "remote_path"
		plan.Writes = append(plan.Writes, local, remote)
	case model.GitConflictDelete:
	default:
		return resolutionPlan{}, newConflictFieldError("Invalid conflict action", "action")
	}

	outputs := make(map[string]struct{}, len(plan.Writes))
	for _, write := range plan.Writes {
		outputs[write.Path] = struct{}{}
	}
	for _, source := range sources {
		if _, reused := outputs[source]; !reused {
			plan.RemovePaths = append(plan.RemovePaths, source)
		}
	}
	if err := rejectResolutionCollision(conflict, conflicts, append(append([]string(nil), sources...), resolutionWritePaths(plan.Writes)...)); err != nil {
		return resolutionPlan{}, err
	}
	return sortResolutionPlan(plan, sources), nil
}

func resolutionSources(conflict Conflict) ([]string, error) {
	primary, err := cleanConflictPath(conflict.Path, "path")
	if err != nil {
		return nil, err
	}
	sources := []string{primary}
	if conflict.OriginalPath == "" {
		return sources, nil
	}
	original, err := cleanConflictPath(conflict.OriginalPath, "original_path")
	if err != nil {
		return nil, err
	}
	if original != primary {
		sources = append(sources, original)
	}
	return sources, nil
}

func resolutionStageWrite(output string, stage int, source *ConflictStage) (resolutionWrite, error) {
	if source == nil || source.OID == "" || !validResolutionMode(source.Mode) {
		return resolutionWrite{}, newConflictFieldError("Conflict stage is unsupported", "action")
	}
	return resolutionWrite{Path: output, Stage: stage, OID: source.OID, Mode: source.Mode}, nil
}

func resolutionManualMode(conflict Conflict) (string, error) {
	if conflict.Local != nil {
		if !validResolutionMode(conflict.Local.Mode) {
			return "", newConflictFieldError("Conflict stage is unsupported", "action")
		}
		return conflict.Local.Mode, nil
	}
	if conflict.Remote != nil {
		if !validResolutionMode(conflict.Remote.Mode) {
			return "", newConflictFieldError("Conflict stage is unsupported", "action")
		}
		return conflict.Remote.Mode, nil
	}
	return "100644", nil
}

func validResolutionMode(mode string) bool {
	return mode == "100644" || mode == "100755" || mode == "120000"
}

func rejectResolutionCollision(selected Conflict, conflicts []Conflict, paths []string) error {
	requested := make(map[string]struct{}, len(paths))
	for _, value := range paths {
		requested[value] = struct{}{}
	}
	for _, other := range conflicts {
		if other.ID == selected.ID && other.Path == selected.Path && other.OriginalPath == selected.OriginalPath {
			continue
		}
		for _, value := range []string{other.Path, other.OriginalPath} {
			if value == "" {
				continue
			}
			if _, overlaps := requested[value]; overlaps {
				return newConflictFieldError("Conflict paths overlap another unresolved conflict", "path")
			}
		}
	}
	return nil
}

func resolutionWritePaths(writes []resolutionWrite) []string {
	paths := make([]string, 0, len(writes))
	for _, write := range writes {
		paths = append(paths, write.Path)
	}
	return paths
}

func sortResolutionPlan(plan resolutionPlan, sources []string) resolutionPlan {
	sort.Strings(plan.RemovePaths)
	sort.Slice(plan.Writes, func(i, j int) bool {
		if plan.Writes[i].Path != plan.Writes[j].Path {
			return plan.Writes[i].Path < plan.Writes[j].Path
		}
		if plan.Writes[i].Stage != plan.Writes[j].Stage {
			return plan.Writes[i].Stage < plan.Writes[j].Stage
		}
		if plan.Writes[i].OID != plan.Writes[j].OID {
			return plan.Writes[i].OID < plan.Writes[j].OID
		}
		return plan.Writes[i].Mode < plan.Writes[j].Mode
	})
	stagePaths := make(map[string]struct{}, len(plan.RemovePaths)+len(plan.Writes))
	for _, remove := range plan.RemovePaths {
		if len(sources) > 1 && remove == sources[1] {
			continue
		}
		stagePaths[remove] = struct{}{}
	}
	for _, write := range plan.Writes {
		stagePaths[write.Path] = struct{}{}
	}
	plan.StagePaths = make([]string, 0, len(stagePaths))
	for stagePath := range stagePaths {
		plan.StagePaths = append(plan.StagePaths, stagePath)
	}
	sort.Strings(plan.StagePaths)
	return plan
}
