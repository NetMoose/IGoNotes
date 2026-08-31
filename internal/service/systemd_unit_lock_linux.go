//go:build linux

package service

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func acquireSystemdUnitLock(path string) (func() error, error) {
	lockFile, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(lockFile.Fd()), unix.LOCK_EX)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		_ = lockFile.Close()
		return nil, err
	}

	return func() error {
		unlockErr := unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
		return errors.Join(unlockErr, lockFile.Close())
	}, nil
}
