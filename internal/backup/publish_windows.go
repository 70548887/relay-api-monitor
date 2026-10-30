//go:build windows

package backup

import "golang.org/x/sys/windows"

func publishArchive(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	// No REPLACE_EXISTING: a concurrent producer may not overwrite this name.
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}
