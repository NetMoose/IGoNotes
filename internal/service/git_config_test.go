package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
)

type branchValidatorFake struct {
	dir    string
	branch string
	err    error
	calls  int
}

func (f *branchValidatorFake) ValidateBranch(_ context.Context, dir, branch string) error {
	f.calls++
	f.dir = dir
	f.branch = branch
	return f.err
}

func TestValidateGitURLPolicy(t *testing.T) {
	accepted := []string{
		"https://example.com/notes.git",
		"http://example.com/notes.git",
		"https://example.com",
		"https://[2001:db8::1]/notes.git",
		"ssh://example.com/notes.git",
		"ssh://git@example.com/notes.git",
		"ssh://git@[2001:db8::1]/notes.git",
		"git://example.com/notes.git",
		"git://[2001:db8::1]/notes.git",
		"file:///srv/git/notes.git",
		"file://server/srv/git/notes.git",
		"git@example.com:notes.git",
		"example.com:notes.git",
		"git@[2001:db8::1]:notes.git",
		"[2001:db8::1]:notes.git",
		"/srv/git/notes.git",
		"../notes.git",
		"notes.git",
		"path with spaces/notes.git",
		"./repos/team:notes.git",
		"../repos/team:notes.git",
		"/srv/repos/team:notes.git",
	}
	for _, value := range accepted {
		t.Run("accept_"+value, func(t *testing.T) {
			if err := ValidateGitURL(value); err != nil {
				t.Fatalf("ValidateGitURL(%q) error = %v", value, err)
			}
		})
	}

	rejected := []string{
		"",
		" \t ",
		"-upload-pack=evil",
		"notes\x00.git",
		"notes\r.git",
		"notes\n.git",
		"notes\t.git",
		"ext::sh -c touch /tmp/pwned",
		"hg::https://example.com/notes",
		"helper::address",
		"ftp://example.com/notes.git",
		"unknown://example.com/notes.git",
		"https://",
		"https:///notes.git",
		"https://token@example.com/notes.git",
		"https://user:password@example.com/notes.git",
		"https://example.com/notes.git?token=secret",
		"https://example.com/notes.git?",
		"https://example.com/notes.git#private",
		"https://example.com/notes.git#",
		"ssh://",
		"ssh:///notes.git",
		"ssh://user:password@example.com/notes.git",
		"ssh://example.com/notes.git?token=secret",
		"ssh://example.com/notes.git#private",
		"git://",
		"git:///notes.git",
		"git://user@example.com/notes.git",
		"git://example.com/notes.git?token=secret",
		"git://example.com/notes.git#private",
		"file://",
		"file://user@server/notes.git",
		"file:///notes.git?token=secret",
		"file:///notes.git#private",
		"git@:notes.git",
		"@example.com:notes.git",
		"example.com:",
		"[2001:db8::1]notes.git",
		"[2001:db8::1]:",
		"git@[2001:db8::1]notes.git",
		"host::notes.git",
		"./repos/team::notes.git",
		"./repos/team://notes.git",
		"not a scheme://example.com/notes.git",
	}
	for _, value := range rejected {
		t.Run("reject_"+value, func(t *testing.T) {
			err := ValidateGitURL(value)
			if !errors.Is(err, ErrInvalidGitURL) {
				t.Fatalf("ValidateGitURL(%q) error = %v, want ErrInvalidGitURL", value, err)
			}
		})
	}
}

