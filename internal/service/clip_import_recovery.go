package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

// RecoverLocal reconciles unfinished private journals, without network access or
// public rollback. Call after Git local recovery and before starting workers/HTTP.
// Data/ownership ambiguities become durable safe recovery_required snapshots;
// persistence failures and cancellation are fatal to the caller. Terminal rows
// are authoritative and are never inspected, cleaned, indexed or republished.
func (s *ClipImportService) RecoverLocal(ctx context.Context) error {
	s.settings.coordinator.Lock()
	defer s.settings.coordinator.Unlock()
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	rows, err := s.store.ListUnfinished(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Reload under the coordinator, also guarding against a stale list snapshot.
		if err := s.reloadImport(ctx, &row); err != nil {
			return err
		}
		if row.State == repository.ClipImportSucceeded || row.State == repository.ClipImportRejected {
			continue
		}
		err := s.recoverClipConfiguredLocked(ctx, &row)
		var fatal *clipRecoveryPersistenceError
		if errors.As(err, &fatal) {
			return fatal.err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			if err := s.markClipRecoveryRequired(ctx, &row); err != nil {
				return err
			}
		}
	}
	return nil
}

type clipRecoveryPersistenceError struct{ err error }

func (e *clipRecoveryPersistenceError) Error() string { return "persist clip recovery" }
func (e *clipRecoveryPersistenceError) Unwrap() error { return e.err }
func clipRecoveryDB(err error) error {
	if err != nil {
		return &clipRecoveryPersistenceError{err}
	}
	return nil
}

func (s *ClipImportService) markClipRecoveryRequired(ctx context.Context, row *repository.ClipImportRecord) error {
	// Never infer an outcome after an unacknowledged persistence call.
	if err := s.reloadImport(ctx, row); err != nil {
		return err
	}
	if row.State == repository.ClipImportSucceeded || row.State == repository.ClipImportRejected {
		return nil
	}
	if row.State == repository.ClipImportRecoveryRequired {
		return nil
	}
	return s.store.Transition(ctx, row.ImportID, row.Version(), repository.ClipImportRecoveryRequired, nil,
		&model.APIError{Code: string(model.ClipperRecoveryRequired), Message: "Clipper import requires recovery"})
}

