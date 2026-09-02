package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	gitcmd "IGoNotes/internal/git"
	"IGoNotes/internal/model"
)

const (
	probeRemote = "https://example.com/notes.git"
	mainOID     = "1111111111111111111111111111111111111111"
	devOID      = "2222222222222222222222222222222222222222"
	commitScope = "Git commits include every non-ignored file in the base directory."
	mergeRisk   = "Connecting may merge existing local and remote histories."
)

type probeSettingsStub struct {
	config model.Config
}

func (s probeSettingsStub) GetConfig() model.Config { return s.config }

type probePorcelainStub struct {
	version      gitcmd.Version
	versionErr   error
	local        gitcmd.LocalInspection
	localErr     error
	remote       gitcmd.RemoteInspection
	remoteErr    error
	validateErr  error
	history      string
	historyErr   error
	calls        []string
	remoteValue  string
	branchValue  string
	historyValue string
}

func (p *probePorcelainStub) Version(_ context.Context, dir string) (gitcmd.Version, error) {
	p.calls = append(p.calls, "version:"+dir)
	return p.version, p.versionErr
}

func (p *probePorcelainStub) ValidateBranch(_ context.Context, dir, branch string) error {
	p.calls = append(p.calls, "validate:"+dir+":"+branch)
	p.branchValue = branch
	return p.validateErr
}

func (p *probePorcelainStub) InspectLocal(_ context.Context, dir string) (gitcmd.LocalInspection, error) {
	p.calls = append(p.calls, "local:"+dir)
	return p.local, p.localErr
}

func (p *probePorcelainStub) InspectRemote(_ context.Context, dir, remote string) (gitcmd.RemoteInspection, error) {
	p.calls = append(p.calls, "remote:"+dir+":"+remote)
	p.remoteValue = remote
	return p.remote, p.remoteErr
}

func (p *probePorcelainStub) HistoryRelation(_ context.Context, dir, remoteOID string) (string, error) {
	p.calls = append(p.calls, "history:"+dir+":"+remoteOID)
	p.historyValue = remoteOID
	return p.history, p.historyErr
}

func newProbeFixture(t *testing.T) (string, probeSettingsStub, *probePorcelainStub) {
	t.Helper()
	dir := t.TempDir()
	settings := probeSettingsStub{config: model.Config{
		Bases:       []model.Base{{Name: "work", Path: dir}},
		CurrentBase: "work",
	}}
	porcelain := &probePorcelainStub{
		version: gitcmd.Version{Major: 2, Minor: 40, Patch: 1, Raw: "git version 2.40.1"},
		local: gitcmd.LocalInspection{
			HasRepository:      true,
			RepositoryRoot:     dir,
			CurrentBranch:      "main",
			WorkingTreeClean:   true,
			ExistingOriginURL:  probeRemote,
			IdentityConfigured: true,
			HasCommits:         true,
		},
		remote:  gitcmd.RemoteInspection{Branches: map[string]string{"main": mainOID}},
		history: "shared",
	}
	return dir, settings, porcelain
}

func runProbe(t *testing.T, settings SettingsSnapshot, porcelain GitPorcelain, request model.GitProbeRequest) (model.GitProbeResponse, error) {
	t.Helper()
	return NewGitProbeService(settings, porcelain).Probe(context.Background(), request)
}

func requireBlocker(t *testing.T, response model.GitProbeResponse, code, message, field string) {
	t.Helper()
	if response.BlockingError == nil {
		t.Fatalf("BlockingError = nil, want code %q", code)
	}
	want := model.APIError{Code: code, Message: message, Field: field}
	if *response.BlockingError != want {
		t.Fatalf("BlockingError = %#v, want %#v", *response.BlockingError, want)
	}
	if response.CanConfigure {
		t.Error("CanConfigure = true with a blocking error")
	}
}

