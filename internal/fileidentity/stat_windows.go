package fileidentity

import (
	"errors"
	"os"
)

// Stat captures the file ID eagerly. On Windows os.Stat can defer looking up
// that ID until os.SameFile is called, by which time the path may be replaced.
func Stat(path string) (os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	return info, errors.Join(err, file.Close())
}
