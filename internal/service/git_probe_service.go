package service

import (
	"context"
	"errors"
	"sort"
	"strings"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
)

const (
	gitCommitScopeWarning = "Git commits include every non-ignored file in the base directory."
	gitMergeRiskWarning   = "Connecting may merge existing local and remote histories."
)

type GitPorcelain interface {
	Version(context.Context, string) (gitcmd.Version, error)
	ValidateBranch(context.Context, string, string) error
	InspectLocal(context.Context, string) (gitcmd.LocalInspection, error)
	InspectRemote(context.Context, string, string) (gitcmd.RemoteInspection, error)
	HistoryRelation(context.Context, string, string) (string, error)
}

type GitProbeService struct {
	settings  SettingsSnapshot
	porcelain GitPorcelain
}

func NewGitProbeService(settings SettingsSnapshot, porcelain GitPorcelain) *GitProbeService {
	return &GitProbeService{settings: settings, porcelain: porcelain}
}

func (s *GitProbeService) Probe(ctx context.Context, request model.GitProbeRequest) (model.GitProbeResponse, error) {
	request.Base = strings.TrimSpace(request.Base)
	if request.Base == "" {
		return model.GitProbeResponse{}, fieldError(ErrBaseNotFound, "base", "base is required")
	}

	configuredPath, err := configuredBasePath(s.settings.GetConfig(), request.Base)
	if err != nil {
		return model.GitProbeResponse{}, fieldErrorWithCause(ErrBaseNotFound, err, "base", "base is not configured")
	}
	dir, err := canonicalExistingDirectory(configuredPath)
	if err != nil {
		return model.GitProbeResponse{}, fieldErrorWithCause(ErrInvalidPath, err, "base", "resolve base path")
	}

	request.GitURL = strings.TrimSpace(request.GitURL)
	if err := ValidateGitURL(request.GitURL); err != nil {
		return model.GitProbeResponse{}, fieldError(ErrInvalidGitURL, "git_url", "invalid Git URL")
	}
	request.GitBranch = strings.TrimSpace(request.GitBranch)

	response := model.GitProbeResponse{
		Base:            request.Base,
		RemoteBranches:  []string{},
		HistoryRelation: "unknown",
		Warnings:        []string{gitCommitScopeWarning},
	}
	if s.porcelain == nil {
		response.BlockingError = gitProbeAPIError(&gitcmd.SafeError{Code: gitcmd.CodeUnavailable})
		return response, nil
	}

	version, err := s.porcelain.Version(ctx, dir)
	if err != nil {
		response.BlockingError = gitProbeAPIError(err)
		return response, nil
	}
	response.GitVersion = version.Raw
	if !version.Supported() {
		response.BlockingError = &model.APIError{
			Code:    string(gitcmd.CodeUnsupportedVersion),
			Message: "Git 2.28 or newer is required",
		}
		return response, nil
	}

	local, err := s.porcelain.InspectLocal(ctx, dir)
	if err != nil {
		response.BlockingError = gitProbeAPIError(err)
		return response, nil
	}
	response.HasRepository = local.HasRepository
	response.RepositoryRoot = local.RepositoryRoot
	response.RepositoryRootMatches = !local.HasRepository || local.RepositoryRoot == dir
	response.CurrentBranch = local.CurrentBranch
	response.DetachedHead = local.DetachedHead
	response.WorkingTreeClean = local.WorkingTreeClean
	response.PendingOperation = local.PendingOperation
	response.IdentityConfigured = local.IdentityConfigured

	existingOriginUnsafe := false
	if local.ExistingOriginURL == "" {
		response.RequiredMutations.AddOrigin = true
	} else if ValidateGitURL(local.ExistingOriginURL) != nil {
		existingOriginUnsafe = true
		response.RequiredMutations.ReplaceOrigin = true
	} else {
		response.ExistingOriginURL = local.ExistingOriginURL
		response.RequiredMutations.ReplaceOrigin = local.ExistingOriginURL != request.GitURL
	}
	if !local.HasRepository {
		response.RequiredMutations.CreateRepository = true
	}

	if request.GitBranch != "" {
		if err := s.porcelain.ValidateBranch(ctx, dir, request.GitBranch); err != nil {
			response.BlockingError = gitProbeAPIError(err)
			return response, nil
		}
	}

	remote, err := s.porcelain.InspectRemote(ctx, dir, request.GitURL)
	if err != nil {
		response.BlockingError = gitProbeAPIError(err)
		return response, nil
	}
	response.EmptyRemote = remote.Empty
	response.RemoteBranches = make([]string, 0, len(remote.Branches))
	for branch := range remote.Branches {
		response.RemoteBranches = append(response.RemoteBranches, branch)
	}
	sort.Strings(response.RemoteBranches)

	selectedBranchMissing := false
	if request.GitBranch != "" {
		remoteOID, exists := remote.Branches[request.GitBranch]
		switch {
		case remote.Empty:
			response.HistoryRelation = "none"
			response.RequiredMutations.CreateBranch = true
		case !exists:
			selectedBranchMissing = true
		case !local.HasCommits:
			response.HistoryRelation = "none"
		default:
			relation, historyErr := s.porcelain.HistoryRelation(ctx, dir, remoteOID)
			if historyErr != nil {
				response.BlockingError = gitProbeAPIError(historyErr)
				return response, nil
			}
			switch relation {
			case "none", "shared", "unrelated", "unknown":
				response.HistoryRelation = relation
			default:
				response.BlockingError = gitProbeAPIError(nil)
				return response, nil
			}
			if relation == "unrelated" || relation == "unknown" {
				response.RequiredMutations.MergeHistories = true
				response.Warnings = append(response.Warnings, gitMergeRiskWarning)
			}
		}
	}

	response.BlockingError = gitProbeBlocker(local, response.RepositoryRootMatches, existingOriginUnsafe, selectedBranchMissing)
	response.CanConfigure = request.GitBranch != "" && response.BlockingError == nil
	return response, nil
}

