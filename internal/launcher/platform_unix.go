//go:build !windows

package launcher

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

func acquireLock(path string) (*os.File, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("launcher: data directory is already owned: %w", e)
	}
	return f, nil
}
func releaseLock(f *os.File) error           { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); return f.Close() }
func atomicRename(source, dest string) error { return os.Rename(source, dest) }
func syncDirectory(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func processAlive(pid int) bool          { e := unix.Kill(pid, 0); return e == nil || e == unix.EPERM }
func protectFile(path string) error      { return nil } // caller sets owner-only mode before publication
func protectDirectory(path string) error { return os.Chmod(path, 0700) }
