//go:build !windows

package durablejson

import "os"

func replace(source, target string) error { return os.Rename(source, target) }
func syncDirectory(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
