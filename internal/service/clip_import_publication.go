package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"time"

	"IGoNotes/internal/model"
	"IGoNotes/internal/repository"
)

// v2 source references are generated components under the reserved private root,
// never client paths. The immutable plan plus these bytes reconstructs all files.
type clipOwnedSource struct {
	Name     string           `json:"name"`
	Identity clipFileIdentity `json:"identity"`
}
type clipAssetPlan struct {
	File   clipNotePlan    `json:"file"`
	Source clipOwnedSource `json:"source"`
	IDs    []string        `json:"ids"`
}
type clipImagePlan struct {
	Version         int              `json:"version"`
	Note            clipNotePlan     `json:"note"`
	Markdown        string           `json:"markdown"`
	Warnings        *[]string        `json:"warnings,omitempty"` // nil: legacy unknown, not empty
	StagingID       string           `json:"staging_id"`
	StagingIdentity clipFileIdentity `json:"staging_identity"`
	Assets          []clipAssetPlan  `json:"assets"`
}
type clipImageCheckpoint struct {
	Version int                `json:"version"`
	Step    string             `json:"step"`
	Note    clipFileProgress   `json:"note"`
	Assets  []clipFileProgress `json:"assets"`
	Indexed bool               `json:"indexed"`
}
type ownedClipSources struct {
	root, parent *os.Root
	plan         clipImagePlan
	retain       bool
}

func syncClipRoot(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(syncClipDirectory(f), f.Close())
}

func (o *ownedClipSources) close() {
	if !o.retain {
		for _, a := range o.plan.Assets {
			_ = o.root.Remove(a.Source.Name)
		}
		_ = syncClipRoot(o.root)
		_ = o.root.Close()
		_ = o.parent.Remove(o.plan.StagingID)
		_ = syncClipRoot(o.parent)
	} else {
		_ = o.root.Close()
	}
	_ = o.parent.Close()
}

// Receipt lookup precedes durable source ownership and destination validation.
// Copying is outside publication locks; the handler may destroy its root on return.
func (s *ClipImportService) ImportStaged(ctx context.Context, staged *StagedClip) (model.ClipperImportStatusResponse, bool, error) {
	zero := model.ClipperImportStatusResponse{}
	if staged == nil {
		return zero, false, clipperManifestError(model.ClipperInvalidRequest)
	}
	digest, err := ClipperManifestDigest(staged.Manifest)
	if err != nil {
		return zero, false, fixedClipperError(err)
	}
	record, found, err := s.store.Get(ctx, staged.Manifest.ImportID)
	if err != nil {
		return zero, false, fixedClipperError(err)
	}
	if found {
		if record.RequestDigest != digest {
			return zero, false, clipperManifestError(model.ClipperRequestChanged)
		}
		return publicClipImport(record), true, nil
	}
	if len(staged.Manifest.Assets) == 0 {
		return s.Import(ctx, staged.Manifest)
	}
	if staged.root == nil || len(staged.Assets) != len(staged.Manifest.Assets) {
		return zero, false, clipperManifestError(model.ClipperInvalidRequest)
	}
	owned, err := s.ownClipSources(ctx, staged)
	if err != nil {
		return zero, false, fixedClipperError(err)
	}
	defer owned.close()
	return s.importClip(ctx, staged.Manifest, owned)
}

