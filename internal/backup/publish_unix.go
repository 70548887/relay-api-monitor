//go:build !windows

package backup

import (
	"os"
	"path/filepath"
)

func publishArchive(source, target string) error {
	if err := os.Link(source, target); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