// Resolve only current configured snapshots. Historical paths are evidence, not
// an IO capability: a moved/renamed physical root may be rebound under a new name.
// The receipt still uses the original base/path; no runtime switch is performed.
func (s *ClipImportService) recoverClipConfiguredLocked(ctx context.Context, row *repository.ClipImportRecord) error {
	p, cp, err := decodeClipRecovery(*row)
	if err != nil {
		return err
	}
	identity, err := decodeClipImportRootIdentity(row.RootIdentity)
	if err != nil {
		return err
	}
	for _, base := range s.settings.config.Bases {
		matched := false
		err := s.settings.withClipperDestinationSnapshotLocked(base.Name, func(snapshot ClipperDestinationSnapshot, root *os.Root) error {
			file, err := root.Open(".")
			if err != nil {
				return err
			}
			id, e := clipIdentity(file)
			if err := errors.Join(e, file.Close()); err != nil {
				return err
			}
			if id != identity {
				return errClipFileChanged
			}
			matched = true
			if err := s.settings.coordinator.checkMutationForIdentity(snapshot.CanonicalPath, snapshot.RootIdentity); err != nil {
				return err
			}
			if err := s.settings.coordinator.CheckMutation(row.BasePath); err != nil {
				return err
			}
			return s.recoverClipFilesLocked(ctx, row, p, cp, snapshot, root)
		})
		if matched {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return errClipFileChanged
}

// strictClipJournal checks the exact recursive persisted schema before decoding:
// unknown/duplicate/escaped duplicate/missing/null keys and invalid Unicode fail
// closed. Presence is important, especially warnings and zero-valued progress.
func strictClipJournal(raw json.RawMessage, target any) error {
	if !utf8.Valid(raw) || !clipperJSONScalars(raw) {
		return os.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := clipJournalValue(d, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return os.ErrInvalid
	}
	return json.Unmarshal(raw, target)
}

func clipJournalValue(d *json.Decoder, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		token, err := d.Token()
		if err != nil || token != json.Delim('{') {
			return os.ErrInvalid
		}
		fields := make(map[string]reflect.Type, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			fields[strings.Split(f.Tag.Get("json"), ",")[0]] = f.Type
		}
		seen := make(map[string]bool, len(fields))
		for d.More() {
			token, err := d.Token()
			name, ok := token.(string)
			field, known := fields[name]
			if err != nil || !ok || !known || seen[name] {
				return os.ErrInvalid
			}
			seen[name] = true
			if err := clipJournalValue(d, field); err != nil {
				return err
			}
		}
		if token, err := d.Token(); err != nil || token != json.Delim('}') || len(seen) != len(fields) {
			return os.ErrInvalid
		}
	case reflect.Slice:
		if token, err := d.Token(); err != nil || token != json.Delim('[') {
			return os.ErrInvalid
		}
		for d.More() {
			if err := clipJournalValue(d, typ.Elem()); err != nil {
				return err
			}
		}
		if token, err := d.Token(); err != nil || token != json.Delim(']') {
			return os.ErrInvalid
		}
	default:
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil || bytes.Equal(raw, []byte("null")) {
			return os.ErrInvalid
		}
		if err := json.Unmarshal(raw, reflect.New(typ).Interface()); err != nil {
			return err
		}
	}
	return nil
}

// v1 is normalized in memory only; immutable plan references are never rewritten.
func decodeClipRecovery(row repository.ClipImportRecord) (clipImagePlan, clipImageCheckpoint, error) {
	var p clipImagePlan
	var cp clipImageCheckpoint
	invalid := func() (clipImagePlan, clipImageCheckpoint, error) { return p, cp, os.ErrInvalid }
	if row.JournalInvalid {
		return invalid()
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(row.Journal.Plan, &header); err != nil {
		return invalid()
	}
	switch header.Version {
	case 1:
		var text clipImportPlan
		var progress clipImportCheckpoint
		if strictClipJournal(row.Journal.Plan, &text) != nil || strictClipJournal(row.Journal.Checkpoint, &progress) != nil {
			return invalid()
		}
		p = clipImagePlan{Version: 1, Note: text.Note, Markdown: text.Markdown, Warnings: text.Warnings}
		cp = clipImageCheckpoint{Version: 1, Step: progress.Step, Note: progress.Note, Indexed: progress.Indexed}
	case 2:
		if strictClipJournal(row.Journal.Plan, &p) != nil || strictClipJournal(row.Journal.Checkpoint, &cp) != nil || cp.Version != 2 {
			return invalid()
		}
	default:
		return invalid()
	}
	if !clipperUUID.MatchString(row.ImportID) || !clipperSHA256.MatchString(row.RequestDigest) ||
		!filepath.IsAbs(row.BasePath) || filepath.Clean(row.BasePath) != row.BasePath || row.BaseName == "" ||
		!utf8.ValidString(row.BaseName) || strings.TrimSpace(row.BaseName) != row.BaseName || strings.TrimSpace(row.DestinationRevision) == "" ||
		p.Warnings == nil || len(*p.Warnings) > clipperMaxExternalImages || !utf8.ValidString(p.Markdown) || strings.TrimSpace(p.Markdown) == "" || len(p.Markdown) > model.ClipperMaxMarkdownBytes ||
		p.Note.SHA256 != clipContentHash([]byte(p.Markdown)) || p.Note.Size != int64(len(p.Markdown)) ||
		row.NotePath != path.Join(p.Note.Parent, p.Note.Filename) || row.NoteContentHash != p.Note.SHA256 || !strings.HasSuffix(p.Note.Filename, ".md") || p.Note.Filename == ".md" {
		return invalid()
	}
	for _, w := range *p.Warnings {
		if !utf8.ValidString(w) || strings.TrimSpace(w) == "" || len(w) > clipperMaxReasonBytes || strings.ContainsAny(w, "<>") || strings.ContainsFunc(w, unicode.IsControl) {
			return invalid()
		}
	}
	for _, part := range strings.Split(p.Note.Parent, "/") {
		if p.Note.Parent != "" && (!clipperSafeComponent(part) || !noteDirectoryVisible(part)) {
			return invalid()
		}
	}
	switch cp.Step {
	case "creating", "writing", "written", "published", "receipt", "recovery":
	default:
		return invalid()
	}
	if !validClipRecoveryFile(p.Note, cp.Note, model.ClipperMaxMarkdownBytes) {
		return invalid()
	}
	if (cp.Indexed || cp.Step == "receipt") && !cp.Note.Published {
		return invalid()
	}
	if p.Version == 1 {
		return p, cp, nil
	}
	if !clipGeneratedComponent(p.StagingID, "owned-", "") || p.StagingIdentity == (clipFileIdentity{}) || len(p.Assets) == 0 || len(p.Assets) > model.ClipperMaxAssets || len(cp.Assets) != len(p.Assets) {
		return invalid()
	}
	names := map[string]bool{p.Note.TempName: true, p.Note.Filename: true}
	identities := map[clipFileIdentity]bool{}
	if cp.Note.Identity != (clipFileIdentity{}) {
		identities[cp.Note.Identity] = true
	}
	ids := map[string]bool{}
	var total int64
	for i, a := range p.Assets {
		f := a.File
		ext := path.Ext(f.Filename)
		if f.Parent != "assets/images" || !validClipRecoveryFile(f, cp.Assets[i], model.ClipperMaxAssetBytes) ||
			(ext != ".png" && ext != ".jpg" && ext != ".gif" && ext != ".webp") || f.Filename != "clip-"+row.ImportID+"-"+f.SHA256+ext ||
			!clipGeneratedComponent(a.Source.Name, "source-", "") || a.Source.Identity == (clipFileIdentity{}) || len(a.IDs) == 0 ||
			(i > 0 && p.Assets[i-1].File.Filename >= f.Filename) {
			return invalid()
		}
		if (cp.Indexed || cp.Step == "receipt") && !cp.Assets[i].Published {
			return invalid()
		}
		if id := cp.Assets[i].Identity; id != (clipFileIdentity{}) {
			if identities[id] {
				return invalid()
			}
			identities[id] = true
		}
		for _, name := range []string{f.Filename, f.TempName, a.Source.Name} {
			if names[name] {
				return invalid()
			}
			names[name] = true
		}
		for j, id := range a.IDs {
			if !clipperAssetID.MatchString(id) || id == "manifest" || ids[id] || (j > 0 && a.IDs[j-1] >= id) {
				return invalid()
			}
			ids[id] = true
			total += f.Size
		}
		if len(ids) > model.ClipperMaxAssets || total > model.ClipperMaxTotalAssetBytes {
			return invalid()
		}
	}
	return p, cp, nil
}

func clipGeneratedComponent(name, prefix, suffix string) bool {
	return clipperSafeComponent(name) && strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) && len(name) > len(prefix)+len(suffix)
}
func validClipRecoveryFile(p clipNotePlan, cp clipFileProgress, max int64) bool {
	if !clipperSafeComponent(p.Filename) || !clipGeneratedComponent(p.TempName, "igonotes-clip-", ".tmp") || p.TempName == p.Filename || !clipperSHA256.MatchString(p.SHA256) || p.Size <= 0 || p.Size > max {
		return false
	}
	if cp.Identity == (clipFileIdentity{}) {
		return cp == (clipFileProgress{})
	}
	return clipperSHA256.MatchString(cp.SHA256) && cp.Size >= 0 && cp.Size <= p.Size && (!cp.Durable || cp.Published) &&
		(!cp.Published || (cp.SHA256 == p.SHA256 && cp.Size == p.Size)) && !(cp.Published && cp.PublicationUncertain) &&
		(cp.Size != p.Size || cp.SHA256 == p.SHA256) && (cp.Size != 0 || cp.SHA256 == clipContentHash(nil))
}

// All ownership checks run before public effects. No hash-only adoption, no late
// deletion, and no creation if a prior known inode has disappeared.
func reconcileClipFile(f *clipNoteFile) error {
	_, finalErr := f.parent.Lstat(f.plan.Filename)
	_, tempErr := f.parent.Lstat(f.plan.TempName)
	if finalErr != nil && !errors.Is(finalErr, os.ErrNotExist) {
		return finalErr
	}
	if tempErr != nil && !errors.Is(tempErr, os.ErrNotExist) {
		return tempErr
	}
	known := f.progress.Identity != (clipFileIdentity{})
	if finalErr == nil {
		if !known || tempErr == nil {
			return errClipFileChanged
		}
		f.owned = true
		f.progress.SHA256, f.progress.Size = f.plan.SHA256, f.plan.Size
		temp := f.plan.TempName
		f.plan.TempName = f.plan.Filename
		err := f.checkTemp()
		f.plan.TempName = temp
		if err != nil {
			return err
		}
		f.progress.Published, f.progress.PublicationUncertain = true, false
		return nil
	}
	if f.progress.Published {
		return errClipFileChanged
	}
	if tempErr != nil {
		if known {
			return errClipFileChanged
		}
		return nil // New O_EXCL creation is permitted by the complete plan.
	}
	if !known {
		return errClipFileChanged
	} // create-before-identity-checkpoint gap
	f.owned = true
	original := f.progress
	f.progress.SHA256, f.progress.Size = f.plan.SHA256, f.plan.Size
	if f.checkTemp() == nil {
		f.ready = true // a full write may have completed before its checkpoint
	} else {
		f.progress = original
		if err := f.checkTemp(); err != nil {
			return err
		}
	}
	f.progress.PublicationUncertain = false // observed own temp, final absent
	return nil
}

func (s *ClipImportService) recoverClipFilesLocked(ctx context.Context, row *repository.ClipImportRecord, p clipImagePlan, cp clipImageCheckpoint, snapshot ClipperDestinationSnapshot, root *os.Root) error {
	var sourceRoot, sourceParent *os.Root
	if p.Version == 2 {
		var err error
		sourceParent, sourceRoot, err = s.openClipRecoverySourcesLocked(ctx, p)
		if err != nil {
			return err
		}
		defer sourceParent.Close()
		defer sourceRoot.Close()
	}
	note, err := s.prepareRecovery(root, p.Note)
	if err != nil {
		return err
	}
	defer note.close()
	note.markdown = p.Markdown
	note.progress = cp.Note
	note.source = func() (io.ReadCloser, error) {
		return io.NopCloser(clipContextReader{ctx, strings.NewReader(p.Markdown)}), nil
	}
	if err := reconcileClipFile(note); err != nil {
		return err
	}
	var assets []*clipNoteFile
	defer func() {
		for _, f := range assets {
			_ = f.close()
		}
	}()
	if p.Version == 2 {
		assetRoot, err := clipAssetDirectory(root, false)
		if errors.Is(err, os.ErrNotExist) {
			// No known asset may disappear, and an already published note must
			// never be repaired by recreating later-deleted dependencies.
			if note.progress.Published {
				return errClipFileChanged
			}
			for _, progress := range cp.Assets {
				if progress != (clipFileProgress{}) {
					return errClipFileChanged
				}
			}
			// Persist recovery admission before creating even empty public
			// directories. Retain the original immutable plan and ownership.
			raw, _ := json.Marshal(cp)
			if err := s.store.Checkpoint(ctx, row.ImportID, row.Version(), raw); err != nil {
				return clipRecoveryDB(err)
			}
			if err := s.reloadImport(ctx, row); err != nil {
				return clipRecoveryDB(err)
			}
			assetRoot, err = clipAssetDirectory(root, true)
		}
		if err != nil {
			return err
		}
		defer assetRoot.Close()
		for i, a := range p.Assets {
			a := a
			local := a.File
			local.Parent = ""
			f, err := s.prepareRecovery(assetRoot, local)
			if err != nil {
				return err
			}
			f.plan = a.File
			f.source = func() (io.ReadCloser, error) {
				file, err := sourceRoot.Open(a.Source.Name)
				if err != nil {
					return nil, err
				}
				id, e := clipIdentity(file)
				if e != nil || id != a.Source.Identity {
					file.Close()
					return nil, errClipFileChanged
				}
				return &clipSourceReader{Reader: clipContextReader{ctx, file}, Closer: file}, nil
			}
			assets = append(assets, f)
			f.progress = cp.Assets[i]
			if err := reconcileClipFile(f); err != nil {
				return err
			}
			if note.progress.Published && !f.progress.Published {
				return errClipFileChanged
			}
		}
	}
	all := append(append([]*clipNoteFile{}, assets...), note)
	save := func(step string) error {
		cp.Step = step
		cp.Note = note.progress
		for i, f := range assets {
			cp.Assets[i] = f.progress
		}
		var raw []byte
		if p.Version == 1 {
			raw, _ = json.Marshal(clipImportCheckpoint{Step: step, Note: cp.Note, Indexed: cp.Indexed})
		} else {
			raw, _ = json.Marshal(cp)
		}
		if err := s.store.Checkpoint(ctx, row.ImportID, row.Version(), raw); err != nil {
			return clipRecoveryDB(err)
		}
		return clipRecoveryDB(s.reloadImport(ctx, row))
	}
	if row.State != repository.ClipImportPublishing {
		if err := s.store.Transition(ctx, row.ImportID, row.Version(), repository.ClipImportPublishing, nil, nil); err != nil {
			return clipRecoveryDB(err)
		}
		if err := s.reloadImport(ctx, row); err != nil {
			return clipRecoveryDB(err)
		}
	}
	if err := save("creating"); err != nil {
		return err
	}
	for _, f := range all {
		if err := ctx.Err(); err != nil {
			return err
		}
		if f.progress.Published {
			if err := syncClipRecoveredFile(f); err != nil {
				return err
			}
			if err := f.ops.syncParent(f.dir); err != nil {
				return err
			}
			f.progress.Durable = true
			continue
		}
		if !f.owned {
			if _, err := f.create(); err != nil {
				if e := save("recovery"); e != nil {
					return e
				}
				return err
			}
			if err := save("writing"); err != nil {
				return err
			}
		} else if !f.ready {
			// Resume only a byte-for-byte checkpointed partial owned temp.
			if err := f.checkTemp(); err != nil {
				return err
			}
			file, err := f.parent.OpenFile(f.plan.TempName, os.O_RDWR, 0)
			if err != nil {
				return err
			}
			id, e := clipIdentity(file)
			if e != nil || id != f.progress.Identity {
				file.Close()
				return errClipFileChanged
			}
			f.file = file
			if err := save("writing"); err != nil {
				return err
			}
			if err := file.Truncate(0); err != nil {
				return err
			}
		}
		if !f.ready {
			_, err := f.write()
			if e := save("written"); e != nil {
				return e
			}
			if err != nil {
				return err
			}
		}
		// Persist reconciled identities/content before attempting each rename.
		if err := syncClipRecoveredFile(f); err != nil {
			return err
		}
		if err := save("written"); err != nil {
			return err
		}
		if f == note {
			// External writers do not take coordinator. Recheck dependencies at
			// the note-last boundary, not only at admission/after publication.
			for _, asset := range assets {
				if !asset.progress.Published {
					return errClipFileChanged
				}
				if err := reconcileClipFile(asset); err != nil {
					return err
				}
			}
		}
		_, err := f.publish()
		if e := save("published"); e != nil {
			return e
		}
		if err != nil {
			return err
		}
	}
	// Reinspect all final locations before indexing/receipt; assets still must
	// match even when the note's published checkpoint lagged its rename.
	for _, f := range all {
		if err := reconcileClipFile(f); err != nil {
			return err
		}
	}
	if snapshot.Active {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.replaceClipRecoveryIndexLocked(ctx); err != nil {
			return err
		}
		cp.Indexed = true
	}
	if err := save("receipt"); err != nil {
		return err
	}
	result := &model.ClipperImportResult{ImportID: row.ImportID, Base: row.BaseName, Path: row.NotePath, Revision: noteRevision([]byte(p.Markdown)), Warnings: append([]string{}, (*p.Warnings)...)}
	if err := s.store.Transition(ctx, row.ImportID, row.Version(), repository.ClipImportSucceeded, result, nil); err != nil {
		return clipRecoveryDB(err)
	}
	// Receipt is durable. Only private, revalidated owned sources may be removed;
	// no recursive deletion and no public cleanup on any recovery outcome.
	if sourceRoot != nil {
		cleanupClipRecoverySources(sourceParent, sourceRoot, p)
	}
	return nil
}

// A full write/rename observed after a crash is not evidence that its file fsync
// completed. Reestablish durability on the same owned inode before a receipt.
func syncClipRecoveredFile(f *clipNoteFile) error {
	name := f.plan.TempName
	if f.progress.Published {
		name = f.plan.Filename
	}
	file, err := f.parent.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	id, err := clipIdentity(file)
	if err != nil || id != f.progress.Identity {
		return errClipFileChanged
	}
	if err := f.ops.sync(file); err != nil {
		return err
	}
	return file.Close()
}

// Caller holds coordinator/settings/baseMu. Never re-enter the public staging
// opener (it acquires those locks), create a vanished source, or follow aliases.
func (s *ClipImportService) openClipRecoverySourcesLocked(ctx context.Context, p clipImagePlan) (parent, root *os.Root, err error) {
	policy := s.settings.privateStorage
	if policy == nil || policy.stagingDir == "" {
		return nil, nil, os.ErrInvalid
	}
	if err := policy.validateConfig(s.settings.config); err != nil {
		return nil, nil, err
	}
	parent, err = os.OpenRoot(policy.stagingDir)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			if root != nil {
				root.Close()
			}
			parent.Close()
		}
	}()
	if _, _, err = canonicalPinnedRootIdentity(parent, policy.stagingDir); err != nil {
		return parent, nil, err
	}
	info, err := parent.Lstat(p.StagingID)
	if err != nil {
		return parent, nil, err
	}
	root, err = openClipperFolder(parent, p.StagingID, info)
	if err != nil {
		return parent, nil, err
	}
	dir, err := root.Open(".")
	if err != nil {
		return parent, root, err
	}
	id, e := clipIdentity(dir)
	err = errors.Join(e, dir.Close())
	if err != nil {
		return parent, root, err
	}
	if id != p.StagingIdentity {
		return parent, root, errClipFileChanged
	}
	for _, a := range p.Assets {
		if err = ctx.Err(); err != nil {
			return parent, root, err
		}
		if err = checkClipRecoverySource(root, a); err != nil {
			return parent, root, err
		}
	}
	return parent, root, nil
}

