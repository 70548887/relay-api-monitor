package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	backupcore "relaylab/internal/backup"
	"relaylab/internal/domain"
	"relaylab/internal/durablejson"
	"relaylab/internal/operationlock"
)

const verificationTempTTL = 2 * time.Hour
const verificationOwnerFile = ".verify-owner.json"

var verificationTempName = regexp.MustCompile(`^verify-[a-f0-9]{32}$`)

type verificationTempOwner struct {
	Version   int       `json:"version"`
	Root      string    `json:"root"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Each verifier owns this cache root until all plaintext files are removed.
// The OS releases the lock after an abnormal exit, allowing the next invocation
// to collect expired managed directories without racing a live verifier.
func lockVerificationStaging(root string) (func() error, error) {
	path := filepath.Join(root, ".verify.lock")
	if info, e := os.Lstat(path); e == nil && !info.Mode().IsRegular() {
		return nil, errors.New("预检锁必须为普通文件")
	} else if e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("预检锁路径无效")
	}
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		f.Close()
		return nil, errors.New("预检锁在打开后改变")
	}
	if e = backupcore.ProtectFile(path); e != nil {
		f.Close()
		return nil, e
	}
	if e = lockVerificationFile(f); e != nil {
		f.Close()
		return nil, errors.New("另一个备份预检正在使用暂存目录，或无法取得独占锁")
	}
	var once sync.Once
	var closeErr error
	return func() error { once.Do(func() { closeErr = unlockVerificationFile(f) }); return closeErr }, nil
}

func newVerificationStaging(root string, now time.Time) (string, error) {
	canonical, e := operationlock.CanonicalDataDir(root)
	if e != nil {
		return "", e
	}
	name := "verify-" + domain.NewID()
	dir := filepath.Join(root, name)
	if e = os.Mkdir(dir, 0700); e != nil {
		return "", e
	}
	ready := false
	defer func() {
		if !ready {
			_ = os.RemoveAll(dir)
		}
	}()
	if e = backupcore.ProtectDirectory(dir); e != nil {
		return "", e
	}
	if e = durablejson.Write(filepath.Join(dir, verificationOwnerFile), verificationTempOwner{Version: 1, Root: canonical, Name: name, CreatedAt: now.UTC()}); e != nil {
		return "", e
	}
	ready = true
	return dir, nil
}

func cleanupVerificationStaging(root string, now time.Time) error {
	canonical, e := operationlock.CanonicalDataDir(root)
	if e != nil {
		return e
	}
	entries, e := os.ReadDir(root)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if !verificationTempName.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		info, e := os.Lstat(dir)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		resolved, e := operationlock.CanonicalDataDir(dir)
		if e != nil {
			continue
		}
		if resolved != filepath.Join(canonical, entry.Name()) {
			continue
		}
		marker := filepath.Join(dir, verificationOwnerFile)
		meta, e := os.Lstat(marker)
		if e != nil || !meta.Mode().IsRegular() || meta.Size() <= 0 || meta.Size() > 4096 {
			continue
		}
		f, e := os.Open(marker)
		if e != nil {
			continue
		}
		var owner verificationTempOwner
		e = json.NewDecoder(io.LimitReader(f, 4097)).Decode(&owner)
		_ = f.Close()
		if e != nil || owner.Version != 1 || owner.Root != canonical || owner.Name != entry.Name() || owner.CreatedAt.IsZero() || now.Sub(owner.CreatedAt) < verificationTempTTL {
			continue
		}
		if e = os.RemoveAll(dir); e != nil {
			return errors.New("过期备份预检明文暂存清理失败，请核对目录权限")
		}
	}
	return nil
}
