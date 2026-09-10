package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const conflictPreviewLimit = 1024 * 1024

type ConflictKind string

const (
	ConflictContent      ConflictKind = "content"
	ConflictAddAdd       ConflictKind = "add_add"
	ConflictModifyDelete ConflictKind = "modify_delete"
	ConflictRenameDelete ConflictKind = "rename_delete"
)

type ContentKind string

const (
	ContentText   ContentKind = "text"
	ContentBinary ContentKind = "binary"
)

type ConflictStage struct {
	Path             string
	OID              string
	Mode             string
	Size             int64
	Content          *string
	PreviewTruncated bool
}

type Conflict struct {
	ID           string
	Kind         ConflictKind
	ContentKind  ContentKind
	Path         string
	OriginalPath string
	Base         *ConflictStage
	Local        *ConflictStage
	Remote       *ConflictStage
	Actions      []string
}

type ConflictSnapshot struct {
	HeadOID       string
	MergeHeadOID  string
	MergeBaseOIDs []string
	Conflicts     []Conflict
	CanComplete   bool
}

func (s *Service) Conflicts(ctx context.Context, base ConfiguredBase, operation Operation) (snapshot ConflictSnapshot, err error) {
	snapshot.Conflicts = []Conflict{}
	defer func() {
		if err != nil {
			snapshot = ConflictSnapshot{Conflicts: []Conflict{}}
			if err != ErrConflictUnsupported {
				err = ErrRecoveryRequired
			}
		}
	}()
	if s == nil || s.runner == nil || s.porcelain == nil || !validConflictRequest(base, operation) {
		return snapshot, ErrRecoveryRequired
	}
	local, err := s.porcelain.InspectLocal(ctx, base.Path)
	if err != nil {
		return snapshot, err
	}
	if err := validConflictInspection(base, local); err != nil {
		return snapshot, err
	}
	if err := conflictMarkersClear(local.GitDir); err != nil {
		return snapshot, err
	}

	status, err := s.conflictRun(ctx, local.RepositoryRoot, "--no-optional-locks", "status", "--porcelain=v2", "-z", "--untracked-files=no", "--renames")
	if err != nil {
		return snapshot, err
	}
	porcelain, err := parsePorcelainV2Z([]byte(status.Stdout))
	if err != nil {
		return snapshot, err
	}
	unmergedResult, err := s.conflictRun(ctx, local.RepositoryRoot, "ls-files", "--unmerged", "--stage", "--full-name", "-z")
	if err != nil {
		return snapshot, err
	}
	unmerged, err := parseIndexStagesZ([]byte(unmergedResult.Stdout))
	if err != nil {
		return snapshot, err
	}
	allResult, err := s.conflictRun(ctx, local.RepositoryRoot, "ls-files", "--stage", "--full-name", "-z")
	if err != nil {
		return snapshot, err
	}
	allStages, err := parseIndexStagesZ([]byte(allResult.Stdout))
	if err != nil {
		return snapshot, err
	}
	groups, err := conflictStageGroups(unmerged, allStages, porcelain)
	if err != nil {
		return snapshot, err
	}
	head, err := s.conflictCommit(ctx, local.RepositoryRoot, "HEAD^{commit}")
	if err != nil || head != operation.LocalOID {
		return snapshot, ErrRecoveryRequired
	}
	mergeHead, err := s.conflictCommit(ctx, local.RepositoryRoot, "MERGE_HEAD^{commit}")
	if err != nil {
		return snapshot, err
	}
	managed, err := s.conflictCommit(ctx, local.RepositoryRoot, managedRemoteRef(base.Branch)+"^{commit}")
	if err != nil || mergeHead != operation.RemoteOID || managed != operation.RemoteOID || operation.CandidateOID != operation.RemoteOID {
		return snapshot, ErrRecoveryRequired
	}
	bases, err := s.conflictBases(ctx, local.RepositoryRoot)
	if err != nil {
		return snapshot, err
	}

	conflicts, err := s.buildConflicts(ctx, local.RepositoryRoot, local.GitDir, groups, bases, head, mergeHead)
	if err != nil {
		return snapshot, err
	}
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].Path == conflicts[j].Path {
			return conflicts[i].OriginalPath < conflicts[j].OriginalPath
		}
		return conflicts[i].Path < conflicts[j].Path
	})
	return ConflictSnapshot{
		HeadOID:       head,
		MergeHeadOID:  mergeHead,
		MergeBaseOIDs: append([]string(nil), bases...),
		Conflicts:     conflicts,
		CanComplete:   len(conflicts) == 0,
	}, nil
}

