package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"IGoNotes/internal/fileidentity"
	"IGoNotes/internal/model"
)

// PrivateStoragePolicy reserves destinations, not the entire application data
// directory. Fields are immutable; aliases are resolved again on each use.
type PrivateStoragePolicy struct {
	configDir  string
	sqlitePath string
	stagingDir string
}

func (p *PrivateStoragePolicy) clone() *PrivateStoragePolicy {
	if p == nil {
		return nil
	}
	snapshot := *p
	return &snapshot
}

// NewPrivateStoragePolicy captures absolute destinations without creating them.
func NewPrivateStoragePolicy(configDir, sqlitePath, stagingDir string) (*PrivateStoragePolicy, error) {
	p := &PrivateStoragePolicy{}
	for _, entry := range []struct {
		src string
		dst *string
	}{{configDir, &p.configDir}, {sqlitePath, &p.sqlitePath}, {stagingDir, &p.stagingDir}} {
		if entry.src == "" {
			return nil, fmt.Errorf("private storage layout requires all destinations")
		}
		path, err := filepath.Abs(entry.src)
		if err != nil {
			return nil, fmt.Errorf("resolve private storage layout: %w", err)
		}
		*entry.dst = filepath.Clean(path)
	}
	return p, nil
}

// canonicalProspectivePath resolves every existing ancestor before appending
// missing components. Broken symlinks and inaccessible ancestors fail closed.
func canonicalProspectivePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path = filepath.Clean(abs)
	var missing []string
	for {
		_, err := os.Lstat(path)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		missing = append(missing, filepath.Base(path))
		path = parent
	}
}

func privatePathContains(parent, child string) bool {
	return privatePathContainsWithStat(parent, child, fileidentity.Stat)
}

func privatePathContainsWithStat(parent, child string, stat func(string) (os.FileInfo, error)) bool {
	if privateLexicalContains(parent, child) {
		return true
	}
	// Native aliases can have different volumes/spellings even after symlink
	// resolution. Anchor the prospective parent at its nearest existing ancestor
	// and retain its missing suffix; neither prospective path has to exist.
	anchor := parent
	var parentInfo os.FileInfo
	for {
		info, err := stat(anchor)
		if err == nil {
			parentInfo = info
			break
		}
		if !os.IsNotExist(err) || filepath.Dir(anchor) == anchor {
			return false
		}
		anchor = filepath.Dir(anchor)
	}
	parentTail, err := filepath.Rel(anchor, parent)
	if err != nil {
		return false
	}
	// Ascend the child's spelling until we find the same physical anchor. Compare
	// the complete remaining component sequences, preserving directional
	// containment (not merely equality of missing tails).
	for current := child; ; current = filepath.Dir(current) {
		info, err := stat(current)
		if err == nil && samePhysicalFile(parentInfo, info) {
			childTail, err := filepath.Rel(current, child)
			return err == nil && privateLexicalContains(parentTail, childTail)
		}
		if err != nil && !os.IsNotExist(err) {
			return false
		}
		if filepath.Dir(current) == current {
			return false
		}
	}
}

func privateLexicalContains(parent, child string) bool {
	if runtime.GOOS == "windows" {
		parent = strings.ToLower(parent)
		child = strings.ToLower(child)
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func privatePathError() error {
	return fieldError(ErrInvalidPath, "path", "base path overlaps private application storage")
}

// ValidateBasePath checks a prospective or existing base without filesystem writes.
// Errors are safe for the public settings API and never contain private paths.
func (p *PrivateStoragePolicy) ValidateBasePath(path string) error {
	// Pathless ConfigStore fakes retain compatibility. Real ConfigService always
	// supplies at least its own configuration-directory reservation.
	if p == nil {
		return nil
	}
	if p.configDir == "" || path == "" {
		return privatePathError()
	}
	base, err := canonicalProspectivePath(path)
	if err != nil {
		return privatePathError()
	}
	for _, dir := range []string{p.configDir, p.stagingDir} {
		if dir == "" {
			continue
		}
		private, err := canonicalProspectivePath(dir)
		if err != nil || privatePathContains(base, private) || privatePathContains(private, base) {
			return privatePathError()
		}
	}
	if p.sqlitePath != "" {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			private, err := canonicalProspectivePath(p.sqlitePath + suffix)
			if err != nil || privatePathContains(base, private) {
				return privatePathError()
			}
		}
		// SQLite super-journals use <database>-mj<random> names.
		db, err := canonicalProspectivePath(p.sqlitePath)
		if err != nil {
			return privatePathError()
		}
		parent, name := filepath.Dir(base), filepath.Base(base)
		dbParent, dbName := filepath.Dir(db), filepath.Base(db)
		if runtime.GOOS == "windows" {
			name = strings.ToLower(name)
			dbName = strings.ToLower(dbName)
		}
		if privatePathContains(parent, dbParent) && privatePathContains(dbParent, parent) && strings.HasPrefix(name, dbName+"-mj") {
			return privatePathError()
		}
	}
	return nil
}

func (p *PrivateStoragePolicy) validateConfig(config model.Config) error {
	// An absent provider is supported for pathless stores, but an explicitly
	// supplied zero policy is invalid even when no bases require validation yet.
	if p != nil && p.configDir == "" {
		return privatePathError()
	}
	for _, base := range config.Bases {
		if err := p.ValidateBasePath(base.Path); err != nil {
			return err
		}
	}
	return nil
}

func privatePolicyFromStore(store ConfigStore) *PrivateStoragePolicy {
	if provider, ok := store.(interface{ PrivateStoragePolicy() *PrivateStoragePolicy }); ok {
		// Providers need not implement ConfigService's defensive-copy accessor.
		// Retain our own immutable snapshot before publishing settings.
		return provider.PrivateStoragePolicy().clone()
	}
	return nil
}

// OpenPrivateStagingRoot creates/opens only the reserved directory and returns a
// pinned handle. The caller owns it and must use root-relative IO, then Close.
// No lifecycle lock is retained during network IO. External hostile filesystem
// retargeting is not serialized by the application's coordinator.
func (s *SettingsService) OpenPrivateStagingRoot() (*os.Root, error) {
	s.lockMutation()
	defer s.unlockMutation()
	if s.privateStorage == nil || s.privateStorage.stagingDir == "" {
		return nil, fmt.Errorf("private staging layout is required")
	}
	if err := s.privateStorage.validateConfig(s.config); err != nil {
		return nil, err
	}
	path, err := canonicalProspectivePath(s.privateStorage.stagingDir)
	if err != nil {
		return nil, fmt.Errorf("resolve private staging: %w", err)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, fmt.Errorf("create private staging: %w", err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	if _, _, err := canonicalPinnedRootIdentity(root, s.privateStorage.stagingDir); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}