func TestValidateGitIntervalPolicy(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		interval int
		wantErr  bool
	}{
		{name: "disabled zero", interval: 0},
		{name: "disabled five", interval: 5},
		{name: "disabled fifteen", interval: 15},
		{name: "disabled thirty", interval: 30},
		{name: "disabled sixty", interval: 60},
		{name: "enabled five", enabled: true, interval: 5},
		{name: "enabled fifteen", enabled: true, interval: 15},
		{name: "enabled thirty", enabled: true, interval: 30},
		{name: "enabled sixty", enabled: true, interval: 60},
		{name: "enabled zero", enabled: true, interval: 0, wantErr: true},
		{name: "disabled unsupported", interval: 10, wantErr: true},
		{name: "enabled unsupported", enabled: true, interval: 10, wantErr: true},
		{name: "negative", interval: -5, wantErr: true},
		{name: "too large", interval: 120, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateGitInterval(test.enabled, test.interval)
			if test.wantErr && !errors.Is(err, ErrInvalidGitInterval) {
				t.Fatalf("ValidateGitInterval() error = %v, want ErrInvalidGitInterval", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("ValidateGitInterval() error = %v", err)
			}
		})
	}
}

func TestNormalizeGitCommitTemplate(t *testing.T) {
	valid := []struct {
		input string
		want  string
	}{
		{input: "", want: DefaultGitCommitMessageTemplate},
		{input: "sync {{base}}/{{branch}} {{date}} {{datetime}} {{count}} {{base}}", want: "sync {{base}}/{{branch}} {{date}} {{datetime}} {{count}} {{base}}"},
		{input: strings.Repeat("я", 200), want: strings.Repeat("я", 200)},
	}
	for _, test := range valid {
		got, err := NormalizeGitCommitTemplate(test.input)
		if err != nil {
			t.Fatalf("NormalizeGitCommitTemplate(%q) error = %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("NormalizeGitCommitTemplate(%q) = %q, want %q", test.input, got, test.want)
		}
	}

	invalid := []string{
		"   \t",
		strings.Repeat("я", 201),
		"message\x00",
		"message\r",
		"message\n",
		"message\t",
		"message {{",
		"message {{base",
		"message {{base}",
		"message }}",
		"message {{unknown}}",
		"message {{ base }}",
		"message {{{base}}}",
	}
	for _, value := range invalid {
		if _, err := NormalizeGitCommitTemplate(value); !errors.Is(err, ErrInvalidGitTemplate) {
			t.Errorf("NormalizeGitCommitTemplate(%q) error = %v, want ErrInvalidGitTemplate", value, err)
		}
	}
}

func TestRenderGitCommitMessage(t *testing.T) {
	at := time.Date(2026, 9, 1, 14, 30, 0, 0, time.FixedZone("UTC+3", 3*60*60))
	got, err := RenderGitCommitMessage(
		"IGoNotes: {{base}} {{branch}} {{date}} {{datetime}} {{count}} {{base}}",
		GitCommitData{Base: "work", Branch: "main", Count: 3, Time: at},
	)
	if err != nil {
		t.Fatalf("RenderGitCommitMessage() error = %v", err)
	}
	want := "IGoNotes: work main 2026-09-01 2026-09-01T14:30:00+03:00 3 work"
	if got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}

	if _, err := RenderGitCommitMessage("{{unsupported}}", GitCommitData{}); !errors.Is(err, ErrInvalidGitTemplate) {
		t.Fatalf("invalid template error = %v, want ErrInvalidGitTemplate", err)
	}
}

func TestValidateGitConfigNormalizesAndDelegatesBranch(t *testing.T) {
	branchValidator := &branchValidatorFake{}
	validator := NewGitConfigValidator(branchValidator)
	request := model.GitConfigRequest{
		GitURL:                   "  https://example.com/notes.git  ",
		GitBranch:                "  feature/editor  ",
		AutoSync:                 true,
		AutoSyncIntervalMinutes:  15,
		GitCommitMessageTemplate: "",
	}

	got, err := validator.Validate(context.Background(), "/notes", request)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got.GitURL != "https://example.com/notes.git" || got.GitBranch != "feature/editor" {
		t.Fatalf("Validate() URL/branch = %q/%q, want trimmed values", got.GitURL, got.GitBranch)
	}
	if got.GitCommitMessageTemplate != DefaultGitCommitMessageTemplate {
		t.Fatalf("template = %q, want default", got.GitCommitMessageTemplate)
	}
	if branchValidator.calls != 1 || branchValidator.dir != "/notes" || branchValidator.branch != "feature/editor" {
		t.Fatalf("branch validator = calls %d, dir %q, branch %q", branchValidator.calls, branchValidator.dir, branchValidator.branch)
	}
}

