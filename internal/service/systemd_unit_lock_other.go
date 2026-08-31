//go:build !linux

package service

import (
	"context"
	"fmt"
	"runtime"
)

func acquireSystemdUnitLock(context.Context, string, func()) (func() error, error) {
	return nil, fmt.Errorf("systemd user unit locking is unavailable on %s", runtime.GOOS)
}
