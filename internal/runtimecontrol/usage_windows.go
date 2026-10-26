//go:build windows

package runtimecontrol

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func lockUsage(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{}); err != nil {
		f.Close()
		return nil, fmt.Errorf("runtimecontrol: database role still in use: %w", err)
	}
	return f, nil
}
func unlockUsage(f *os.File) error {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
	return f.Close()
}
