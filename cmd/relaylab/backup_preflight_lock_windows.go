//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"os"
)

func lockVerificationFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}
func unlockVerificationFile(f *os.File) error {
	e := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
	closeErr := f.Close()
	if e != nil {
		return e
	}
	return closeErr
}