func validConflictRequest(base ConfiguredBase, operation Operation) bool {
	return base.Name != "" && filepath.IsAbs(base.Path) && validLiteralBranch(base.Branch) &&
		(operation.Kind == OperationInitialize || operation.Kind == OperationSync) && validOperationID(operation.ID) && operation.BaseName == base.Name &&
		operation.RepoPath == base.Path && operation.Branch == base.Branch && operation.State == OperationConflict && validObjectID(operation.LocalOID) &&
		validObjectID(operation.CandidateOID) && validObjectID(operation.RemoteOID)
}

func validConflictInspection(base ConfiguredBase, local LocalInspection) error {
	if !local.HasRepository || local.RepositoryRoot != base.Path || !filepath.IsAbs(local.GitDir) || local.DetachedHead ||
		local.CurrentBranch != base.Branch || local.PendingOperation != "merge" {
		return ErrRecoveryRequired
	}
	return nil
}

func conflictMarkersClear(gitDir string) error {
	for _, name := range []string{"index.lock", "rebase-merge", "rebase-apply", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if _, err := os.Lstat(filepath.Join(gitDir, name)); err == nil {
			return ErrRecoveryRequired
		} else if !os.IsNotExist(err) {
			return ErrRecoveryRequired
		}
	}
	return nil
}

func (s *Service) conflictRun(ctx context.Context, dir string, args ...string) (Result, error) {
	result, err := s.runLocal(ctx, dir, true, args...)
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		return Result{}, ErrRecoveryRequired
	}
	return result, nil
}

func (s *Service) conflictCommit(ctx context.Context, dir, revision string) (string, error) {
	result, err := s.conflictRun(ctx, dir, "rev-parse", "--verify", revision)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(result.Stdout, "\n") {
		return "", ErrRecoveryRequired
	}
	oid := strings.TrimSuffix(result.Stdout, "\n")
	if oid == "" || strings.ContainsAny(oid, "\r\n") || !validObjectID(oid) {
		return "", ErrRecoveryRequired
	}
	return oid, nil
}

func (s *Service) conflictBases(ctx context.Context, dir string) ([]string, error) {
	result, err := s.runLocal(ctx, dir, true, "merge-base", "--all", "HEAD", "MERGE_HEAD")
	if result.StdoutTruncated || result.StderrTruncated {
		return nil, ErrRecoveryRequired
	}
	if err != nil {
		if expectedExit(err, 1) && result.Stdout == "" {
			return nil, nil
		}
		return nil, ErrRecoveryRequired
	}
	if result.Stdout == "" {
		return nil, nil
	}
	if !strings.HasSuffix(result.Stdout, "\n") {
		return nil, ErrRecoveryRequired
	}
	values := strings.Split(strings.TrimSuffix(result.Stdout, "\n"), "\n")
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validObjectID(value) {
			return nil, ErrRecoveryRequired
		}
		if _, exists := seen[value]; exists {
			return nil, ErrRecoveryRequired
		}
		seen[value] = struct{}{}
	}
	return values, nil
}

type conflictGroup struct {
	path   string
	stages map[int]indexStage
}

