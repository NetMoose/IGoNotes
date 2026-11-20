package service

import (
	"os"

	"golang.org/x/sys/windows"
)

func clipIdentity(file *os.File) (clipFileIdentity, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return clipFileIdentity{}, err
	}
	return clipFileIdentity{Device: uint64(info.VolumeSerialNumber), File: uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)}, nil
}

// Windows does not support fsync on the read-only directory handles supplied by
// os.Root. File contents are flushed before the native no-replace rename.
func syncClipDirectory(*os.File) error { return nil }
