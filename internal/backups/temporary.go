package backups

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

const temporaryRetention = 2 * time.Hour

// cleanupTemporaryFiles is called only while periodicCleanup owns the instance
// operation lock. It never scans user folders, committed archives or journals.
func (m *Manager) cleanupTemporaryFiles(now time.Time) {
	root := filepath.Clean(m.opts.BackupDir)
	entries, e := os.ReadDir(root)
	if e != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".backup-") && strings.HasSuffix(name, ".partial") {
			m.removeExpiredTemporary(root, filepath.Join(root, name), now, false)
		}
	}
	staging := filepath.Join(root, ".staging")
	info, e := os.Lstat(staging)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	entries, e = os.ReadDir(staging)
	if e != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "archive-") || strings.HasPrefix(name, "validate-") {
			m.removeExpiredTemporary(root, filepath.Join(staging, name), now, true)
		} else if strings.HasPrefix(name, "download-") && strings.HasSuffix(name, ".zip.age") || strings.HasPrefix(name, "materialize-") {
			m.removeExpiredTemporary(root, filepath.Join(staging, name), now, false)
		}
	}
}

func (m *Manager) removeExpiredTemporary(root, path string, now time.Time, directory bool) {
	info, e := os.Lstat(path)
	if e != nil || info.Mode()&os.ModeSymlink != 0 || now.Sub(info.ModTime()) < temporaryRetention {
		return
	}
	if directory {
		if !info.IsDir() {
			return
		}
	} else if !info.Mode().IsRegular() {
		return
	}
	resolvedRoot, e := filepath.EvalSymlinks(root)
	if e != nil {
		return
	}
	resolvedPath, e := filepath.EvalSymlinks(path)
	if e != nil {
		return
	}
	rel, e := filepath.Rel(resolvedRoot, resolvedPath)
	if e != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return
	}
	// RemoveAll does not follow child symlinks. The absolute top-level target
	// has been checked to remain inside the dedicated backup directory above.
	if directory {
		_ = os.RemoveAll(path)
	} else {
		_ = os.Remove(path)
	}
}