func conflictStageGroups(unmerged, all []indexStage, porcelain []porcelainEntry) ([]conflictGroup, error) {
	groups := make(map[string]map[int]indexStage)
	for _, stage := range unmerged {
		if stage.Stage == 0 || !utf8.ValidString(stage.Path) {
			return nil, ErrConflictUnsupported
		}
		if stage.Mode == "160000" {
			return nil, ErrConflictStateAmbiguous
		}
		if _, ok := groups[stage.Path]; !ok {
			groups[stage.Path] = make(map[int]indexStage)
		}
		groups[stage.Path][stage.Stage] = stage
	}
	matched := make(map[string]map[int]struct{}, len(groups))
	for _, stage := range all {
		if expected, ok := groups[stage.Path]; ok {
			actual, exists := expected[stage.Stage]
			if stage.Stage == 0 || !exists || actual != stage {
				return nil, ErrConflictStateAmbiguous
			}
			if matched[stage.Path] == nil {
				matched[stage.Path] = make(map[int]struct{}, len(expected))
			}
			matched[stage.Path][stage.Stage] = struct{}{}
		}
	}
	for path, expected := range groups {
		if len(matched[path]) != len(expected) {
			return nil, ErrConflictStateAmbiguous
		}
	}
	status := make(map[string]porcelainEntry)
	for _, entry := range porcelain {
		if entry.RecordType == 'u' {
			if !utf8.ValidString(entry.Path) {
				return nil, ErrConflictUnsupported
			}
			if _, exists := status[entry.Path]; exists {
				return nil, ErrConflictStateAmbiguous
			}
			status[entry.Path] = entry
		}
	}
	if len(status) != len(groups) {
		return nil, ErrConflictStateAmbiguous
	}
	result := make([]conflictGroup, 0, len(groups))
	for path, stages := range groups {
		entry, exists := status[path]
		if !exists || conflictXY(stages) != entry.XY {
			return nil, ErrConflictStateAmbiguous
		}
		result = append(result, conflictGroup{path: path, stages: stages})
	}
	return result, nil
}

func conflictXY(stages map[int]indexStage) string {
	_, base := stages[1]
	_, local := stages[2]
	_, remote := stages[3]
	switch {
	case base && local && remote:
		return "UU"
	case !base && local && remote:
		return "AA"
	case base && local && !remote:
		return "UD"
	case base && !local && remote:
		return "DU"
	default:
		return ""
	}
}

func (s *Service) buildConflicts(ctx context.Context, root, gitDir string, groups []conflictGroup, bases []string, head, mergeHead string) ([]Conflict, error) {
	if len(groups) == 0 {
		return []Conflict{}, nil
	}
	needRename := false
	for _, group := range groups {
		if isModifyDelete(group.stages) {
			needRename = true
		}
	}
	var localChanges, remoteChanges []nameStatus
	if needRename {
		if len(bases) != 1 {
			for _, group := range groups {
				if isModifyDelete(group.stages) {
					return nil, ErrConflictStateAmbiguous
				}
			}
		}
		var err error
		localChanges, err = s.conflictNameStatus(ctx, root, bases[0], head)
		if err != nil {
			return nil, err
		}
		remoteChanges, err = s.conflictNameStatus(ctx, root, bases[0], mergeHead)
		if err != nil {
			return nil, err
		}
	}
	paths := make([]string, 0, len(groups))
	for _, group := range groups {
		paths = append(paths, group.path)
	}
	attributes, err := s.conflictAttributes(ctx, root, paths)
	if err != nil {
		return nil, err
	}
	temp, err := os.MkdirTemp("", "igonotes-conflict-")
	if err != nil {
		return nil, ErrRecoveryRequired
	}
	defer os.RemoveAll(temp)
	tempRoot, err := os.OpenRoot(temp)
	if err != nil {
		return nil, ErrRecoveryRequired
	}
	defer tempRoot.Close()

	conflicts := make([]Conflict, 0, len(groups))
	for _, group := range groups {
		kind, original, err := classifyConflict(group, bases, localChanges, remoteChanges)
		if err != nil {
			return nil, err
		}
		stages, contentKind, err := s.materializeConflictStages(ctx, gitDir, temp, tempRoot, group, attributes[group.path])
		if err != nil {
			return nil, err
		}
		conflict := Conflict{Kind: kind, ContentKind: contentKind, Path: group.path, OriginalPath: original}
		conflict.Base = stages[1]
		conflict.Local = stages[2]
		conflict.Remote = stages[3]
		conflict.Actions = conflictActions(kind, contentKind, conflict.Local != nil)
		conflict.ID = conflictIdentity(conflict)
		conflicts = append(conflicts, conflict)
	}
	return conflicts, nil
}

func (s *Service) conflictNameStatus(ctx context.Context, root, base, tip string) ([]nameStatus, error) {
	result, err := s.conflictRun(ctx, root, "diff-tree", "--no-commit-id", "-r", "--name-status", "-z", "--find-renames", "--diff-filter=DMRC", base, tip)
	if err != nil {
		return nil, err
	}
	changes, err := parseNameStatusZ([]byte(result.Stdout))
	if err != nil {
		return nil, err
	}
	return changes, nil
}

func isModifyDelete(stages map[int]indexStage) bool {
	_, base := stages[1]
	_, local := stages[2]
	_, remote := stages[3]
	return base && (local != remote) && (local || remote)
}

