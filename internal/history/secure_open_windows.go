//go:build windows

package history

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func secureOpenHistory(globalRoot, name string) (*os.File, error) {
	if err := os.Mkdir(globalRoot, 0o700); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("create global directory: %w", err)
	}

	rootHandle, err := openWindowsPath(globalRoot, windows.GENERIC_READ, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS)
	if err != nil {
		return nil, fmt.Errorf("open global directory: %w", err)
	}
	defer windows.CloseHandle(rootHandle)
	var rootInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(rootHandle, &rootInfo); err != nil {
		return nil, fmt.Errorf("inspect global directory: %w", err)
	}
	if rootInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || rootInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return nil, fmt.Errorf("open global directory: not a directory")
	}

	dir := filepath.Join(globalRoot, "history")
	dirHandle, err := openWindowsRelative(
		rootHandle,
		"history",
		windows.FILE_GENERIC_READ,
		windows.FILE_OPEN_IF,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		windows.FILE_ATTRIBUTE_DIRECTORY,
	)
	if err != nil {
		return nil, fmt.Errorf("create history directory: %w", err)
	}
	defer windows.CloseHandle(dirHandle)
	var dirInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(dirHandle, &dirInfo); err != nil {
		return nil, fmt.Errorf("inspect history directory: %w", err)
	}
	if dirInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || dirInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return nil, fmt.Errorf("open history directory: not a directory")
	}

	path := filepath.Join(dir, name)
	handle, err := openWindowsRelative(
		dirHandle,
		name,
		windows.FILE_GENERIC_READ|windows.FILE_APPEND_DATA,
		windows.FILE_OPEN_IF,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		windows.FILE_ATTRIBUTE_NORMAL,
	)
	if err != nil {
		return nil, fmt.Errorf("open history: %w", err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("inspect history: %w", err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("open history: not a regular file")
	}
	return os.NewFile(uintptr(handle), path), nil
}

func openWindowsRelative(root windows.Handle, name string, access, disposition, options, attributes uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	objectAttributes := windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: root,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(
		&handle,
		access,
		&objectAttributes,
		&status,
		nil,
		attributes,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		disposition,
		options,
		0,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func openWindowsPath(path string, access, disposition, flags uint32) (windows.Handle, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(
		path16,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		disposition,
		flags|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}