func TestGitProbeServiceContractAndInputValidation(t *testing.T) {
	contract := reflect.TypeOf((*GitPorcelain)(nil)).Elem()
	if contract.NumMethod() != 5 {
		t.Fatalf("GitPorcelain method count = %d, want exactly five read-only methods", contract.NumMethod())
	}
	for _, name := range []string{"Version", "ValidateBranch", "InspectLocal", "InspectRemote", "HistoryRelation"} {
		if _, exists := contract.MethodByName(name); !exists {
			t.Errorf("GitPorcelain is missing %s", name)
		}
	}

	t.Run("unknown base is an exact-name field error", func(t *testing.T) {
		_, settings, porcelain := newProbeFixture(t)
		_, err := runProbe(t, settings, porcelain, model.GitProbeRequest{Base: "Work", GitURL: probeRemote})
		if !errors.Is(err, ErrBaseNotFound) {
			t.Fatalf("Probe() error = %v, want ErrBaseNotFound", err)
		}
		var fieldErr *FieldError
		if !errors.As(err, &fieldErr) || fieldErr.Field != "base" {
			t.Fatalf("Probe() error = %#v, want base FieldError", err)
		}
		if len(porcelain.calls) != 0 {
			t.Fatalf("porcelain calls = %v, want none", porcelain.calls)
		}
	})

	t.Run("empty base is rejected before Git", func(t *testing.T) {
		_, settings, porcelain := newProbeFixture(t)
		_, err := runProbe(t, settings, porcelain, model.GitProbeRequest{Base: " \t", GitURL: probeRemote})
		if !errors.Is(err, ErrBaseNotFound) {
			t.Fatalf("Probe() error = %v, want ErrBaseNotFound", err)
		}
		if len(porcelain.calls) != 0 {
			t.Fatalf("porcelain calls = %v, want none", porcelain.calls)
		}
	})

	for _, unsafeURL := range []string{
		"ext::sh -c touch /tmp/nope",
		"https://token@example.com/notes.git",
		"https://example.com/notes.git?token=secret",
	} {
		t.Run("unsafe candidate "+unsafeURL, func(t *testing.T) {
			_, settings, porcelain := newProbeFixture(t)
			_, err := runProbe(t, settings, porcelain, model.GitProbeRequest{Base: " work ", GitURL: unsafeURL})
			if !errors.Is(err, ErrInvalidGitURL) {
				t.Fatalf("Probe() error = %v, want ErrInvalidGitURL", err)
			}
			var fieldErr *FieldError
			if !errors.As(err, &fieldErr) || fieldErr.Field != "git_url" {
				t.Fatalf("Probe() error = %#v, want git_url FieldError", err)
			}
			if len(porcelain.calls) != 0 {
				t.Fatalf("porcelain calls = %v, want none", porcelain.calls)
			}
		})
	}

	t.Run("configured path is canonicalized before Git", func(t *testing.T) {
		realDir := t.TempDir()
		alias := filepath.Join(t.TempDir(), "base-link")
		if err := os.Symlink(realDir, alias); err != nil {
			t.Skipf("create directory symlink: %v", err)
		}
		settings := probeSettingsStub{config: model.Config{Bases: []model.Base{{Name: "work", Path: alias}}}}
		porcelain := &probePorcelainStub{
			version: gitcmd.Version{Major: 2, Minor: 40, Raw: "git version 2.40.0"},
			local:   gitcmd.LocalInspection{HasRepository: true, RepositoryRoot: realDir, WorkingTreeClean: true, IdentityConfigured: true},
			remote:  gitcmd.RemoteInspection{Empty: true, Branches: map[string]string{}},
		}
		response, err := runProbe(t, settings, porcelain, model.GitProbeRequest{Base: " work ", GitURL: " notes.git ", GitBranch: " main "})
		if err != nil {
			t.Fatalf("Probe() error = %v", err)
		}
		if response.Base != "work" || !response.RepositoryRootMatches {
			t.Fatalf("response base/root match = %q/%v, want work/true", response.Base, response.RepositoryRootMatches)
		}
		if porcelain.remoteValue != "notes.git" || porcelain.branchValue != "main" {
			t.Fatalf("normalized remote/branch = %q/%q, want notes.git/main", porcelain.remoteValue, porcelain.branchValue)
		}
		for _, call := range porcelain.calls {
			if !strings.Contains(call, realDir) {
				t.Fatalf("call %q does not use canonical directory %q", call, realDir)
			}
		}
	})
}