func classifyConflict(group conflictGroup, bases []string, local, remote []nameStatus) (ConflictKind, string, error) {
	_, hasBase := group.stages[1]
	_, hasLocal := group.stages[2]
	_, hasRemote := group.stages[3]
	switch {
	case hasBase && hasLocal && hasRemote:
		if len(bases) == 0 {
			return "", "", ErrConflictStateAmbiguous
		}
		return ConflictContent, "", nil
	case !hasBase && hasLocal && hasRemote:
		return ConflictAddAdd, "", nil
	case hasBase && hasLocal != hasRemote:
		if len(bases) != 1 {
			return "", "", ErrConflictStateAmbiguous
		}
		retained, deleted := local, remote
		if !hasLocal {
			retained, deleted = remote, local
		}
		original, renamed, err := renameDeleteEvidence(group.path, retained, deleted)
		if err != nil {
			return "", "", err
		}
		if renamed {
			return ConflictRenameDelete, original, nil
		}
		return ConflictModifyDelete, "", nil
	default:
		return "", "", ErrConflictStateAmbiguous
	}
}

func conflictActions(kind ConflictKind, content ContentKind, localAlive bool) []string {
	switch kind {
	case ConflictContent:
		if content == ContentText {
			return []string{"local", "remote", "manual"}
		}
		return []string{"local", "remote", "keep_both"}
	case ConflictAddAdd:
		if content == ContentText {
			return []string{"local", "remote", "manual", "keep_both"}
		}
		return []string{"local", "remote", "keep_both"}
	case ConflictModifyDelete, ConflictRenameDelete:
		retained := "remote"
		if localAlive {
			retained = "local"
		}
		if content == ContentText {
			return []string{retained, "manual", "delete"}
		}
		return []string{retained, "delete"}
	default:
		return []string{}
	}
}

func renameDeleteEvidence(path string, retained, deleted []nameStatus) (string, bool, error) {
	var old string
	modified := 0
	for _, change := range retained {
		if change.Path != path {
			continue
		}
		switch change.Status {
		case 'R':
			if old != "" || modified != 0 {
				return "", false, ErrConflictStateAmbiguous
			}
			if !utf8.ValidString(change.OldPath) {
				return "", false, ErrConflictUnsupported
			}
			old = change.OldPath
		case 'M':
			if old != "" {
				return "", false, ErrConflictStateAmbiguous
			}
			modified++
		case 'C':
			return "", false, ErrConflictStateAmbiguous
		default:
			return "", false, ErrConflictStateAmbiguous
		}
	}
	deletedPath := 0
	deletedOld := 0
	for _, change := range deleted {
		if change.Status == 'C' && change.Path == path {
			return "", false, ErrConflictStateAmbiguous
		}
		if change.Status != 'D' {
			continue
		}
		if change.Path == path {
			deletedPath++
		}
		if old != "" && change.Path == old {
			deletedOld++
		}
	}
	if old != "" {
		if deletedOld != 1 || deletedPath != 0 {
			return "", false, ErrConflictStateAmbiguous
		}
		return old, true, nil
	}
	if modified != 1 || deletedPath != 1 {
		return "", false, ErrConflictStateAmbiguous
	}
	return "", false, nil
}

func (s *Service) conflictAttributes(ctx context.Context, root string, paths []string) (map[string]string, error) {
	input := make([]byte, 0)
	for _, path := range paths {
		input = append(input, path...)
		input = append(input, 0)
	}
	result, err := s.runLocalInput(ctx, root, true, bytes.NewReader(input), "check-attr", "-z", "--stdin", "diff")
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		return nil, ErrRecoveryRequired
	}
	records, err := parseCheckAttrZ([]byte(result.Stdout))
	if err != nil || len(records) != len(paths) {
		return nil, ErrRecoveryRequired
	}
	attributes := make(map[string]string, len(records))
	for _, record := range records {
		if record.Name != "diff" || !utf8.ValidString(record.Path) {
			return nil, ErrConflictUnsupported
		}
		if _, exists := attributes[record.Path]; exists {
			return nil, ErrRecoveryRequired
		}
		attributes[record.Path] = record.Value
	}
	for _, path := range paths {
		if _, exists := attributes[path]; !exists {
			return nil, ErrRecoveryRequired
		}
	}
	return attributes, nil
}

