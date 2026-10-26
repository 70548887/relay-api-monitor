//go:build !windows

package security

import (
	"io"
	"os"
)

func protectDataDir(path string) error { return os.Chmod(path, 0700) }

func protectSecretFile(path string) error             { return os.Chmod(path, 0600) }
func publishKeyFile(source, destination string) error { return os.Link(source, destination) }
func readSecretFile(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 33))
}