func TestGitProbeServiceRejectsNilDependencies(t *testing.T) {
	tests := []struct {
		name    string
		service func(*probePorcelainStub) *GitProbeService
	}{
		{name: "nil receiver", service: func(*probePorcelainStub) *GitProbeService { return nil }},
		{name: "nil settings", service: func(p *probePorcelainStub) *GitProbeService { return NewGitProbeService(nil, p) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			porcelain := &probePorcelainStub{}
			service := test.service(porcelain)
			response, err := service.Probe(context.Background(), model.GitProbeRequest{Base: "work", GitURL: probeRemote})
			if err != errGitProbeNotInitialized || err.Error() != "git probe service is not initialized" {
				t.Fatalf("Probe() error = %v, want fixed internal initialization error", err)
			}
			if errors.Is(err, ErrBaseNotFound) {
				t.Fatalf("Probe() error = %v, must not be classified as base not found", err)
			}
			var fieldErr *FieldError
			if errors.As(err, &fieldErr) {
				t.Fatalf("Probe() error = %#v, must not expose a field error", err)
			}
			if !reflect.DeepEqual(response, model.GitProbeResponse{}) {
				t.Fatalf("Probe() response = %#v, want zero response", response)
			}
			if len(porcelain.calls) != 0 {
				t.Fatalf("porcelain calls = %v, want none", porcelain.calls)
			}
		})
	}
}

func TestGitProbeServiceDiscoversRemoteWithEmptyBranch(t *testing.T) {
	dir, settings, porcelain := newProbeFixture(t)
	porcelain.remote.Branches = map[string]string{"zeta": devOID, "main": mainOID, "alpha": devOID}

	response, err := runProbe(t, settings, porcelain, model.GitProbeRequest{
		Base: " work ", GitURL: " " + probeRemote + " ", GitBranch: " \t",
	})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	wantCalls := []string{
		"version:" + dir,
		"local:" + dir,
		"remote:" + dir + ":" + probeRemote,
	}
	if !slices.Equal(porcelain.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", porcelain.calls, wantCalls)
	}
	if !slices.Equal(response.RemoteBranches, []string{"alpha", "main", "zeta"}) {
		t.Errorf("RemoteBranches = %v, want sorted branches", response.RemoteBranches)
	}
	if response.RemoteBranches == nil {
		t.Error("RemoteBranches = nil")
	}
	if response.HistoryRelation != "unknown" {
		t.Errorf("HistoryRelation = %q, want unknown", response.HistoryRelation)
	}
	if response.RequiredMutations.CreateBranch || response.RequiredMutations.MergeHistories {
		t.Errorf("branch mutations = %#v, want false", response.RequiredMutations)
	}
	if response.CanConfigure || response.BlockingError != nil {
		t.Errorf("CanConfigure/BlockingError = %v/%#v, want false/nil", response.CanConfigure, response.BlockingError)
	}
	if !slices.Equal(response.Warnings, []string{commitScope}) {
		t.Errorf("Warnings = %v, want deterministic commit warning", response.Warnings)
	}
}

func TestGitProbeServiceReprobesSelectedBranch(t *testing.T) {
	dir, settings, porcelain := newProbeFixture(t)
	porcelain.remote.Branches = map[string]string{"main": mainOID, "dev": devOID}
	response, err := runProbe(t, settings, porcelain, model.GitProbeRequest{
		Base: "work", GitURL: probeRemote, GitBranch: "main",
	})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	wantCalls := []string{
		"version:" + dir,
		"local:" + dir,
		"validate:" + dir + ":main",
		"remote:" + dir + ":" + probeRemote,
		"history:" + dir + ":" + mainOID,
	}
	if !slices.Equal(porcelain.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", porcelain.calls, wantCalls)
	}
	if response.HistoryRelation != "shared" || response.RequiredMutations.CreateBranch || response.RequiredMutations.MergeHistories {
		t.Errorf("selected branch facts = relation %q mutations %#v", response.HistoryRelation, response.RequiredMutations)
	}
	if !response.CanConfigure || response.BlockingError != nil {
		t.Errorf("CanConfigure/BlockingError = %v/%#v, want true/nil", response.CanConfigure, response.BlockingError)
	}
}

