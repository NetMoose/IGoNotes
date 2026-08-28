//go:build !windows

package service

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}
