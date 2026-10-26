package runtimecontrol

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// AcquireUsage owns a role's database usage for the complete process lifetime,
// including initialization and cleanup. Callers must release after closing DB.
func AcquireUsage(dataDir, role string) (func() error, error) {
	if role != "serve" && role != "worker" {
		return nil, errors.New("runtimecontrol: invalid usage role")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	f, err := lockUsage(filepath.Join(dataDir, "runtime-"+role+".lock"))
	if err != nil {
		return nil, err
	}
	var once sync.Once
	var releaseErr error
	return func() error { once.Do(func() { releaseErr = unlockUsage(f) }); return releaseErr }, nil
}
