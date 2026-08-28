//go:build windows

package service

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func renameNoReplace(parent *os.File, source, destination string) error {
	parentHandle := windows.Handle(parent.Fd())
	sourceName, err := windows.NewNTUnicodeString(source)
	if err != nil {
		return err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: parentHandle,
		ObjectName:    sourceName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}

	var sourceHandle windows.Handle
	err = windows.NtCreateFile(
		&sourceHandle,
		windows.SYNCHRONIZE|windows.DELETE,
		attributes,
		&windows.IO_STATUS_BLOCK{},
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return convertRenameNTStatus(err)
	}
	defer windows.CloseHandle(sourceHandle)

	destinationName, err := windows.UTF16FromString(destination)
	if err != nil {
		return err
	}
	destinationName = destinationName[:len(destinationName)-1]
	var header fileRenameInformation
	nameOffset := int(unsafe.Offsetof(header.FileName))
	buffer := make([]byte, nameOffset+len(destinationName)*2)
	info := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.RootDirectory = parentHandle
	info.FileNameLength = uint32(len(destinationName) * 2)
	copy(unsafe.Slice(&info.FileName[0], len(destinationName)), destinationName)

	err = windows.NtSetInformationFile(
		sourceHandle,
		&windows.IO_STATUS_BLOCK{},
		&buffer[0],
		uint32(len(buffer)),
		windows.FileRenameInformation,
	)
	return convertRenameNTStatus(err)
}

func convertRenameNTStatus(err error) error {
	status, ok := err.(windows.NTStatus)
	if !ok {
		return err
	}
	if status == windows.STATUS_OBJECT_NAME_COLLISION || status == windows.STATUS_OBJECT_NAME_EXISTS {
		return syscall.EEXIST
	}
	errno := status.Errno()
	if errno == windows.ERROR_FILE_EXISTS || errno == windows.ERROR_ALREADY_EXISTS {
		return syscall.EEXIST
	}
	return errno
}
