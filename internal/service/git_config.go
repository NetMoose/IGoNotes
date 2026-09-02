package service

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"IGoNotes/internal/model"
)

const DefaultGitCommitMessageTemplate = "IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)"

var (
	ErrInvalidGitURL      = errors.New("invalid git URL")
	ErrInvalidGitBranch   = errors.New("invalid git branch")
	ErrInvalidGitInterval = errors.New("invalid auto sync interval")
	ErrInvalidGitTemplate = errors.New("invalid git commit message template")
	ErrGitRepositoryInUse = errors.New("git repository in use")
)

type GitBranchValidator interface {
	ValidateBranch(context.Context, string, string) error
}

type GitConfigValidator interface {
	Validate(context.Context, string, model.GitConfigRequest) (model.GitConfigRequest, error)
}

type SettingsSnapshot interface {
	GetConfig() model.Config
}

type GitCommitData struct {
	Base   string
	Branch string
	Count  int
	Time   time.Time
}

type gitConfigValidator struct {
	branches GitBranchValidator
}

func NewGitConfigValidator(branches GitBranchValidator) GitConfigValidator {
	return &gitConfigValidator{branches: branches}
}

func (v *gitConfigValidator) Validate(ctx context.Context, dir string, request model.GitConfigRequest) (model.GitConfigRequest, error) {
	request.GitURL = strings.TrimSpace(request.GitURL)
	request.GitBranch = strings.TrimSpace(request.GitBranch)
	if err := ValidateGitURL(request.GitURL); err != nil {
		return model.GitConfigRequest{}, fieldError(ErrInvalidGitURL, "git_url", "invalid Git URL")
	}
	if request.GitBranch == "" {
		return model.GitConfigRequest{}, fieldError(ErrInvalidGitBranch, "git_branch", "Git branch is required")
	}
	if v.branches == nil {
		return model.GitConfigRequest{}, fieldError(ErrInvalidGitBranch, "git_branch", "invalid Git branch")
	}
	if err := v.branches.ValidateBranch(ctx, dir, request.GitBranch); err != nil {
		return model.GitConfigRequest{}, fieldErrorWithCause(ErrInvalidGitBranch, err, "git_branch", "invalid Git branch")
	}
	if err := ValidateGitInterval(request.AutoSync, request.AutoSyncIntervalMinutes); err != nil {
		return model.GitConfigRequest{}, fieldError(ErrInvalidGitInterval, "auto_sync_interval_minutes", "invalid auto sync interval")
	}
	template, err := NormalizeGitCommitTemplate(request.GitCommitMessageTemplate)
	if err != nil {
		return model.GitConfigRequest{}, fieldError(ErrInvalidGitTemplate, "git_commit_message_template", "invalid Git commit message template")
	}
	request.GitCommitMessageTemplate = template
	return request, nil
}

func ValidateGitURL(value string) error {
	if value == "" || containsControl(value) {
		return ErrInvalidGitURL
	}
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") {
		return ErrInvalidGitURL
	}

	if strings.Contains(value, "://") {
		return validateGitURLForm(value)
	}
	for _, scheme := range []string{"http:", "https:", "ssh:", "git:", "file:"} {
		if strings.HasPrefix(strings.ToLower(value), scheme) {
			return ErrInvalidGitURL
		}
	}
	if strings.Contains(value, "::") {
		if validateSCPLike(value) {
			return nil
		}
		return ErrInvalidGitURL
	}
	if strings.Contains(value, ":") {
		colon := strings.IndexByte(value, ':')
		if separator := strings.IndexAny(value, `/\`); separator >= 0 && separator < colon {
			return nil
		}
		if validateSCPLike(value) {
			return nil
		}
		return ErrInvalidGitURL
	}
	return nil
}

func validateGitURLForm(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || strings.ContainsAny(value, "?#") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ErrInvalidGitURL
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		if parsed.User != nil || parsed.Host == "" || parsed.Hostname() == "" {
			return ErrInvalidGitURL
		}
	case "ssh":
		if parsed.Host == "" || parsed.Hostname() == "" {
			return ErrInvalidGitURL
		}
		if parsed.User != nil {
			if parsed.User.Username() == "" {
				return ErrInvalidGitURL
			}
			if _, hasPassword := parsed.User.Password(); hasPassword {
				return ErrInvalidGitURL
			}
		}
	case "git":
		if parsed.User != nil || parsed.Host == "" || parsed.Hostname() == "" {
			return ErrInvalidGitURL
		}
	case "file":
		if parsed.User != nil || parsed.Path == "" {
			return ErrInvalidGitURL
		}
	default:
		return ErrInvalidGitURL
	}
	return nil
}

func validateSCPLike(value string) bool {
	hostStart := 0
	if at := strings.IndexByte(value, '@'); at >= 0 {
		if at == 0 || strings.Contains(value[at+1:], "@") {
			return false
		}
		user := value[:at]
		if strings.ContainsAny(user, ":/[] ") {
			return false
		}
		hostStart = at + 1
	}
	remainder := value[hostStart:]
	if strings.HasPrefix(remainder, "[") {
		closeBracket := strings.IndexByte(remainder, ']')
		if closeBracket < 0 || closeBracket+1 >= len(remainder) || remainder[closeBracket+1] != ':' {
			return false
		}
		host := remainder[1:closeBracket]
		path := remainder[closeBracket+2:]
		return strings.Contains(host, ":") && net.ParseIP(host) != nil && path != ""
	}
	if strings.Count(remainder, ":") != 1 {
		return false
	}
	host, path, _ := strings.Cut(remainder, ":")
	return host != "" && path != "" && !strings.ContainsAny(host, "/\\[]@ ")
}

func ValidateGitInterval(enabled bool, interval int) error {
	if !enabled && interval == 0 {
		return nil
	}
	switch interval {
	case 5, 15, 30, 60:
		return nil
	default:
		return ErrInvalidGitInterval
	}
}

func NormalizeGitCommitTemplate(template string) (string, error) {
	if template == "" {
		return DefaultGitCommitMessageTemplate, nil
	}
	if strings.TrimSpace(template) == "" || utf8.RuneCountInString(template) > 200 || containsControl(template) {
		return "", ErrInvalidGitTemplate
	}

	remainder := template
	for {
		start := strings.Index(remainder, "{{")
		if start < 0 {
			if strings.Contains(remainder, "}}") {
				return "", ErrInvalidGitTemplate
			}
			break
		}
		if strings.Contains(remainder[:start], "}}") {
			return "", ErrInvalidGitTemplate
		}
		end := strings.Index(remainder[start+2:], "}}")
		if end < 0 {
			return "", ErrInvalidGitTemplate
		}
		token := remainder[start+2 : start+2+end]
		switch token {
		case "base", "branch", "date", "datetime", "count":
		default:
			return "", ErrInvalidGitTemplate
		}
		remainder = remainder[start+2+end+2:]
	}
	return template, nil
}

func RenderGitCommitMessage(template string, data GitCommitData) (string, error) {
	normalized, err := NormalizeGitCommitTemplate(template)
	if err != nil {
		return "", err
	}
	return strings.NewReplacer(
		"{{base}}", data.Base,
		"{{branch}}", data.Branch,
		"{{date}}", data.Time.Format("2006-01-02"),
		"{{datetime}}", data.Time.Format(time.RFC3339),
		"{{count}}", strconv.Itoa(data.Count),
	).Replace(normalized), nil
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