func (s *Service) materializeConflictStages(ctx context.Context, gitDir, temp string, tempRoot *os.Root, group conflictGroup, diffAttribute string) (map[int]*ConflictStage, ContentKind, error) {
	stages := make(map[int]*ConflictStage, len(group.stages))
	binaryContent := diffAttribute == "unset"
	for number, index := range group.stages {
		name, err := s.checkoutConflictStage(ctx, gitDir, temp, group.path, number)
		if err != nil {
			return nil, "", err
		}
		file, err := tempRoot.Open(name)
		if err != nil {
			return nil, "", ErrRecoveryRequired
		}
		info, statErr := file.Stat()
		preview, truncated, previewBinary, readErr := readConflictPreview(file, info)
		closeErr := file.Close()
		if statErr != nil || readErr != nil || closeErr != nil || !info.Mode().IsRegular() {
			return nil, "", ErrRecoveryRequired
		}
		if previewBinary {
			binaryContent = true
		}
		stage := &ConflictStage{Path: group.path, OID: index.OID, Mode: index.Mode, Size: info.Size()}
		if !binaryContent {
			stage.Content = preview
			stage.PreviewTruncated = truncated
		}
		stages[number] = stage
	}
	if binaryContent {
		for _, stage := range stages {
			stage.Content = nil
			stage.PreviewTruncated = false
		}
		return stages, ContentBinary, nil
	}
	return stages, ContentText, nil
}

func readConflictPreview(file *os.File, info os.FileInfo) (*string, bool, bool, error) {
	if info == nil || !info.Mode().IsRegular() || info.Size() < 0 {
		return nil, false, false, ErrRecoveryRequired
	}
	buffer := make([]byte, conflictPreviewLimit+1)
	n, err := io.ReadFull(file, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, false, false, err
	}
	buffer = buffer[:n]
	truncated := info.Size() > conflictPreviewLimit
	if bytes.IndexByte(buffer, 0) >= 0 || !validPreviewUTF8(buffer, truncated) {
		return nil, false, true, nil
	}
	if truncated {
		return nil, true, false, nil
	}
	value := string(buffer)
	return &value, false, false, nil
}

func validPreviewUTF8(value []byte, truncated bool) bool {
	if utf8.Valid(value) {
		return true
	}
	if !truncated {
		return false
	}
	for len(value) > 0 {
		runeValue, size := utf8.DecodeRune(value)
		if runeValue == utf8.RuneError && size == 1 {
			return !utf8.FullRune(value)
		}
		value = value[size:]
	}
	return true
}

func (s *Service) checkoutConflictStage(ctx context.Context, gitDir, tempRoot, indexPath string, stage int) (string, error) {
	if !filepath.IsAbs(gitDir) || !filepath.IsAbs(tempRoot) || !filepath.IsLocal(indexPath) || indexPath == "." || stage < 1 || stage > 3 {
		return "", ErrRecoveryRequired
	}
	input := bytes.NewReader(append([]byte(indexPath), 0))
	result, err := s.runLocalInput(ctx, tempRoot, true, input,
		"--git-dir="+gitDir, "--work-tree="+tempRoot, "--literal-pathspecs", "checkout-index", "--temp", "--stage="+strconv.Itoa(stage), "-z", "--stdin")
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		return "", ErrRecoveryRequired
	}
	mappings, err := parseCheckoutTempZ([]byte(result.Stdout))
	if err != nil || len(mappings) != 1 || mappings[0].Path != indexPath || !filepath.IsLocal(mappings[0].TempPath) {
		return "", ErrRecoveryRequired
	}
	return mappings[0].TempPath, nil
}

func conflictIdentity(conflict Conflict) string {
	base, local, remote := "", "", ""
	if conflict.Base != nil {
		base = conflict.Base.OID
	}
	if conflict.Local != nil {
		local = conflict.Local.OID
	}
	if conflict.Remote != nil {
		remote = conflict.Remote.OID
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{string(conflict.Kind), conflict.Path, conflict.OriginalPath, base, local, remote}, "\x00")))
	return "sha256:" + fmtHex(sum[:])
}

func fmtHex(value []byte) string {
	const hex = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, byteValue := range value {
		result[index*2] = hex[byteValue>>4]
		result[index*2+1] = hex[byteValue&0x0f]
	}
	return string(result)
}
