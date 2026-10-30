package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	backupcore "relaylab/internal/backup"
)

// Verification deliberately runs before loading app configuration or opening
// the active data directory. It only migrates and inspects a disposable copy.
func runVerifyBackup(args []string) error {
	flags := flag.NewFlagSet("verify-backup", flag.ContinueOnError)
	input := flags.String("input", "", "待校验 ZIP 或加密包")
	identity := flags.String("identity-file", "", "恢复私钥文件，私钥正文不放命令行")
	staging := flags.String("staging-dir", "", "受限校验暂存目录；默认用户缓存目录")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return errors.New("命令包含未识别的参数")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	return verifyBackupTo(ctx, *input, *identity, *staging, os.Stdout)
}

func verifyBackupTo(ctx context.Context, input, identityFile, staging string, out io.Writer) (resultErr error) {
	if input == "" {
		return errors.New("请使用 --input 指定备份 ZIP 或加密包")
	}
	identity, e := readVerificationIdentity(identityFile)
	if e != nil {
		return e
	}
	if staging == "" {
		cache, e := os.UserCacheDir()
		if e != nil {
			return errors.New("无法确定校验暂存位置，请指定 --staging-dir")
		}
		staging = filepath.Join(cache, "RelayLab", "backup-verification")
	}
	if e = os.MkdirAll(staging, 0700); e != nil {
		return e
	}
	if e = backupcore.ProtectDirectory(staging); e != nil {
		return e
	}
	release, e := lockVerificationStaging(staging)
	if e != nil {
		return e
	}
	defer release()
	if e = cleanupVerificationStaging(staging, time.Now().UTC()); e != nil {
		return e
	}
	root, e := newVerificationStaging(staging, time.Now().UTC())
	if e != nil {
		return e
	}
	defer func() {
		if e := os.RemoveAll(root); e != nil {
			resultErr = errors.Join(resultErr, errors.New("备份预检暂存清理失败，请核对目录权限"))
		}
	}()
	v, e := backupcore.Validate(ctx, input, identity, root)
	identity = ""
	if e != nil {
		return e
	}
	defer v.Close()
	return json.NewEncoder(out).Encode(struct {
		Valid    bool                `json:"valid"`
		Manifest backupcore.Manifest `json:"manifest"`
	}{true, v.Manifest})
}

func readVerificationIdentity(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	info, e := os.Lstat(path)
	if e != nil {
		return "", e
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", errors.New("恢复私钥文件无效")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	defer clear(b)
	identity := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if identity != "" {
			return "", errors.New("恢复私钥文件只能包含一个私钥")
		}
		identity = line
	}
	return identity, nil
}
