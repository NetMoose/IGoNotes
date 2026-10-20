//go:build !windows

package service

import "os"

func openBaseRoot(path string) (*os.Root, error) { return os.OpenRoot(path) }