func (s *ClipImportService) ownClipSources(ctx context.Context, staged *StagedClip) (_ *ownedClipSources, err error) {
	parent, err := s.settings.OpenPrivateStagingRoot()
	if err != nil {
		return nil, err
	}
	id := "owned-" + rand.Text()
	if err = parent.Mkdir(id, 0700); err != nil {
		parent.Close()
		return nil, err
	}
	root, err := parent.OpenRoot(id)
	if err != nil {
		_ = parent.Remove(id)
		parent.Close()
		return nil, err
	}
	o := &ownedClipSources{parent: parent, root: root, plan: clipImagePlan{Version: 2, StagingID: id}}
	defer func() {
		if err != nil {
			o.close()
		}
	}()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	o.plan.StagingIdentity, err = clipIdentity(dir)
	err = errors.Join(err, dir.Close())
	if err != nil {
		return nil, err
	}
	dedup := map[string]int{}
	for _, declared := range staged.Manifest.Assets {
		a, ok := staged.Assets[declared.ID]
		if !ok || a.Descriptor != declared || a.Extension != clipImageExtension(declared.MediaType) {
			return nil, os.ErrInvalid
		}
		key := declared.SHA256 + "." + a.Extension
		if i, ok := dedup[key]; ok {
			o.plan.Assets[i].IDs = append(o.plan.Assets[i].IDs, declared.ID)
			continue
		}
		source := clipOwnedSource{Name: "source-" + rand.Text()}
		plan := clipAssetPlan{File: clipNotePlan{Parent: "assets/images", Filename: "clip-" + staged.Manifest.ImportID + "-" + key, TempName: "igonotes-clip-" + rand.Text() + ".tmp", SHA256: declared.SHA256, Size: declared.Size}, Source: source, IDs: []string{declared.ID}}
		input, e := staged.OpenAsset(declared.ID)
		if e != nil {
			return nil, e
		}
		out, e := root.OpenFile(source.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			input.Close()
			return nil, e
		}
		// Register ownership immediately so every copy/cancellation failure cleans up.
		dedup[key] = len(o.plan.Assets)
		o.plan.Assets = append(o.plan.Assets, plan)
		identity, idErr := clipIdentity(out)
		o.plan.Assets[len(o.plan.Assets)-1].Source.Identity = identity
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(out, hash), io.LimitReader(clipContextReader{ctx, input}, declared.Size+1))
		syncErr := out.Sync()
		e = errors.Join(idErr, copyErr, syncErr, out.Close(), input.Close())
		if e != nil {
			return nil, e
		}
		if n != declared.Size || hex.EncodeToString(hash.Sum(nil)) != declared.SHA256 {
			return nil, errClipFileChanged
		}
	}
	sort.Slice(o.plan.Assets, func(i, j int) bool { return o.plan.Assets[i].File.Filename < o.plan.Assets[j].File.Filename })
	for i := range o.plan.Assets {
		sort.Strings(o.plan.Assets[i].IDs)
	}
	if err = errors.Join(ctx.Err(), syncClipRoot(root), syncClipRoot(parent)); err != nil {
		return nil, err
	}
	return o, nil
}

// Existing components are pinned and validated exactly like note folders, but
// assets is intentionally allowed here. Never follow aliases into .git.
func clipAssetDirectory(root *os.Root, create bool) (*os.Root, error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"assets", "images"} {
		info, e := current.Lstat(name)
		if errors.Is(e, os.ErrNotExist) && create {
			if e = current.Mkdir(name, 0755); e == nil {
				e = syncClipRoot(current)
			}
			if e == nil {
				info, e = current.Lstat(name)
			}
		}
		if e != nil {
			current.Close()
			return nil, e
		}
		next, e := openClipperFolder(current, name, info)
		closeErr := current.Close()
		if e = errors.Join(e, closeErr); e != nil {
			if next != nil {
				next.Close()
			}
			return nil, e
		}
		current = next
	}
	return current, nil
}

func prepareClipBinary(root *os.Root, plan clipNotePlan, source func() (io.ReadCloser, error)) (*clipNoteFile, error) {
	// Reuse confinement/handle setup; note validation remains in prepareClipNote.
	local := plan
	local.Parent = "" // root is already pinned at the planned assets/images parent
	f, err := prepareClipFile(root, local)
	if err != nil {
		return nil, err
	}
	f.plan = plan
	f.source = source
	return f, nil
}

