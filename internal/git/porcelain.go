package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

type Version struct {
	Major int
	Minor int
	Patch int
	Raw   string
}

func (v Version) Supported() bool {
	return v.Major > 2 || v.Major == 2 && v.Minor >= 28
}

type LocalInspection struct {
	HasRepository      bool
	RepositoryRoot     string
	GitDir             string
	CurrentBranch      string
	DetachedHead       bool
	WorkingTreeClean   bool
	ExistingOriginURL  string
	PendingOperation   string
	IdentityConfigured bool
	HasCommits         bool
}

type RemoteInspection struct {
	Branches map[string]string
	Empty    bool
}

type Porcelain interface {
	Version(context.Context, string) (Version, error)
	ValidateBranch(context.Context, string, string) error
	InspectLocal(context.Context, string) (LocalInspection, error)
	InspectRemote(context.Context, string, string) (RemoteInspection, error)
	HistoryRelation(context.Context, string, string) (string, error)
}

type Client struct {
	runner Runner
}

func NewClient(runner Runner) *Client {
	return &Client{runner: runner}
}

func (c *Client) Version(ctx context.Context, dir string) (Version, error) {
	result, err := c.run(ctx, readOnlyCommand(dir, "--version"))
	if err != nil {
		return Version{}, err
	}
	return parseGitVersion(result.Stdout)
}

