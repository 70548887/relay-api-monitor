package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Atomic metadata and executable writes always stage in the destination
// directory. The old name remains intact if the final replace fails.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".launcher-write-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(mode); e == nil {
		e = protectFile(tmp)
	}
	if e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	if closeErr := f.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	if e = atomicRename(tmp, path); e != nil {
		return e
	}
	return syncDirectory(filepath.Dir(path))
}
func copyFile(source, destination string) error {
	in, e := os.Open(source)
	if e != nil {
		return e
	}
	defer in.Close()
	info, e := in.Stat()
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() {
		return errors.New("launcher: source must be a regular file")
	}
	if e = os.MkdirAll(filepath.Dir(destination), 0700); e != nil {
		return e
	}
	out, e := os.CreateTemp(filepath.Dir(destination), ".launcher-copy-*")
	if e != nil {
		return e
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	if e = out.Chmod(0700); e == nil {
		e = protectFile(tmp)
	}
	if e == nil {
		_, e = io.Copy(out, in)
	}
	if e == nil {
		e = out.Sync()
	}
	if closeErr := out.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	if e = atomicRename(tmp, destination); e != nil {
		return e
	}
	return syncDirectory(filepath.Dir(destination))
}
func replaceFile(source, destination string) error { return copyFile(source, destination) }
func SHA256File(path string) (string, int64, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	if e != nil {
		return "", n, e
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
