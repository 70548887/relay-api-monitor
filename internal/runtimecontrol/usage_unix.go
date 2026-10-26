//go:build !windows

package runtimecontrol

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lockUsage(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("runtimecontrol: database role still in use: %w", err)
	}
	return f, nil
}
func unlockUsage(f *os.File) error {
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return f.Close()
}
