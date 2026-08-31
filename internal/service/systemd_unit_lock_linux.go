//go:build linux

package service

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const systemdUnitLockPollInterval = 10 * time.Millisecond

func acquireSystemdUnitLock(ctx context.Context, path string, onContention func()) (func() error, error) {
	lockFile, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	closeWithError := func(err error) (func() error, error) {
		return nil, errors.Join(err, lockFile.Close())
	}
	ticker := time.NewTicker(systemdUnitLockPollInterval)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return closeWithError(err)
		}
		err = unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return closeWithError(err)
		}
		if onContention != nil {
			onContention()
		}
		select {
		case <-ctx.Done():
			return closeWithError(ctx.Err())
		case <-ticker.C:
		}
	}

	return func() error {
		unlockErr := unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
		return errors.Join(unlockErr, lockFile.Close())
	}, nil
}