func parseGitVersion(output string) (Version, error) {
	raw := strings.TrimSpace(output)
	const prefix = "git version "
	if !strings.HasPrefix(raw, prefix) {
		return Version{}, malformedOutputError()
	}
	parts := strings.Split(strings.TrimPrefix(raw, prefix), ".")
	if len(parts) < 3 {
		return Version{}, malformedOutputError()
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	patch, patchErr := strconv.Atoi(parts[2])
	if majorErr != nil || minorErr != nil || patchErr != nil || major < 0 || minor < 0 || patch < 0 {
		return Version{}, malformedOutputError()
	}
	return Version{Major: major, Minor: minor, Patch: patch, Raw: raw}, nil
}

func (c *Client) ValidateBranch(ctx context.Context, dir, branch string) error {
	if !validLiteralBranch(branch) {
		return invalidBranchError()
	}
	_, err := c.run(ctx, readOnlyCommand(dir, "check-ref-format", "refs/heads/"+branch))
	if err != nil {
		var safeErr *SafeError
		if errors.As(err, &safeErr) && safeErr.Message == "Git output exceeded the configured limit" {
			return err
		}
		return invalidBranchError()
	}
	return nil
}

func validLiteralBranch(branch string) bool {
	if branch == "" || strings.TrimSpace(branch) != branch || strings.HasPrefix(branch, "-") ||
		strings.HasPrefix(branch, "refs/") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") ||
		strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") ||
		strings.Contains(branch, "//") || strings.ContainsAny(branch, "~^:?*[\\") {
		return false
	}
	switch branch {
	case "@", "HEAD", "FETCH_HEAD", "ORIG_HEAD", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "AUTO_MERGE", "BISECT_HEAD":
		return false
	}
	for _, character := range branch {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func invalidBranchError() error {
	return &SafeError{Code: CodeInvalidBranch, Message: "Invalid Git branch", Field: "git_branch"}
}

func (c *Client) InspectLocal(ctx context.Context, dir string) (LocalInspection, error) {
	rootResult, err := c.run(ctx, readOnlyCommand(dir, "rev-parse", "--show-toplevel"))
	if err != nil {
		var safeErr *SafeError
		if errors.As(err, &safeErr) && safeErr.Code == CodeNotRepository {
			return LocalInspection{}, nil
		}
		return LocalInspection{}, err
	}
	root, err := canonicalDirectory(singleLine(rootResult.Stdout))
	if err != nil {
		return LocalInspection{}, malformedOutputError()
	}

	gitDirResult, err := c.run(ctx, readOnlyCommand(dir, "rev-parse", "--absolute-git-dir"))
	if err != nil {
		return LocalInspection{}, err
	}
	gitDir := singleLine(gitDirResult.Stdout)
	if gitDir == "" || !filepath.IsAbs(gitDir) {
		return LocalInspection{}, malformedOutputError()
	}
	gitDir = filepath.Clean(gitDir)

	branchResult, branchErr := c.run(ctx, readOnlyCommand(dir, "symbolic-ref", "--quiet", "--short", "HEAD"))
	detached := false
	branch := ""
	if branchErr != nil {
		if !expectedExit(branchErr, 1) {
			return LocalInspection{}, branchErr
		}
		detached = true
	} else {
		branch = singleLine(branchResult.Stdout)
		if branch == "" || !validLiteralBranch(branch) {
			return LocalInspection{}, malformedOutputError()
		}
	}

	statusResult, err := c.run(ctx, readOnlyCommand(dir, "status", "--porcelain=v1", "-z", "--untracked-files=all"))
	if err != nil {
		return LocalInspection{}, err
	}
	if statusResult.Stdout != "" && !strings.HasSuffix(statusResult.Stdout, "\x00") {
		return LocalInspection{}, malformedOutputError()
	}

	originResult, originErr := c.run(ctx, readOnlyCommand(dir, "remote", "get-url", "origin"))
	origin := ""
	if originErr != nil {
		if !expectedExit(originErr, 2) {
			return LocalInspection{}, originErr
		}
	} else {
		origin = singleLine(originResult.Stdout)
		if origin == "" {
			return LocalInspection{}, malformedOutputError()
		}
	}

	name, err := c.optionalConfig(ctx, dir, "user.name")
	if err != nil {
		return LocalInspection{}, err
	}
	email, err := c.optionalConfig(ctx, dir, "user.email")
	if err != nil {
		return LocalInspection{}, err
	}

	_, headErr := c.run(ctx, readOnlyCommand(dir, "rev-parse", "--verify", "HEAD"))
	hasCommits := headErr == nil
	if headErr != nil && !expectedExit(headErr, 128) {
		return LocalInspection{}, headErr
	}

	pending, err := pendingOperation(gitDir)
	if err != nil {
		return LocalInspection{}, err
	}
	return LocalInspection{
		HasRepository:      true,
		RepositoryRoot:     root,
		GitDir:             gitDir,
		CurrentBranch:      branch,
		DetachedHead:       detached,
		WorkingTreeClean:   statusResult.Stdout == "",
		ExistingOriginURL:  origin,
		PendingOperation:   pending,
		IdentityConfigured: strings.TrimSpace(name) != "" && strings.TrimSpace(email) != "",
		HasCommits:         hasCommits,
	}, nil
}

func (c *Client) optionalConfig(ctx context.Context, dir, key string) (string, error) {
	result, err := c.run(ctx, readOnlyCommand(dir, "config", "--get", key))
	if err != nil {
		if expectedExit(err, 1) {
			return "", nil
		}
		return "", err
	}
	return singleLine(result.Stdout), nil
}

func pendingOperation(gitDir string) (string, error) {
	markers := []struct {
		path string
		name string
	}{
		{path: "MERGE_HEAD", name: "merge"},
		{path: "rebase-merge", name: "rebase"},
		{path: "rebase-apply", name: "rebase"},
		{path: "CHERRY_PICK_HEAD", name: "cherry-pick"},
		{path: "REVERT_HEAD", name: "revert"},
	}
	for _, marker := range markers {
		_, err := os.Stat(filepath.Join(gitDir, marker.path))
		switch {
		case err == nil:
			return marker.name, nil
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			return "", &SafeError{Code: CodeCommandFailed, Message: "Git repository inspection failed", cause: err}
		}
	}
	return "", nil
}

func (c *Client) InspectRemote(ctx context.Context, dir, remote string) (RemoteInspection, error) {
	command := readOnlyCommand(dir, "ls-remote", "--symref", remote)
	command.Scope = NetworkOperation
	command.Secrets = []string{remote}
	result, err := c.run(ctx, command)
	if err != nil {
		return RemoteInspection{}, err
	}
	return parseRemoteInspection(result.Stdout)
}

func parseRemoteInspection(output string) (RemoteInspection, error) {
	inspection := RemoteInspection{Branches: make(map[string]string), Empty: output == ""}
	if output == "" {
		return inspection, nil
	}
	if !strings.HasSuffix(output, "\n") {
		return RemoteInspection{}, malformedOutputError()
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	for _, line := range lines {
		value, ref, found := strings.Cut(line, "\t")
		if !found || value == "" || ref == "" || strings.Contains(ref, "\t") {
			return RemoteInspection{}, malformedOutputError()
		}
		if strings.HasPrefix(value, "ref: ") {
			target := strings.TrimPrefix(value, "ref: ")
			if !strings.HasPrefix(target, "refs/") {
				return RemoteInspection{}, malformedOutputError()
			}
			continue
		}
		if !validObjectID(value) || (ref != "HEAD" && !strings.HasPrefix(ref, "refs/")) {
			return RemoteInspection{}, malformedOutputError()
		}
		if branch, found := strings.CutPrefix(ref, "refs/heads/"); found {
			if branch == "" || !validLiteralBranch(branch) {
				return RemoteInspection{}, malformedOutputError()
			}
			if existing, exists := inspection.Branches[branch]; exists && existing != value {
				return RemoteInspection{}, malformedOutputError()
			}
			inspection.Branches[branch] = value
		}
	}
	inspection.Empty = false
	return inspection, nil
}

func (c *Client) HistoryRelation(ctx context.Context, dir, remoteOID string) (string, error) {
	if remoteOID == "" {
		return "none", nil
	}
	if !validObjectID(remoteOID) {
		return "", malformedOutputError()
	}
	_, err := c.run(ctx, readOnlyCommand(dir, "cat-file", "-e", remoteOID+"^{commit}"))
	if err != nil {
		if expectedExit(err, 1) {
			return "unknown", nil
		}
		return "", err
	}

	_, err = c.run(ctx, readOnlyCommand(dir, "merge-base", "--is-ancestor", remoteOID, "HEAD"))
	if err == nil {
		return "shared", nil
	}
	if !expectedExit(err, 1) {
		return "", err
	}

	result, err := c.run(ctx, readOnlyCommand(dir, "merge-base", "HEAD", remoteOID))
	if err != nil {
		if expectedExit(err, 1) {
			return "unrelated", nil
		}
		return "", err
	}
	if !validObjectID(singleLine(result.Stdout)) {
		return "", malformedOutputError()
	}
	return "shared", nil
}

func (c *Client) run(ctx context.Context, command Command) (Result, error) {
	if c == nil || c.runner == nil {
		return Result{}, &SafeError{Code: CodeUnavailable, Message: "Git executable is unavailable"}
	}
	result, err := c.runner.Run(ctx, command)
	if result.StdoutTruncated || result.StderrTruncated {
		return Result{}, &SafeError{Code: CodeCommandFailed, Message: "Git output exceeded the configured limit"}
	}
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func readOnlyCommand(dir string, args ...string) Command {
	return Command{Dir: dir, Args: args, Scope: LocalOperation, ReadOnly: true}
}

func singleLine(output string) string {
	return strings.TrimSuffix(strings.TrimSuffix(output, "\n"), "\r")
}

func expectedExit(err error, code int) bool {
	var safeErr *SafeError
	return errors.As(err, &safeErr) && safeErr.ExitCode == code
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return false
		}
	}
	return true
}

func malformedOutputError() error {
	return &SafeError{Code: CodeCommandFailed, Message: "Git command returned malformed output"}
}
