package operationlock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// CanonicalDataDir resolves aliases even before a new data directory exists.
// Windows paths are case folded so spelling variations share one namespace.
func CanonicalDataDir(dataDir string) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", errors.New("数据目录不能为空")
	}
	abs, e := filepath.Abs(dataDir)
	if e != nil {
		return "", e
	}
	abs = filepath.Clean(abs)
	path := abs
	parts := []string{}
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(parts) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, parts[i])
			}
			resolved = filepath.Clean(resolved)
			if runtime.GOOS == "windows" {
				resolved = strings.ToLower(resolved)
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		parts = append(parts, filepath.Base(path))
		path = parent
	}
}
func InstanceID(dataDir string) string {
	canonical, e := CanonicalDataDir(dataDir)
	if e != nil {
		canonical, _ = filepath.Abs(dataDir)
		canonical = filepath.Clean(canonical)
		if runtime.GOOS == "windows" {
			canonical = strings.ToLower(canonical)
		}
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}
func BackupRoot(dataDir string) string {
	canonical, e := CanonicalDataDir(dataDir)
	if e != nil {
		canonical, _ = filepath.Abs(dataDir)
	}
	return filepath.Join(filepath.Dir(canonical), "backups", "instances", InstanceID(dataDir))
}
