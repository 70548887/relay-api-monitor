// Package operationlock coordinates backup, restore and installation across processes.
package operationlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrBusy = errors.New("备份、恢复或升级操作正在进行")

// Path stays outside the database directory so restoring that directory cannot
// replace a lock inode that is still held by the launcher.
func Path(dataDir string) string {
	return filepath.Join(BackupRoot(dataDir), ".operation.lock")
}

// Acquire never waits. The OS releases the lock if the owning process exits.
func Acquire(dataDir string) (func() error, error) {
	if _, err := CanonicalDataDir(dataDir); err != nil {
		return nil, err
	}
	p := Path(dataDir)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(p); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("操作锁必须为普通文件")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %v", ErrBusy, err)
	}
	var once sync.Once
	var closeErr error
	return func() error { once.Do(func() { closeErr = unlockFile(f) }); return closeErr }, nil
}
