//go:build !windows

package main

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockVerificationFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
func unlockVerificationFile(f *os.File) error {
	e := unix.Flock(int(f.Fd()), unix.LOCK_UN)
	closeErr := f.Close()
	if e != nil {
		return e
	}
	return closeErr
}