func TestGitProbeServiceVersionAndPorcelainErrorsAreSafe(t *testing.T) {
	tests := []struct {
		name        string
		configure   func(*probePorcelainStub)
		wantCode    string
		wantMessage string
		wantField   string
		wantCalls   int
	}{
		{
			name: "Git unavailable",
			configure: func(p *probePorcelainStub) {
				p.versionErr = &gitcmd.SafeError{Code: gitcmd.CodeUnavailable, Message: "SECRET executable detail"}
			},
			wantCode: "git_unavailable", wantMessage: "Git executable is unavailable", wantCalls: 1,
		},
		{
			name: "unsupported Git",
			configure: func(p *probePorcelainStub) {
				p.version = gitcmd.Version{Major: 2, Minor: 27, Patch: 9, Raw: "git version 2.27.9"}
			},
			wantCode: "git_version_unsupported", wantMessage: "Git 2.28 or newer is required", wantCalls: 1,
		},
		{
			name: "local inspection diagnostic",
			configure: func(p *probePorcelainStub) {
				p.localErr = errors.New("raw stderr SECRET_REMOTE")
			},
			wantCode: "git_command_failed", wantMessage: "Git command failed", wantCalls: 2,
		},
		{
			name: "branch validation diagnostic",
			configure: func(p *probePorcelainStub) {
				p.validateErr = &gitcmd.SafeError{Code: gitcmd.CodeInvalidBranch, Message: "bad branch SECRET"}
			},
			wantCode: "invalid_branch", wantMessage: "Invalid Git branch", wantField: "git_branch", wantCalls: 3,
		},
		{
			name: "remote diagnostic",
			configure: func(p *probePorcelainStub) {
				p.remoteErr = &gitcmd.SafeError{Code: gitcmd.CodeAuthentication, Message: "token SECRET authentication"}
			},
			wantCode: "auth_failed", wantMessage: "Git authentication failed", wantCalls: 4,
		},
		{
			name: "history diagnostic",
			configure: func(p *probePorcelainStub) {
				p.historyErr = &gitcmd.SafeError{Code: gitcmd.CodeTimedOut, Message: "timeout SECRET remote"}
			},
			wantCode: "git_timeout", wantMessage: "Git command timed out", wantCalls: 5,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, settings, porcelain := newProbeFixture(t)
			test.configure(porcelain)
			response, err := runProbe(t, settings, porcelain, model.GitProbeRequest{Base: "work", GitURL: probeRemote, GitBranch: "main"})
			if err != nil {
				t.Fatalf("Probe() error = %v", err)
			}
			requireBlocker(t, response, test.wantCode, test.wantMessage, test.wantField)
			if len(porcelain.calls) != test.wantCalls {
				t.Fatalf("porcelain calls = %v, want %d ordered calls", porcelain.calls, test.wantCalls)
			}
			serialized := response.BlockingError.Code + response.BlockingError.Message + response.BlockingError.Field
			if strings.Contains(serialized, "SECRET") || strings.Contains(serialized, "stderr") {
				t.Fatalf("BlockingError leaked diagnostic: %#v", response.BlockingError)
			}
			if response.RemoteBranches == nil || response.Warnings == nil {
				t.Fatalf("response slices must be non-nil: %#v", response)
			}
		})
	}
}

