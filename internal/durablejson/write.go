// Package durablejson commits recovery metadata before protected data is changed.
package durablejson

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func Write(path string, value any) error {
	data, e := json.Marshal(value)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".durable-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e == nil {
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
	if e = replace(tmp, path); e != nil {
		return e
	}
	for directory := filepath.Dir(path); ; directory = filepath.Dir(directory) {
		if e = syncDirectory(directory); e != nil {
			return e
		}
		if filepath.Dir(directory) == directory {
			break
		}
	}
	return nil
}
