package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"IGoNotes/internal/model"
)

var (
	errClipFileChanged          = errors.New("clip import file ownership changed")
	errClipPublicationUncertain = errors.New("clip import publication requires recovery")
)

// These private primitives require the caller to hold coordinator -> settings.mu
// -> notes.baseMu throughout preparation, publication and any immediate cleanup.
// They neither acquire locks nor perform SQLite/index operations. Errors are
// private diagnostics (possibly PathErrors); an API mapper must not expose them.
// An external filesystem writer does not participate in these application locks.
// Identity/hash checks detect observed interference; this is not a filesystem
// transaction or a conditional-unlink primitive against hostile external writers.
type clipNoteFile struct {
	plan     clipNotePlan
	markdown string
	parent   *os.Root
	dir      *os.File
	file     *os.File
	owned    bool
	ready    bool
	progress clipFileProgress
	ops      clipFileOps
}

// Plan is available before the first write, including the server-generated
// ownership name. Persist it in PREPARED before create; never accept a temp name
// supplied by a request. Parent and Filename have portable manifest semantics.
type clipNotePlan struct {
	Parent   string `json:"parent"`
	Filename string `json:"filename"`
	TempName string `json:"temp_name"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

// Native identity is serializable for later recovery; hash alone does not prove
// ownership of a pathname. Progress must be journaled between filesystem steps.
// Published remains true on a directory-sync failure. A rename error instead
// sets PublicationUncertain: Published=false then means unknown, NOT known
// unpublished. Journal that uncertainty for recovery; never record rejection,
// retry or clean up based solely on an error. With neither flag set no rename
// has been attempted. Durable means all supported syncs completed, not an atomic
// commit with SQLite.
type clipFileIdentity struct {
	Device uint64 `json:"device"`
	File   uint64 `json:"file"`
}

type clipFileProgress struct {
	Identity             clipFileIdentity `json:"identity"`
	SHA256               string           `json:"sha256"`
	Size                 int64            `json:"size"`
	Published            bool             `json:"published"`
	PublicationUncertain bool             `json:"publication_uncertain"`
	Durable              bool             `json:"durable"`
}

// Hooks belong to an individual operation, never package globals. A rename hook
// must use no-replace semantics, but an error does not establish that the entry
// was not renamed (for example, a remote rename may lose its acknowledgement).
type clipFileOps struct {
	write      func(*os.File, []byte) (int, error)
	sync       func(*os.File) error
	close      func(*os.File) error
	rename     func(*os.File, string, string) error
	syncParent func(*os.File) error
}

func prepareClipNote(root *os.Root, parent, filename, markdown string) (*clipNoteFile, error) {
	if root == nil || !utf8.ValidString(parent) || !utf8.ValidString(filename) ||
		!clipperSafeComponent(filename) || !strings.HasSuffix(filename, ".md") || filename == ".md" ||
		!utf8.ValidString(markdown) || strings.TrimSpace(markdown) == "" || len(markdown) > model.ClipperMaxMarkdownBytes {
		return nil, os.ErrInvalid
	}
	if parent != "" {
		for _, component := range strings.Split(parent, "/") {
			if !clipperSafeComponent(component) {
				return nil, os.ErrInvalid
			}
		}
	}
	// Pin each child separately, using the enumeration identity/type checks. This
	// rejects even in-root symlinks into .git, not just escapes from the base root.
	pinned, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if parent != "" {
		for _, component := range strings.Split(parent, "/") {
			info, err := pinned.Lstat(component)
			if err != nil {
				return nil, errors.Join(err, pinned.Close())
			}
			child, openErr := openClipperFolder(pinned, component, info)
			closeErr := pinned.Close()
			if err := errors.Join(openErr, closeErr); err != nil {
				if child != nil {
					err = errors.Join(err, child.Close())
				}
				return nil, err
			}
			pinned = child
		}
	}
	dir, err := pinned.Open(".")
	if err != nil {
		return nil, errors.Join(err, pinned.Close())
	}
	f := &clipNoteFile{
		plan:     clipNotePlan{Parent: parent, Filename: filename, TempName: "igonotes-clip-" + rand.Text() + ".tmp", SHA256: clipContentHash([]byte(markdown)), Size: int64(len(markdown))},
		markdown: markdown, parent: pinned, dir: dir,
		ops: clipFileOps{
			write: (*os.File).Write, sync: (*os.File).Sync, close: (*os.File).Close,
			rename: renameNoReplace, syncParent: syncClipDirectory,
		},
	}
	return f, nil
}

func clipContentHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// create uses O_EXCL once: a collision is never adopted, removed or silently
// retried with an unjournaled name. Persist returned identity before write.
func (f *clipNoteFile) create() (clipFileProgress, error) {
	if f.owned || f.parent == nil {
		return f.progress, os.ErrInvalid
	}
	file, err := f.parent.OpenFile(f.plan.TempName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return f.progress, err
	}
	f.file, f.owned = file, true
	id, err := clipIdentity(file)
	if err != nil {
		return f.progress, err
	} // retain unknown ownership; never guess/delete
	f.progress = clipFileProgress{Identity: id, SHA256: clipContentHash(nil)}
	return f.progress, nil
}

// write completes the exact Markdown, syncs it and closes before publication.
// On failure progress describes only bytes successfully written by this helper.
// Caller may discardTemp under the original locks, or preserve it for recovery.
func (f *clipNoteFile) write() (clipFileProgress, error) {
	if f.file == nil || f.ready || f.progress.Identity == (clipFileIdentity{}) {
		return f.progress, os.ErrInvalid
	}
	file := f.file
	data := []byte(f.markdown)
	var writeErr error
	written := 0
	for written < len(data) {
		n, err := f.ops.write(file, data[written:])
		if n < 0 || n > len(data)-written {
			writeErr = io.ErrShortWrite
			break
		}
		written += n
		if err != nil {
			writeErr = err
			break
		}
		if n == 0 {
			writeErr = io.ErrShortWrite
			break
		}
	}
	f.progress.Size = int64(written)
	f.progress.SHA256 = clipContentHash(data[:written])
	if writeErr == nil {
		writeErr = f.ops.sync(file)
	}
	closeErr := f.ops.close(file)
	// A hook may report failure without closing. Ensure the real handle cannot
	// leak, without turning the normal second-close error into an operation error.
	_ = file.Close()
	f.file = nil
	err := errors.Join(writeErr, closeErr)
	f.ready = err == nil
	return f.progress, err
}

func (f *clipNoteFile) publish() (clipFileProgress, error) {
	if f.progress.PublicationUncertain {
		return f.progress, errClipPublicationUncertain
	}
	if !f.ready || f.progress.Published || f.parent == nil {
		return f.progress, os.ErrInvalid
	}
	if err := f.checkTemp(); err != nil {
		return f.progress, err
	}
	if err := f.ops.rename(f.dir, f.plan.TempName, f.plan.Filename); err != nil {
		// Do not infer non-publication from errno, even EEXIST: this primitive
		// does no reconciliation. Preserve the original error for diagnostics,
		// but forbid retry/cleanup until journal recovery establishes the truth.
		f.progress.PublicationUncertain = true
		f.ready = false
		return f.progress, err
	}
	f.progress.Published = true // checkpoint truth even if the next durability step fails
	if err := f.ops.syncParent(f.dir); err != nil {
		return f.progress, err
	}
	f.progress.Durable = true
	return f.progress, nil
}

// checkTemp refuses symlinks, replaced entries and modified contents, including
// partial writes. Read through an opened handle and check the directory entry
// again afterwards; never infer ownership just from a random name or hash.
func (f *clipNoteFile) checkTemp() error {
	if !f.owned || f.progress.Identity == (clipFileIdentity{}) {
		return errClipFileChanged
	}
	info, err := f.parent.Lstat(f.plan.TempName)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errClipFileChanged
	}
	file, err := f.parent.Open(f.plan.TempName)
	if err != nil {
		return err
	}
	defer file.Close()
	id, err := clipIdentity(file)
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if id != f.progress.Identity || !samePhysicalFile(info, opened) || opened.Size() != f.progress.Size {
		return errClipFileChanged
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, f.progress.Size+1))
	if err != nil {
		return err
	}
	current, err := f.parent.Lstat(f.plan.TempName)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !samePhysicalFile(current, opened) || n != f.progress.Size || hex.EncodeToString(hash.Sum(nil)) != f.progress.SHA256 {
		return errClipFileChanged
	}
	return nil
}

// discardTemp never rolls back a published note or inspects/removes entries after
// an uncertain rename. Later recovery/rollback must make its own identity/hash
// decision from the journal, not reuse a late cleanup closure after releasing the
// original critical section. Interference leaves the entry intact and returns an
// error for recovery_required.
func (f *clipNoteFile) discardTemp() error {
	if f.progress.PublicationUncertain {
		return errClipPublicationUncertain
	}
	if !f.owned || f.progress.Published {
		return nil
	}
	if f.parent == nil {
		return os.ErrInvalid
	}
	if f.file != nil { // an empty create may need cleanup after a journal error
		if err := f.file.Close(); err != nil {
			return err
		}
		f.file = nil
	}
	if err := f.checkTemp(); err != nil {
		return err
	}
	if err := f.parent.Remove(f.plan.TempName); err != nil {
		return err
	}
	f.owned, f.ready = false, false
	return f.ops.syncParent(f.dir)
}

// close releases handles only; it never implicitly deletes an entry. This keeps
// defer safe when the caller must preserve evidence for journal recovery.
func (f *clipNoteFile) close() error {
	var err error
	if f.file != nil {
		err = f.file.Close()
		f.file = nil
	}
	if f.dir != nil {
		err = errors.Join(err, f.dir.Close())
		f.dir = nil
	}
	if f.parent != nil {
		err = errors.Join(err, f.parent.Close())
		f.parent = nil
	}
	return err
}