func TestGitProbeServiceDerivesLocalStateAndOriginMutations(t *testing.T) {
	t.Run("no repository is a matching creatable root", func(t *testing.T) {
		_, settings, porcelain := newProbeFixture(t)
		porcelain.local = gitcmd.LocalInspection{IdentityConfigured: true}
		porcelain.remote = gitcmd.RemoteInspection{Empty: true, Branches: map[string]string{}}
		response, err := runProbe(t, settings, porcelain, model.GitProbeRequest{Base: "work", GitURL: probeRemote, GitBranch: "main"})
		if err != nil {
			t.Fatalf("Probe() error = %v", err)
		}
		if response.HasRepository || !response.RepositoryRootMatches {
			t.Errorf("repository facts = has %v matches %v, want false/true", response.HasRepository, response.RepositoryRootMatches)
		}
		want := model.GitRequiredMutations{CreateRepository: true, AddOrigin: true, CreateBranch: true}
		if response.RequiredMutations != want {
			t.Errorf("RequiredMutations = %#v, want %#v", response.RequiredMutations, want)
		}
		if !response.CanConfigure || response.HistoryRelation != "none" {
			t.Errorf("CanConfigure/HistoryRelation = %v/%q, want true/none", response.CanConfigure, response.HistoryRelation)
		}
	})

	t.Run("no repository without identity is blocked", func(t *testing.T) {
		_, settings, porcelain := newProbeFixture(t)
		porcelain.local = gitcmd.LocalInspection{}
		porcelain.remote = gitcmd.RemoteInspection{Empty: true, Branches: map[string]string{}}
		response, err := runProbe(t, settings, porcelain, model.GitProbeRequest{Base: "work", GitURL: probeRemote, GitBranch: "main"})
		if err != nil {
			t.Fatalf("Probe() error = %v", err)
		}
		requireBlocker(t, response, "identity_missing", "Git identity is not configured", "")
		want := model.GitRequiredMutations{CreateRepository: true, AddOrigin: true, CreateBranch: true}
		if response.RequiredMutations != want {
			t.Errorf("RequiredMutations = %#v, want %#v", response.RequiredMutations, want)
		}
	})

	t.Run("parent repository returns complete blocking response", func(t *testing.T) {
		base, settings, porcelain := newProbeFixture(t)
		porcelain.local.RepositoryRoot = filepath.Dir(base)
		response, err := runProbe(t, settings, porcelain, model.GitProbeRequest{Base: "work", GitURL: probeRemote, GitBranch: "main"})
		if err != nil {
			t.Fatalf("Probe() error = %v", err)
		}
		if response.RepositoryRootMatches {
			t.Error("RepositoryRootMatches = true for parent repository")
		}
		if response.RepositoryRoot != filepath.Dir(base) || response.GitVersion == "" || len(response.RemoteBranches) == 0 {
			t.Fatalf("response is incomplete: %#v", response)
		}
		requireBlocker(t, response, "repository_root_mismatch", "Git repository root does not match the base directory", "")
	})

	tests := []struct {
		name       string
		mutate     func(*probePorcelainStub)
		wantOrigin string
		want       model.GitRequiredMutations
		blockCode  string
		blockMsg   string
	}{
		{
			name:   "missing origin",
			mutate: func(p *probePorcelainStub) { p.local.ExistingOriginURL = "" },
			want:   model.GitRequiredMutations{AddOrigin: true},
		},
		{
			name:       "matching origin",
			mutate:     func(p *probePorcelainStub) { p.local.ExistingOriginURL = probeRemote },
			wantOrigin: probeRemote,
		},
		{
			name:       "different origin",
			mutate:     func(p *probePorcelainStub) { p.local.ExistingOriginURL = "https://example.com/other.git" },
			wantOrigin: "https://example.com/other.git",
			want:       model.GitRequiredMutations{ReplaceOrigin: true},
		},
		{
			name:       "pending operation",
			mutate:     func(p *probePorcelainStub) { p.local.PendingOperation = "rebase" },
			wantOrigin: probeRemote,
			blockCode:  "repository_locked", blockMsg: "Git repository has a pending operation",
		},
		{
			name:       "missing identity",
			mutate:     func(p *probePorcelainStub) { p.local.IdentityConfigured = false },
			wantOrigin: probeRemote,
			blockCode:  "identity_missing", blockMsg: "Git identity is not configured",
		},
		{
			name: "detached dirty repository is reported",
			mutate: func(p *probePorcelainStub) {
				p.local.CurrentBranch = ""
				p.local.DetachedHead = true
				p.local.WorkingTreeClean = false
			},
			wantOrigin: probeRemote,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, settings, porcelain := newProbeFixture(t)
			test.mutate(porcelain)
			response, err := runProbe(t, settings, porcelain, model.GitProbeRequest{Base: "work", GitURL: probeRemote, GitBranch: "main"})
			if err != nil {
				t.Fatalf("Probe() error = %v", err)
			}
			if response.ExistingOriginURL != test.wantOrigin || response.RequiredMutations != test.want {
				t.Errorf("origin/mutations = %q/%#v, want %q/%#v", response.ExistingOriginURL, response.RequiredMutations, test.wantOrigin, test.want)
			}
			if test.name == "detached dirty repository is reported" && (!response.DetachedHead || response.WorkingTreeClean || response.CurrentBranch != "") {
				t.Errorf("detached/dirty fields not mapped exactly: %#v", response)
			}
			if test.blockCode != "" {
				requireBlocker(t, response, test.blockCode, test.blockMsg, "")
			} else if !response.CanConfigure || response.BlockingError != nil {
				t.Errorf("CanConfigure/BlockingError = %v/%#v, want true/nil", response.CanConfigure, response.BlockingError)
			}
		})
	}

	for _, unsafeOrigin := range []string{
		"ext::touch /tmp/nope",
		"https://token@example.com/notes.git",
		"https://example.com/notes.git?token=SECRET",
	} {
		t.Run("unsafe configured origin is omitted "+unsafeOrigin, func(t *testing.T) {
			_, settings, porcelain := newProbeFixture(t)
			porcelain.local.ExistingOriginURL = unsafeOrigin
			response, err := runProbe(t, settings, porcelain, model.GitProbeRequest{Base: "work", GitURL: probeRemote, GitBranch: "main"})
			if err != nil {
				t.Fatalf("Probe() error = %v", err)
			}
			if response.ExistingOriginURL != "" {
				t.Fatalf("ExistingOriginURL leaked unsafe value %q", response.ExistingOriginURL)
			}
			if !response.RequiredMutations.ReplaceOrigin {
				t.Error("ReplaceOrigin = false for unsafe configured origin")
			}
			requireBlocker(t, response, "invalid_git_url", "Invalid Git URL", "git_url")
			if strings.Contains(response.BlockingError.Message, "SECRET") {
				t.Fatalf("BlockingError leaked origin: %#v", response.BlockingError)
			}
		})
	}
}

