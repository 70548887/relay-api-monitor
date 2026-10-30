//go:build windows

package durablejson

import "golang.org/x/sys/windows"

func replace(source, target string) error {
	from, e := windows.UTF16PtrFromString(source)
	if e != nil {
		return e
	}
	to, e := windows.UTF16PtrFromString(target)
	if e != nil {
		return e
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// MoveFileEx WRITE_THROUGH waits for the replacement to reach durable storage.
func syncDirectory(string) error { return nil }
