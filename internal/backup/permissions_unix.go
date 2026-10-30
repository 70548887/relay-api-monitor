//go:build !windows

package backup

import "os"

func protectDirectory(path string) error { return os.Chmod(path, 0700) }
func protectFile(path string) error      { return os.Chmod(path, 0600) }
