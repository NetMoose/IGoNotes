package service

import (
	"errors"
	"os"
)

func openBaseRoot(path string) (*os.Root, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	// OpenRoot uses a Win32 handle without FILE_SHARE_DELETE. Reopen relative
	// to that pinned handle: Root.OpenRoot uses a share-delete-capable handle,
	// retaining access to the original directory if its pathname is replaced.
	pinned, openErr := root.OpenRoot(".")
	closeErr := root.Close()
	if err := errors.Join(openErr, closeErr); err != nil {
		if pinned != nil {
			err = errors.Join(err, pinned.Close())
		}
		return nil, err
	}
	return pinned, nil
}