func (s *ClipImportService) publishClipImagesLocked(ctx context.Context, m model.ClipperManifest, digest string, snapshot ClipperDestinationSnapshot, root *os.Root, identity clipFileIdentity, note *clipNoteFile, o *ownedClipSources) (model.ClipperImportStatusResponse, bool, error) {
	zero := model.ClipperImportStatusResponse{}
	// Validate existing directories/collisions before claim, without creating them.
	assetRoot, err := clipAssetDirectory(root, false)
	if err == nil {
		for _, a := range o.plan.Assets {
			_, e := assetRoot.Lstat(a.File.Filename)
			if e == nil {
				assetRoot.Close()
				return zero, false, clipperManifestError(model.ClipperNoteConflict)
			}
			if !errors.Is(e, os.ErrNotExist) {
				assetRoot.Close()
				return zero, false, e
			}
		}
		assetRoot.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return zero, false, err
	}
	o.plan.Note = note.plan
	o.plan.Markdown = m.Markdown
	o.plan.Warnings = clipImportPlanWarnings(m)
	encoded, _ := json.Marshal(o.plan)
	idJSON, _ := json.Marshal(identity)
	cp := clipImageCheckpoint{Version: 2, Step: "creating", Assets: make([]clipFileProgress, len(o.plan.Assets))}
	checkpoint, _ := json.Marshal(cp)
	now := time.Now().UTC()
	// Unknown Claim acknowledgement can already reference these bytes. Retain
	// until a definitive lookup proves a different owner or terminal receipt.
	o.retain = true
	record, created, err := s.store.Claim(ctx, repository.ClipImportRecord{ImportID: m.ImportID, RequestDigest: digest, BaseName: snapshot.Name, BasePath: snapshot.CanonicalPath, RootIdentity: string(idJSON), DestinationRevision: m.DestinationRevision, NotePath: path.Join(m.Parent, m.Filename), NoteContentHash: note.plan.SHA256, Journal: repository.ClipImportJournal{Plan: encoded, Checkpoint: checkpoint}, State: repository.ClipImportPrepared, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		existing, found, e := s.store.Get(lookupCtx, m.ImportID)
		if e == nil {
			if !found {
				o.retain = false
			} else {
				var p clipImagePlan
				if json.Unmarshal(existing.Journal.Plan, &p) == nil && p.Version == 2 && clipperSafeComponent(p.StagingID) && p.StagingID != o.plan.StagingID {
					o.retain = false
				}
			}
		}
		var conflict *model.ClipperError
		if errors.As(err, &conflict) && conflict.Code == model.ClipperRequestChanged {
			return zero, false, clipperManifestError(model.ClipperRequestChanged)
		}
		return zero, false, clipperManifestError(model.ClipperRecoveryRequired)
	}
	if !created {
		o.retain = false
		return publicClipImport(record), true, nil
	}
	files := []*clipNoteFile{}
	defer func() {
		for _, f := range files {
			_ = f.close()
		}
	}()
	progress := func(step string) error {
		cp.Step = step
		cp.Note = note.progress
		for i, f := range files {
			cp.Assets[i] = f.progress
		}
		raw, _ := json.Marshal(cp)
		if e := s.store.Checkpoint(ctx, record.ImportID, record.Version(), raw); e != nil {
			return e
		}
		return s.reloadImport(ctx, &record)
	}
	fail := func() (model.ClipperImportStatusResponse, bool, error) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// Once note publication was attempted, preserve all recovery evidence.
		safe := !note.progress.Published && !note.progress.PublicationUncertain
		for _, f := range files {
			if f.progress.PublicationUncertain {
				safe = false
			}
		}
		if safe {
			if note.discardTemp() != nil {
				safe = false
			}
			for i := len(files) - 1; i >= 0; i-- {
				if files[i].rollbackOwned() != nil {
					safe = false
				}
			}
		}
		if s.reloadImport(cleanupCtx, &record) != nil {
			return publicClipImport(record), false, nil
		}
		if record.State == repository.ClipImportSucceeded {
			o.retain = false
			return publicClipImport(record), false, nil
		}
		cp.Step = "recovery"
		next := repository.ClipImportRecoveryRequired
		public := &model.APIError{Code: string(model.ClipperRecoveryRequired), Message: "Clipper import requires recovery"}
		if safe {
			cp.Step = "cleaned"
			next = repository.ClipImportRejected
			public = &model.APIError{Code: "internal_error", Message: "Internal server error"}
		}
		cp.Note = note.progress
		for i, f := range files {
			cp.Assets[i] = f.progress
		}
		raw, _ := json.Marshal(cp)
		if s.store.Checkpoint(cleanupCtx, record.ImportID, record.Version(), raw) == nil && s.reloadImport(cleanupCtx, &record) == nil {
			if s.store.Transition(cleanupCtx, record.ImportID, record.Version(), next, nil, public) == nil {
				record.State = next
				record.Error = public
				if safe {
					o.retain = false
				}
			}
		}
		return publicClipImport(record), false, nil
	}
	assetRoot, err = clipAssetDirectory(root, true)
	if err != nil {
		return fail()
	}
	defer assetRoot.Close()
	for _, a := range o.plan.Assets {
		a := a
		source := func() (io.ReadCloser, error) {
			f, e := o.root.Open(a.Source.Name)
			if e != nil {
				return nil, e
			}
			id, e := clipIdentity(f)
			if e != nil || id != a.Source.Identity {
				f.Close()
				return nil, errClipFileChanged
			}
			return &clipSourceReader{Reader: clipContextReader{ctx, f}, Closer: f}, nil
		}
		f, e := s.prepareBinary(assetRoot, a.File, source)
		if e != nil {
			return fail()
		}
		files = append(files, f)
	}
	// Persist every create/write boundary; all assets precede the note.
	all := append(append([]*clipNoteFile{}, files...), note)
	for _, f := range all {
		if _, err = f.create(); err != nil {
			return fail()
		}
		if err = progress("writing"); err != nil {
			return fail()
		}
		if _, err = f.write(); err != nil {
			return fail()
		}
		if err = progress("written"); err != nil {
			return fail()
		}
	}
	if err = s.store.Transition(ctx, record.ImportID, record.Version(), repository.ClipImportPublishing, nil, nil); err != nil {
		return fail()
	}
	if err = s.reloadImport(ctx, &record); err != nil {
		return fail()
	}
	for _, f := range all {
		if _, err = f.publish(); err != nil {
			return fail()
		}
		if err = progress("published"); err != nil {
			return fail()
		}
	}
	if snapshot.Active {
		if err = s.notes.replaceIndexLocked(); err != nil {
			return fail()
		}
		cp.Indexed = true
	}
	if err = progress("receipt"); err != nil {
		return fail()
	}
	warnings, err := clipImportReceiptWarnings(record.Journal.Plan)
	if err != nil {
		return fail()
	}
	result := &model.ClipperImportResult{ImportID: m.ImportID, Base: record.BaseName, Path: record.NotePath, Revision: noteRevision([]byte(m.Markdown)), Warnings: warnings}
	if err = s.store.Transition(ctx, record.ImportID, record.Version(), repository.ClipImportSucceeded, result, nil); err != nil {
		return fail()
	}
	record.State = repository.ClipImportSucceeded
	record.Result = result
	o.retain = false
	return publicClipImport(record), false, nil
}

type clipSourceReader struct {
	io.Reader
	io.Closer
}

// Only the original publication critical section may call this. A known
// published asset is removed only after native identity AND content checks.
func (f *clipNoteFile) rollbackOwned() error {
	if f.progress.PublicationUncertain {
		return errClipPublicationUncertain
	}
	if !f.progress.Published {
		return f.discardTemp()
	}
	temp := f.plan.TempName
	f.plan.TempName = f.plan.Filename
	err := f.checkTemp()
	f.plan.TempName = temp
	if err != nil {
		return err
	}
	if err = f.parent.Remove(f.plan.Filename); err != nil {
		return err
	}
	f.owned = false
	f.progress.Published = false
	f.progress.Durable = false
	return f.ops.syncParent(f.dir)
}
