//go:build !windows

// Package fileidentity captures file identities before their paths can change.
package fileidentity

import "os"

func Stat(path string) (os.FileInfo, error) { return os.Stat(path) }
