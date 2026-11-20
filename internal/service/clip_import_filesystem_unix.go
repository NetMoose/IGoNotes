//go:build linux || darwin

package service

import (
	"errors"
	"os"
	"syscall"
)

func clipIdentity(file *os.File) (clipFileIdentity, error) {
	info, err := file.Stat()
	if err != nil {
		return clipFileIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return clipFileIdentity{}, os.ErrInvalid
	}
	return clipFileIdentity{Device: uint64(stat.Dev), File: uint64(stat.Ino)}, nil
}

func syncClipDirectory(dir *os.File) error {
	err := dir.Sync()
	// Some filesystems do not support directory fsync. Other failures (notably
	// I/O errors) retain published-but-not-durable progress for the journal.
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
		return nil
	}
	return err
}