func gitProbeBlocker(local gitcmd.LocalInspection, rootMatches, existingOriginUnsafe, selectedBranchMissing bool) *model.APIError {
	switch {
	case existingOriginUnsafe:
		return &model.APIError{Code: "invalid_git_url", Message: "Invalid Git URL", Field: "git_url"}
	case !rootMatches:
		return &model.APIError{Code: string(gitcmd.CodeRepositoryRoot), Message: "Git repository root does not match the base directory"}
	case local.PendingOperation != "":
		return &model.APIError{Code: string(gitcmd.CodeRepositoryLocked), Message: "Git repository has a pending operation"}
	case local.HasRepository && !local.IdentityConfigured:
		return &model.APIError{Code: string(gitcmd.CodeIdentityMissing), Message: "Git identity is not configured"}
	case selectedBranchMissing:
		return &model.APIError{Code: string(gitcmd.CodeInvalidBranch), Message: "Selected Git branch does not exist on the remote", Field: "git_branch"}
	default:
		return nil
	}
}

func gitProbeAPIError(err error) *model.APIError {
	code := gitcmd.CodeCommandFailed
	var safeErr *gitcmd.SafeError
	if errors.As(err, &safeErr) {
		code = safeErr.Code
	}

	switch code {
	case gitcmd.CodeUnavailable:
		return &model.APIError{Code: string(code), Message: "Git executable is unavailable"}
	case gitcmd.CodeUnsupportedVersion:
		return &model.APIError{Code: string(code), Message: "Git 2.28 or newer is required"}
	case gitcmd.CodeAuthentication:
		return &model.APIError{Code: string(code), Message: "Git authentication failed"}
	case gitcmd.CodeRemoteUnreachable:
		return &model.APIError{Code: string(code), Message: "Git remote is unreachable"}
	case gitcmd.CodeIdentityMissing:
		return &model.APIError{Code: string(code), Message: "Git identity is not configured"}
	case gitcmd.CodeInvalidBranch:
		return &model.APIError{Code: string(code), Message: "Invalid Git branch", Field: "git_branch"}
	case gitcmd.CodeRepositoryRoot:
		return &model.APIError{Code: string(code), Message: "Git repository root does not match the base directory"}
	case gitcmd.CodeRepositoryLocked:
		return &model.APIError{Code: string(code), Message: "Git repository is locked"}
	case gitcmd.CodeTimedOut:
		return &model.APIError{Code: string(code), Message: "Git command timed out"}
	case gitcmd.CodeCanceled:
		return &model.APIError{Code: string(code), Message: "Git command was canceled"}
	case gitcmd.CodeNotRepository:
		return &model.APIError{Code: string(code), Message: "Directory is not a Git repository"}
	default:
		return &model.APIError{Code: string(gitcmd.CodeCommandFailed), Message: "Git command failed"}
	}
}