func TestGitProbeServiceUsesDeterministicIndependentBlockerPrecedence(t *testing.T) {
	base, settings, porcelain := newProbeFixture(t)
	porcelain.local.RepositoryRoot = filepath.Dir(base)
	porcelain.local.PendingOperation = "merge"
	porcelain.local.IdentityConfigured = false
	porcelain.local.ExistingOriginURL = "https://token@example.com/SECRET.git"
	porcelain.remote.Branches = map[string]string{"dev": devOID}
	response, err := runProbe(t, settings, porcelain, model.GitProbeRequest{Base: "work", GitURL: probeRemote})
	if err != nil {
		t.Fatalf("branchless Probe() error = %v", err)
	}
	requireBlocker(t, response, "invalid_git_url", "Invalid Git URL", "git_url")
	if response.HistoryRelation != "unknown" || response.RequiredMutations.CreateBranch || response.RequiredMutations.MergeHistories {
		t.Fatalf("branchless response derived branch facts: %#v", response)
	}

	porcelain.calls = nil
	porcelain.local.ExistingOriginURL = probeRemote
	response, err = runProbe(t, settings, porcelain, model.GitProbeRequest{Base: "work", GitURL: probeRemote, GitBranch: "main"})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	requireBlocker(t, response, "repository_root_mismatch", "Git repository root does not match the base directory", "")

	porcelain.calls = nil
	porcelain.local.RepositoryRoot = base
	response, err = runProbe(t, settings, porcelain, model.GitProbeRequest{Base: "work", GitURL: probeRemote, GitBranch: "main"})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	requireBlocker(t, response, "repository_locked", "Git repository has a pending operation", "")

	porcelain.calls = nil
	porcelain.local.PendingOperation = ""
	response, err = runProbe(t, settings, porcelain, model.GitProbeRequest{Base: "work", GitURL: probeRemote, GitBranch: "main"})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	requireBlocker(t, response, "identity_missing", "Git identity is not configured", "")
}

