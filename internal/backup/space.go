package backup

import (
	"fmt"
	"os"
	"path/filepath"
)

// AvailableBytes reports free space on the nearest existing parent filesystem.
func AvailableBytes(path string) (uint64, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	for {
		info, err := os.Stat(abs)
		if err == nil {
			if !info.IsDir() {
				abs = filepath.Dir(abs)
			}
			return availableBytes(abs)
		}
		if !os.IsNotExist(err) {
			return 0, err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return 0, err
		}
		abs = parent
	}
}

func EnsureSpace(path string, required uint64) error {
	free, err := AvailableBytes(path)
	if err != nil {
		return err
	}
	if free < required {
		return fmt.Errorf("备份暂存空间不足：需要至少 %d 字节，可用 %d 字节", required, free)
	}
	return nil
}
