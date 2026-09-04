package git

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
)

type ErrorCode string

const (
	CodeUnavailable            ErrorCode = "git_unavailable"
	CodeUnsupportedVersion     ErrorCode = "git_version_unsupported"
	CodeAuthentication         ErrorCode = "auth_failed"
	CodeRemoteUnreachable      ErrorCode = "remote_unreachable"
	CodeIdentityMissing        ErrorCode = "identity_missing"
	CodeInvalidBranch          ErrorCode = "invalid_branch"
	CodeRepositoryRoot         ErrorCode = "repository_root_mismatch"
	CodeRepositoryLocked       ErrorCode = "repository_locked"
	CodeCommandFailed          ErrorCode = "git_command_failed"
	CodeTimedOut               ErrorCode = "git_timeout"
	CodeCanceled               ErrorCode = "git_canceled"
	CodeNotRepository          ErrorCode = "not_a_git_repository"
	CodeOriginMismatch         ErrorCode = "origin_mismatch"
	CodeBranchDeleted          ErrorCode = "branch_deleted"
	CodeRemoteHistoryRewritten ErrorCode = "remote_history_rewritten"
	CodePushRejected           ErrorCode = "push_rejected"
	CodeGitConflict            ErrorCode = "git_conflict"
	CodeNeedsReconnect         ErrorCode = "needs_reconnect"
	CodeConfirmationRequired   ErrorCode = "git_confirmation_required"
	CodeOperationInterrupted   ErrorCode = "operation_interrupted"
	CodeBackupMismatch         ErrorCode = "backup_mismatch"
	CodeConflictNotFound       ErrorCode = "git_conflict_not_found"
	CodeConflictStale          ErrorCode = "git_conflict_stale"
	CodeConflictUnresolved     ErrorCode = "git_conflict_unresolved"
	CodeConflictUnsupported    ErrorCode = "git_conflict_unsupported"
	CodeMergeNotInProgress     ErrorCode = "git_merge_not_in_progress"
	CodeRecoveryRequired       ErrorCode = "git_recovery_required"
	CodePaused                 ErrorCode = "git_paused"
)

var (
	ErrConflictNotFound       = &SafeError{Code: CodeConflictNotFound, Message: "Git conflict was not found"}
	ErrConflictStale          = &SafeError{Code: CodeConflictStale, Message: "Git conflict changed; refresh and try again"}
	ErrConflictUnresolved     = &SafeError{Code: CodeConflictUnresolved, Message: "Git conflict still has unresolved paths"}
	ErrConflictUnsupported    = &SafeError{Code: CodeConflictUnsupported, Message: "Git conflict type is unsupported"}
	ErrMergeNotInProgress     = &SafeError{Code: CodeMergeNotInProgress, Message: "Git merge is not in progress"}
	ErrRecoveryRequired       = &SafeError{Code: CodeRecoveryRequired, Message: "Git repository requires recovery"}
	ErrGitPaused              = &SafeError{Code: CodePaused, Message: "Git synchronization is paused"}
	ErrConflictStateAmbiguous = ErrRecoveryRequired
)

func newConflictFieldError(message, field string) *SafeError {
	return &SafeError{Code: CodeConflictUnsupported, Message: message, Field: field}
}

type SafeError struct {
	Code     ErrorCode
	Message  string
	Field    string
	ExitCode int

	diagnostic string
	cause      error
}

func (e *SafeError) Error() string {
	return e.Message
}

func (e *SafeError) Unwrap() error {
	return e.cause
}

func (e *SafeError) Diagnostic() string {
	return e.diagnostic
}

var (
	httpUserinfoPattern           = regexp.MustCompile(`(?i)(https?://)[^/\s@]+@`)
	truncatedHTTPAuthorityPattern = regexp.MustCompile(`(?i)(https?://)[^/\s@]*$`)
)

func redact(text string, secrets []string) string {
	orderedSecrets := append([]string(nil), secrets...)
	sort.SliceStable(orderedSecrets, func(i, j int) bool {
		return len(orderedSecrets[i]) > len(orderedSecrets[j])
	})

	redacted := text
	for _, secret := range orderedSecrets {
		if secret != "" {
			redacted = strings.ReplaceAll(redacted, secret, "[REDACTED_REMOTE]")
		}
	}
	return httpUserinfoPattern.ReplaceAllString(redacted, `${1}[REDACTED]@`)
}

func classifyFailure(err error, diagnostic string) *SafeError {
	lower := strings.ToLower(diagnostic)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &SafeError{Code: CodeTimedOut, Message: "Git command timed out", cause: context.DeadlineExceeded}
	case errors.Is(err, context.Canceled):
		return &SafeError{Code: CodeCanceled, Message: "Git command was canceled", cause: context.Canceled}
	case strings.Contains(lower, "authentication failed"),
		strings.Contains(lower, "permission denied (publickey)"),
		strings.Contains(lower, "could not read username"),
		strings.Contains(lower, "terminal prompts disabled"):
		return &SafeError{Code: CodeAuthentication, Message: "Git authentication failed"}
	case strings.Contains(lower, "could not resolve host"),
		strings.Contains(lower, "unable to access"),
		strings.Contains(lower, "repository not found"),
		strings.Contains(lower, "does not appear to be a git repository"):
		return &SafeError{Code: CodeRemoteUnreachable, Message: "Git remote is unreachable"}
	case strings.Contains(lower, "index.lock"):
		return &SafeError{Code: CodeRepositoryLocked, Message: "Git repository is locked"}
	case strings.Contains(lower, "not a git repository"):
		return &SafeError{Code: CodeNotRepository, Message: "Directory is not a Git repository"}
	case strings.Contains(lower, "non-fast-forward"),
		strings.Contains(lower, "fetch first"),
		strings.Contains(lower, "remote contains work"):
		return &SafeError{Code: CodePushRejected, Message: "Git push was rejected"}
	default:
		return &SafeError{Code: CodeCommandFailed, Message: "Git command failed"}
	}
}
