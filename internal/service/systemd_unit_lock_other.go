//go:build !linux

package service

import (
	"fmt"
	"runtime"
)

func acquireSystemdUnitLock(string) (func() error, error) {
	return nil, fmt.Errorf("systemd user unit locking is unavailable on %s", runtime.GOOS)
}
