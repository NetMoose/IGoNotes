package service

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"IGoNotes/internal/model"
)

// ClipperDestinationSnapshot is internal filesystem metadata, never a discovery
// response. RootIdentity is captured from an open file handle (also on Windows).
// Active describes the runtime while the callback holds NoteService.baseMu.
type ClipperDestinationSnapshot struct {
	Name          string      `json:"-"`
	CanonicalPath string      `json:"-"`
	RootIdentity  os.FileInfo `json:"-"`
	Active        bool        `json:"-"`
	binding       *clipperDestinationBinding
}

// A nonzero-sized binding has pointer identity across successful configuration
// publications. Deleting it makes an away-and-back (ABA) rebind irreversible for
// every registry sharing these settings, without retaining lifecycle history.
type clipperDestinationBinding struct {
	path string
}

type clipperDestinationRecord struct {
	snapshot ClipperDestinationSnapshot
	revision string
}

// ClipperDestinationService owns a process-local registry, not import receipts.
// A new registry never accepts previous handles. Keep one service for discovery
// and new imports; successful import replay must consult its durable receipt
// before using this service. Records contain metadata only, no open descriptors.
// The settings coordinator serializes all registry access.
type ClipperDestinationService struct {
	settings *SettingsService
	records  map[string]clipperDestinationRecord
}

func NewClipperDestinationService(settings *SettingsService) *ClipperDestinationService {
	return &ClipperDestinationService{settings: settings, records: make(map[string]clipperDestinationRecord)}
}

// ListBases returns detached names in configuration order, with no root paths or
// Git fields. Setup/auth guards belong to the caller's transport boundary.
func (s *ClipperDestinationService) ListBases() []string {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	names := make([]string, 0, len(s.settings.config.Bases))
	for _, base := range s.settings.config.Bases {
		names = append(names, base.Name)
	}
	return names
}

// Issue explicitly selects a configured base. An unchanged binding reuses its
// random 256-bit handle, bounding the registry to one record per configured name.
// It neither switches the runtime nor scans folders/notes or changes the index.
func (s *ClipperDestinationService) Issue(name string) (string, error) {
	s.settings.lockMutation()
	defer s.settings.unlockMutation()
	var record clipperDestinationRecord
	err := s.settings.withClipperDestinationSnapshotLocked(name, func(snapshot ClipperDestinationSnapshot, _ *os.Root) error {
		var err error
		record, err = s.issueSnapshotLocked(snapshot)
		return err
	})
	if err != nil {
		return "", err
	}
	return record.revision, nil
}

// issueSnapshotLocked reuses the already-open snapshot, never reacquiring any
// locks or reopening its configured pathname. Requires coordinator + settings.mu.
func (s *ClipperDestinationService) issueSnapshotLocked(snapshot ClipperDestinationSnapshot) (clipperDestinationRecord, error) {
	// Drop forgotten/renamed records on issuance; there is no per-request history.
	for recordedName := range s.records {
		if baseIndex(s.settings.config.Bases, recordedName) < 0 {
			delete(s.records, recordedName)
		}
	}
	if previous, ok := s.records[snapshot.Name]; ok && sameClipperDestination(previous.snapshot, snapshot) {
		return previous, nil
	}
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return clipperDestinationRecord{}, fmt.Errorf("generate Clipper destination revision: %w", err)
	}
	record := clipperDestinationRecord{snapshot: snapshot, revision: base64.RawURLEncoding.EncodeToString(entropy[:])}
	s.records[snapshot.Name] = record
	return record, nil
}

// WithDestination validates a handle against current configuration and physical
// root identity, then lends a scoped root to use. All locks stay held through use;
// the root is closed on every exit, including callback errors and panics. Do not
// retain/close the root or call services acquiring coordinator/settings/baseMu
// from use. Use only lower-level filesystem/repository work, with no network IO.
// This primitive does not bypass or implement the future import conflict gate.
func (s *ClipperDestinationService) WithDestination(name, revision string, use func(ClipperDestinationSnapshot, *os.Root) error) error {
	s.settings.coordinator.Lock()
	defer s.settings.coordinator.Unlock()
	return s.withDestinationCoordinatorLocked(name, revision, use)
}

