package service

import (
	"errors"
	"os"
	"sort"

	"IGoNotes/internal/model"
)

// Folders captures the binding, revision and folder list using the same scoped
// root under coordinator -> settings.mu -> notes.baseMu. No runtime switch,
// content read or index operation is involved. The response is all-or-nothing.
func (s *ClipperDestinationService) Folders(name string) (model.ClipperFoldersResponse, error) {
	s.settings.lockMutation()
	defer s.settings.unlockMutation()
	var response model.ClipperFoldersResponse
	err := s.settings.withClipperDestinationSnapshotLocked(name, func(snapshot ClipperDestinationSnapshot, root *os.Root) error {
		folders, err := clipperFolders(root)
		if err != nil {
			return err
		}
		record, err := s.issueSnapshotLocked(snapshot)
		if err != nil {
			return err
		}
		response = model.ClipperFoldersResponse{Base: snapshot.Name, Folders: folders, DestinationRevision: record.revision}
		return nil
	})
	if err != nil {
		// Do not expose PathError paths, even to a caller logging the service error.
		var clipErr *model.ClipperError
		if errors.As(err, &clipErr) && clipErr.Code == model.ClipperDestinationNotFound {
			return model.ClipperFoldersResponse{}, &model.ClipperError{Code: model.ClipperDestinationNotFound}
		}
		return model.ClipperFoldersResponse{}, &model.ClipperError{Code: model.ClipperDestinationChanged}
	}
	return response, nil
}

func clipperFolders(root *os.Root) ([]string, error) {
	folders := []string{""}
	var walk func(*os.Root, string) error
	walk = func(parent *os.Root, prefix string) error {
		file, err := parent.Open(".")
		if err != nil {
			return err
		}
		entries, readErr := file.ReadDir(-1)
		if err := errors.Join(readErr, file.Close()); err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !noteDirectoryVisible(entry.Name()) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			child, err := openClipperFolder(parent, entry.Name(), info)
			if err != nil {
				return err
			}
			folder := entry.Name()
			if prefix != "" {
				folder = prefix + "/" + folder
			}
			folders = append(folders, folder)
			// Traverse this handle, not a root-relative path whose ancestors could
			// now be replaced by symlinks (including aliases into .git).
			walkErr := walk(child, folder)
			if err := errors.Join(walkErr, child.Close()); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	sort.Strings(folders)
	return folders, nil
}

// OpenRoot confines even a concurrent escape symlink. The identity/type checks
// additionally reject replacement by an in-root symlink, notably a .git alias,
// before any directory entries are read. Stat the opened handle for Windows IDs.
func openClipperFolder(parent *os.Root, name string, expected os.FileInfo) (*os.Root, error) {
	if !expected.IsDir() || expected.Mode()&os.ModeSymlink != 0 {
		return nil, clipperDestinationChanged(nil)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	file, err := child.Open(".")
	if err != nil {
		return nil, errors.Join(err, child.Close())
	}
	opened, statErr := file.Stat()
	current, linkErr := parent.Lstat(name)
	err = errors.Join(statErr, linkErr, file.Close())
	if err == nil && (!current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !samePhysicalFile(expected, opened) || !samePhysicalFile(current, opened)) {
		err = clipperDestinationChanged(nil)
	}
	if err != nil {
		return nil, errors.Join(err, child.Close())
	}
	return child, nil
}
