package service

import (
	"errors"
	"fmt"
	"path/filepath"
)

func withSystemdUnitLock(unitPath string, action func() error) error {
	lockPath := filepath.Join(filepath.Dir(unitPath), "."+filepath.Base(unitPath)+".lock")
	release, err := acquireSystemdUnitLock(lockPath)
	if err != nil {
		return fmt.Errorf("lock systemd user unit %q: %w", unitPath, err)
	}

	actionErr := action()
	releaseErr := release()
	if actionErr != nil {
		if releaseErr != nil {
			return errors.Join(actionErr, fmt.Errorf("release systemd user unit lock %q: %w", lockPath, releaseErr))
		}
		return actionErr
	}
	if releaseErr != nil {
		return fmt.Errorf("release systemd user unit lock %q: %w", lockPath, releaseErr)
	}
	return nil
}