// withDestinationCoordinatorLocked is the import publication entry point when
// the caller ALREADY holds coordinator. It acquires settings.mu then baseMu and
// must not be called with either of those locks held. It deliberately does not
// reacquire coordinator, so journal/publication can share one critical section.
func (s *ClipperDestinationService) withDestinationCoordinatorLocked(name, revision string, use func(ClipperDestinationSnapshot, *os.Root) error) error {
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	if use == nil {
		return &model.ClipperError{Code: model.ClipperInvalidRequest}
	}
	record, ok := s.records[name]
	if !ok || revision == "" || record.revision != revision || s.settings.clipperBindings[name] != record.snapshot.binding {
		return clipperDestinationChanged(nil)
	}
	return s.settings.withClipperDestinationSnapshotLocked(name, func(snapshot ClipperDestinationSnapshot, root *os.Root) error {
		if !sameClipperDestination(record.snapshot, snapshot) {
			return clipperDestinationChanged(nil)
		}
		return use(snapshot, root)
	})
}

func sameClipperDestination(left, right ClipperDestinationSnapshot) bool {
	return left.Name == right.Name && left.binding == right.binding &&
		left.CanonicalPath == right.CanonicalPath && samePhysicalFile(left.RootIdentity, right.RootIdentity)
}

func clipperDestinationChanged(cause error) error {
	return fmt.Errorf("inspect Clipper destination: %w", errors.Join(
		&model.ClipperError{Code: model.ClipperDestinationChanged}, cause,
	))
}

// invalidateClipperBindingsLocked runs only at successful settings publication,
// under coordinator + settings.mu. Git-only edits and current_base changes keep
// bindings; rename/path changes/forget delete them, even if later changed back.
func (s *SettingsService) invalidateClipperBindingsLocked(next model.Config) {
	for name, binding := range s.clipperBindings {
		index := baseIndex(next.Bases, name)
		if index < 0 || next.Bases[index].Path != binding.path {
			delete(s.clipperBindings, name)
		}
	}
}

// withClipperDestinationSnapshotLocked requires coordinator + settings.mu and
// acquires NoteService.baseMu exclusively, even for inactive bases. Callbacks may
// use only lower layers; future active publication may call replaceIndexLocked
// directly while this lock is held, never a public NoteService locking method.
// BaseRuntime stays unchanged: Clipper requires the real pinned NoteService.
func (s *SettingsService) withClipperDestinationSnapshotLocked(name string, use func(ClipperDestinationSnapshot, *os.Root) error) (err error) {
	if s.degraded != nil {
		return clipperDestinationChanged(s.degraded)
	}
	index := baseIndex(s.config.Bases, name)
	if name == "" || index < 0 {
		return &model.ClipperError{Code: model.ClipperDestinationNotFound}
	}
	notes, ok := s.notes.(*NoteService)
	if !ok || notes.coordinator != s.coordinator {
		return fmt.Errorf("Clipper pinned runtime dependency: %w", ErrInvalidConfig)
	}
	notes.baseMu.Lock()
	defer notes.baseMu.Unlock()
	if notes.baseErr != nil {
		return clipperDestinationChanged(notes.baseErr)
	}
	base := s.config.Bases[index]
	active := s.config.CurrentBase == name
	var root *os.Root
	if active {
		if notes.baseRoot == nil {
			return clipperDestinationChanged(ErrRuntimePathChanged)
		}
		// Borrow a separately closeable scoped handle to the runtime-pinned root,
		// never a newly opened replacement at its configured pathname.
		root, err = notes.baseRoot.OpenRoot(".")
	} else {
		root, err = openBaseRoot(base.Path)
	}
	if err != nil {
		return clipperDestinationChanged(err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, clipperDestinationChanged(closeErr))
		}
	}()
	canonicalPath, _, err := canonicalPinnedRootIdentity(root, base.Path)
	if err != nil {
		return clipperDestinationChanged(err)
	}
	// File.Stat eagerly captures Windows file IDs, unlike potentially lazy
	// pathname-based FileInfo. No file/root handles are retained in the registry.
	file, err := root.Open(".")
	if err != nil {
		return clipperDestinationChanged(err)
	}
	identity, statErr := file.Stat()
	if err := errors.Join(statErr, file.Close()); err != nil {
		return clipperDestinationChanged(err)
	}
	if s.clipperBindings == nil {
		s.clipperBindings = make(map[string]*clipperDestinationBinding)
	}
	binding := s.clipperBindings[name]
	if binding == nil {
		binding = &clipperDestinationBinding{path: base.Path}
		s.clipperBindings[name] = binding
	}
	return use(ClipperDestinationSnapshot{
		Name: name, CanonicalPath: canonicalPath, RootIdentity: identity,
		Active: active, binding: binding,
	}, root)
}
