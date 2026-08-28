//go:build darwin

package service

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameNoReplace(parent *os.File, source, destination string) error {
	fd := int(parent.Fd())
	return unix.RenameatxNp(fd, source, fd, destination, unix.RENAME_EXCL)
}