// Preserve NoteService's index publication/fail-closed semantics while keeping
// filesystem scan errors recoverable and repository IO errors fatal. The real
// repository supports context cancellation; existing repository adapters retain
// their ordinary preparation contract.
func (s *ClipImportService) replaceClipRecoveryIndexLocked(ctx context.Context) error {
	n := s.notes
	if n.baseErr != nil {
		return n.baseErr
	}
	nodes, err := n.scan(n.baseRoot)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var commit, rollback func() error
	var operationErr, rollbackErr error
	if repo, ok := n.repo.(interface {
		BeginReplaceAllContext(context.Context, []model.NoteNode) (func() error, func() error, error, error)
	}); ok {
		commit, rollback, operationErr, rollbackErr = repo.BeginReplaceAllContext(ctx, nodes)
	} else {
		commit, rollback, operationErr, rollbackErr = n.repo.BeginReplaceAll(nodes)
	}
	if operationErr != nil {
		if rollbackErr != nil {
			return clipRecoveryDB(n.failClosedLocked(operationErr, rollbackErr))
		}
		return clipRecoveryDB(operationErr)
	}
	if err := ctx.Err(); err != nil {
		return clipRecoveryDB(errors.Join(err, rollback()))
	}
	if err := commit(); err != nil {
		return clipRecoveryDB(n.failClosedLocked(err, commitOutcomeError(rollback())))
	}
	n.publishCompleteIndexLocked()
	return nil
}

