package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
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
	CodeNotPaused              ErrorCode = "git_not_paused"
)

var (
	ErrConflictNotFound       = &SafeError{Code: CodeConflictNotFound, Message: "Git conflict was not found"}
	ErrConflictStale          = &SafeError{Code: CodeConflictStale, Message: "Git conflict changed; refresh and try again"}
	ErrConflictUnresolved     = &SafeError{Code: CodeConflictUnresolved, Message: "Git conflict still has unresolved paths"}
	ErrConflictUnsupported    = &SafeError{Code: CodeConflictUnsupported, Message: "Git conflict type is unsupported"}
	ErrMergeNotInProgress     = &SafeError{Code: CodeMergeNotInProgress, Message: "Git merge is not in progress"}
	ErrRecoveryRequired       = &SafeError{Code: CodeRecoveryRequired, Message: "Git repository requires recovery"}
	ErrGitPaused              = &SafeError{Code: CodePaused, Message: "Git synchronization is paused"}
	ErrGitNotPaused           = &SafeError{Code: CodeNotPaused, Message: "Git synchronization is not paused"}
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

// Format keeps even Go-syntax formatting from inspecting private diagnostics
// or invoking a private cause's Error or Format methods.
func (e *SafeError) Format(state fmt.State, verb rune) {
	message := e.Message
	if verb == 'q' {
		message = strconv.Quote(message)
	}
	_, _ = io.WriteString(state, message)
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

// secretVariants is local to each redaction: credentials are never retained on
// a runner or error. Only explicit secrets and their HTTP userinfo are used.
func secretVariants(secrets []string) []string {
	unique := make(map[string]struct{})
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		unique[secret] = struct{}{}
		parsed, err := url.Parse(secret)
		if err != nil || parsed.User == nil || !(strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) {
			continue
		}
		password, _ := parsed.User.Password()
		_, authority, _ := strings.Cut(secret, "://")
		if end := strings.IndexAny(authority, "/?#"); end >= 0 {
			authority = authority[:end]
		}
		rawUserinfo := ""
		if at := strings.LastIndexByte(authority, '@'); at >= 0 {
			rawUserinfo = authority[:at]
		}
		for _, variant := range []string{rawUserinfo, parsed.User.String(), parsed.User.Username(), password} {
			if len(variant) >= 4 {
				unique[variant] = struct{}{}
			}
		}
	}
	ordered := make([]string, 0, len(unique))
	for variant := range unique {
		ordered = append(ordered, variant)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) != len(ordered[j]) {
			return len(ordered[i]) > len(ordered[j])
		}
		return ordered[i] < ordered[j]
	})
	return ordered
}

func redact(text string, secrets []string) string {
	ordered := secretVariants(secrets)
	// A single pass avoids matching another secret inside a replacement marker.
	redacted := text
	if len(ordered) == 1 {
		// Single-pattern Replacer preprocessing can itself be quadratic for
		// repetitive secrets; ReplaceAll has the same single-pass semantics.
		redacted = strings.ReplaceAll(text, ordered[0], "[REDACTED_REMOTE]")
	} else if len(ordered) > 1 {
		redacted = replaceSecretVariants(text, ordered)
	}
	// Capture lookahead can end inside a later credential after earlier full
	// replacements shrink the output. Protect that tail after full replacements;
	// never derive arbitrary words or redact prefixes shorter than four bytes.
	tail := 0
	if !strings.HasSuffix(redacted, "[REDACTED_REMOTE]") {
		for _, secret := range ordered {
			if len(secret)-1 > tail {
				tail = max(tail, secretPrefixSuffixLength(redacted, secret))
			}
		}
	}
	if tail > 0 {
		redacted = redacted[:len(redacted)-tail] + "[REDACTED_REMOTE]"
	}
	return httpUserinfoPattern.ReplaceAllString(redacted, `${1}[REDACTED]@`)
}

// replaceSecretVariants scans the original text with KMP for each longest-first
// pattern: O(len(text) + len(pattern)) per pattern, with O(len(text) + longest
// pattern) workspace. It records only the longest match at each start, then
// emits nonoverlapping replacements left to right. Markers are never rescanned.
func replaceSecretVariants(text string, ordered []string) string {
	var prefix, longest []int
	for _, secret := range ordered {
		if len(secret) > len(text) {
			continue
		}
		if prefix == nil {
			// Patterns are longest first, so this scratch table can be reused.
			prefix = make([]int, len(secret))
		}
		table := prefix[:len(secret)]
		fillSecretPrefixTable(secret, table)
		for i, matched := 0, 0; i < len(text); i++ {
			for matched > 0 && text[i] != secret[matched] {
				matched = table[matched-1]
			}
			if text[i] == secret[matched] {
				matched++
			}
			if matched == len(secret) {
				if longest == nil {
					longest = make([]int, len(text))
				}
				start := i + 1 - matched
				longest[start] = max(longest[start], matched)
				matched = table[matched-1]
			}
		}
	}
	if longest == nil {
		return text
	}
	var output strings.Builder
	output.Grow(len(text))
	from := 0
	for i := 0; i < len(text); {
		if longest[i] == 0 {
			i++
			continue
		}
		output.WriteString(text[from:i])
		output.WriteString("[REDACTED_REMOTE]")
		i += longest[i]
		from = i
	}
	output.WriteString(text[from:])
	return output.String()
}

func fillSecretPrefixTable(pattern string, prefix []int) {
	prefix[0] = 0
	for i, matched := 1, 0; i < len(pattern); i++ {
		for matched > 0 && pattern[i] != pattern[matched] {
			matched = prefix[matched-1]
		}
		if pattern[i] == pattern[matched] {
			matched++
		}
		prefix[i] = matched
	}
}

// secretPrefixSuffixLength finds the longest proper secret prefix of at least
// four bytes at the text's tail. KMP avoids repeatedly comparing overlapping
// suffixes of repetitive input: time and space are linear in the candidate
// length, bounded by both the secret and the diagnostic length.
func secretPrefixSuffixLength(text, secret string) int {
	length := min(len(secret)-1, len(text))
	if length < 4 {
		return 0
	}
	pattern := secret[:length]
	prefix := make([]int, length)
	fillSecretPrefixTable(pattern, prefix)
	matched := 0
	for i := len(text) - length; i < len(text); i++ {
		for matched > 0 && (matched == length || text[i] != pattern[matched]) {
			matched = prefix[matched-1]
		}
		if text[i] == pattern[matched] {
			matched++
		}
	}
	if matched < 4 {
		return 0
	}
	return matched
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