func TestGitProbeServiceDerivesRemoteAndSelectedBranchState(t *testing.T) {
	tests := []struct {
		name          string
		branch        string
		remote        gitcmd.RemoteInspection
		hasCommits    bool
		wantBranches  []string
		wantEmpty     bool
		wantCreate    bool
		wantRelation  string
		wantBlockCode string
		wantCan       bool
		wantHistory   bool
	}{
		{
			name:         "branchless empty remote",
			remote:       gitcmd.RemoteInspection{Empty: true, Branches: map[string]string{}},
			wantBranches: []string{}, wantEmpty: true, wantRelation: "unknown",
		},
		{
			name:         "branchless tag-only remote",
			remote:       gitcmd.RemoteInspection{Empty: false, Branches: map[string]string{}},
			wantBranches: []string{}, wantRelation: "unknown",
		},
		{
			name:   "selected branch on empty remote",
			branch: "main", remote: gitcmd.RemoteInspection{Empty: true, Branches: map[string]string{}},
			wantBranches: []string{}, wantEmpty: true, wantCreate: true, wantRelation: "none", wantCan: true,
		},
		{
			name:   "selected branch absent from tag-only remote",
			branch: "main", remote: gitcmd.RemoteInspection{Empty: false, Branches: map[string]string{}},
			wantBranches: []string{}, wantRelation: "unknown", wantBlockCode: "invalid_branch",
		},
		{
			name:   "selected branch absent from nonempty remote",
			branch: "main", remote: gitcmd.RemoteInspection{Branches: map[string]string{"dev": devOID}},
			wantBranches: []string{"dev"}, wantRelation: "unknown", wantBlockCode: "invalid_branch",
		},
		{
			name:   "selected branch with no local commits",
			branch: "main", remote: gitcmd.RemoteInspection{Branches: map[string]string{"zeta": devOID, "main": mainOID, "dev": devOID}},
			wantBranches: []string{"dev", "main", "zeta"}, wantRelation: "none", wantCan: true,
		},
		{
			name:   "selected branch with local commits",
			branch: "main", remote: gitcmd.RemoteInspection{Branches: map[string]string{"main": mainOID}}, hasCommits: true,
			wantBranches: []string{"main"}, wantRelation: "shared", wantCan: true, wantHistory: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, settings, porcelain := newProbeFixture(t)
			porcelain.remote = test.remote
			porcelain.local.HasCommits = test.hasCommits
			response, err := runProbe(t, settings, porcelain, model.GitProbeRequest{Base: "work", GitURL: probeRemote, GitBranch: test.branch})
			if err != nil {
				t.Fatalf("Probe() error = %v", err)
			}
			if response.EmptyRemote != test.wantEmpty || !slices.Equal(response.RemoteBranches, test.wantBranches) || response.RemoteBranches == nil {
				t.Errorf("remote facts = empty %v branches %v, want %v/%v nonnil", response.EmptyRemote, response.RemoteBranches, test.wantEmpty, test.wantBranches)
			}
			if response.RequiredMutations.CreateBranch != test.wantCreate || response.RequiredMutations.MergeHistories {
				t.Errorf("branch mutations = %#v, want create=%v merge=false", response.RequiredMutations, test.wantCreate)
			}
			if response.HistoryRelation != test.wantRelation {
				t.Errorf("HistoryRelation = %q, want %q", response.HistoryRelation, test.wantRelation)
			}
			if (porcelain.historyValue != "") != test.wantHistory {
				t.Errorf("HistoryRelation called = %v, want %v", porcelain.historyValue != "", test.wantHistory)
			}
			if test.wantBlockCode != "" {
				requireBlocker(t, response, test.wantBlockCode, "Selected Git branch does not exist on the remote", "git_branch")
			} else if response.CanConfigure != test.wantCan || response.BlockingError != nil {
				t.Errorf("CanConfigure/BlockingError = %v/%#v, want %v/nil", response.CanConfigure, response.BlockingError, test.wantCan)
			}
		})
	}
}

func TestGitProbeServiceDerivesHistoryConservatively(t *testing.T) {
	tests := []struct {
		name       string
		hasCommits bool
		relation   string
		want       string
		wantMerge  bool
		wantWarn   bool
	}{
		{name: "no local history", relation: "shared", want: "none"},
		{name: "shared history", hasCommits: true, relation: "shared", want: "shared"},
		{name: "unrelated history", hasCommits: true, relation: "unrelated", want: "unrelated", wantMerge: true, wantWarn: true},
		{name: "unknown history", hasCommits: true, relation: "unknown", want: "unknown", wantMerge: true, wantWarn: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, settings, porcelain := newProbeFixture(t)
			porcelain.local.HasCommits = test.hasCommits
			porcelain.history = test.relation
			response, err := runProbe(t, settings, porcelain, model.GitProbeRequest{Base: "work", GitURL: probeRemote, GitBranch: "main"})
			if err != nil {
				t.Fatalf("Probe() error = %v", err)
			}
			if response.HistoryRelation != test.want || response.RequiredMutations.MergeHistories != test.wantMerge {
				t.Errorf("history = %q merge=%v, want %q/%v", response.HistoryRelation, response.RequiredMutations.MergeHistories, test.want, test.wantMerge)
			}
			wantWarnings := []string{commitScope}
			if test.wantWarn {
				wantWarnings = append(wantWarnings, mergeRisk)
			}
			if !slices.Equal(response.Warnings, wantWarnings) {
				t.Errorf("Warnings = %v, want %v", response.Warnings, wantWarnings)
			}
		})
	}
}