func TestValidateGitConfigRejectsMissingOrInvalidBranch(t *testing.T) {
	branchValidator := &branchValidatorFake{}
	validator := NewGitConfigValidator(branchValidator)
	request := model.GitConfigRequest{GitURL: "https://example.com/notes.git", GitBranch: " \t", AutoSyncIntervalMinutes: 5}
	_, err := validator.Validate(context.Background(), "/notes", request)
	if !errors.Is(err, ErrInvalidGitBranch) {
		t.Fatalf("missing branch error = %v, want ErrInvalidGitBranch", err)
	}
	if branchValidator.calls != 0 {
		t.Fatalf("branch validator calls = %d, want 0", branchValidator.calls)
	}

	invalidErrors := []error{
		&gitcmd.SafeError{Code: gitcmd.CodeInvalidBranch, Message: "Invalid Git branch", Field: "git_branch"},
		ErrInvalidGitBranch,
	}
	for _, invalidErr := range invalidErrors {
		branchValidator.err = invalidErr
		request.GitBranch = "main"
		_, err = validator.Validate(context.Background(), "/notes", request)
		if !errors.Is(err, ErrInvalidGitBranch) {
			t.Fatalf("delegated branch error = %v, want ErrInvalidGitBranch", err)
		}
		var fieldErr *FieldError
		if !errors.As(err, &fieldErr) || fieldErr.Field != "git_branch" {
			t.Fatalf("delegated branch error = %#v, want git_branch FieldError", err)
		}
	}
}

func TestValidateGitConfigPreservesBranchValidatorOperationalErrors(t *testing.T) {
	request := model.GitConfigRequest{
		GitURL:                  "https://example.com/notes.git",
		GitBranch:               "main",
		AutoSyncIntervalMinutes: 5,
	}
	tests := []struct {
		name string
		err  error
	}{
		{name: "unavailable", err: &gitcmd.SafeError{Code: gitcmd.CodeUnavailable, Message: "Git executable is unavailable"}},
		{name: "timeout", err: &gitcmd.SafeError{Code: gitcmd.CodeTimedOut, Message: "Git command timed out"}},
		{name: "canceled", err: &gitcmd.SafeError{Code: gitcmd.CodeCanceled, Message: "Git command was canceled"}},
		{name: "generic operational error", err: errors.New("branch validation operation failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			branchValidator := &branchValidatorFake{err: test.err}
			_, err := NewGitConfigValidator(branchValidator).Validate(context.Background(), "/notes", request)
			if err != test.err {
				t.Fatalf("Validate() error = %#v, want exact operational error %#v", err, test.err)
			}
			if errors.Is(err, ErrInvalidGitBranch) {
				t.Fatalf("Validate() error = %#v, must not wrap ErrInvalidGitBranch", err)
			}
			if branchValidator.calls != 1 {
				t.Fatalf("branch validator calls = %d, want 1", branchValidator.calls)
			}
		})
	}
}

func TestGitConfigContracts(t *testing.T) {
	if DefaultGitCommitMessageTemplate != "IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)" {
		t.Fatalf("default template = %q", DefaultGitCommitMessageTemplate)
	}
	for name, err := range map[string]error{
		"URL": ErrInvalidGitURL, "branch": ErrInvalidGitBranch, "interval": ErrInvalidGitInterval,
		"template": ErrInvalidGitTemplate, "repository in use": ErrGitRepositoryInUse,
	} {
		if err == nil {
			t.Errorf("%s sentinel is nil", name)
		}
	}

	var _ SettingsSnapshot = (*SettingsService)(nil)
	var _ GitBranchValidator = (*branchValidatorFake)(nil)
}