func checkClipRecoverySource(root *os.Root, a clipAssetPlan) error {
	f := &clipNoteFile{parent: root, plan: clipNotePlan{TempName: a.Source.Name}, owned: true, progress: clipFileProgress{Identity: a.Source.Identity, SHA256: a.File.SHA256, Size: a.File.Size}}
	return f.checkTemp()
}

func cleanupClipRecoverySources(parent, root *os.Root, p clipImagePlan) {
	dir, err := root.Open(".")
	if err != nil {
		return
	}
	id, e := clipIdentity(dir)
	_ = dir.Close()
	if e != nil || id != p.StagingIdentity {
		return
	}
	// Check the current named directory too; a pinned old handle is not ownership
	// of a replacement at that name. Unknown entries prevent rmdir, never RemoveAll.
	info, err := parent.Lstat(p.StagingID)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	current, err := parent.Open(p.StagingID)
	if err != nil {
		return
	}
	currentID, e := clipIdentity(current)
	opened, statErr := current.Stat()
	_ = current.Close()
	if e != nil || statErr != nil || !samePhysicalFile(info, opened) || currentID != id {
		return
	}
	for _, a := range p.Assets {
		if checkClipRecoverySource(root, a) == nil {
			_ = root.Remove(a.Source.Name)
		}
	}
	_ = syncClipRoot(root)
	_ = parent.Remove(p.StagingID)
	_ = syncClipRoot(parent)
}
