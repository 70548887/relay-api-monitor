package main

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
	"relaylab/internal/config"
	"relaylab/internal/runtimecontrol"
	"relaylab/internal/security"
	"relaylab/internal/server"
	"relaylab/internal/store"
	"relaylab/internal/update"
	"relaylab/internal/upstream"
	"relaylab/internal/worker"
)

var version = "dev"
var commit = "unknown"
var buildTime = "unknown"

func main() {
	if e := run(os.Args[1:]); e != nil {
		log.Printf("Relay Lab: %v", e)
		os.Exit(1)
	}
}

func runUpdateChecks(ctx context.Context, manager *update.Manager, interval time.Duration) {
	if interval <= 0 || manager == nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := manager.Check(ctx); err != nil {
				continue
			}
			if manager.Config().AutoDownload {
				_, _ = manager.Download(ctx)
			}
		}
	}
}

func buildMetadata() runtimecontrol.Build {
	return runtimecontrol.Build{Version: version, Commit: commit, BuildTime: buildTime, OS: runtime.GOOS, Arch: runtime.GOARCH, LauncherProtocol: runtimecontrol.Protocol, DatabaseSchema: 1, MinDatabaseSchema: 1}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Println("Relay Lab — 中转 API 监控与模型测试\n命令: serve | worker | init-admin | backup | restore | version\n运行 scripts/start.ps1 启动网站和任务进程。")
		return nil
	}
	if args[0] == "version" {
		if len(args) == 2 && args[1] == "--json" {
			return json.NewEncoder(os.Stdout).Encode(buildMetadata())
		}
		if len(args) != 1 {
			return errors.New("version 仅支持 --json")
		}
		fmt.Println(version)
		return nil
	}
	switch args[0] {
	case "serve", "worker", "init-admin", "backup", "restore":
	default:
		return fmt.Errorf("未知命令 %q", args[0])
	}
	defaults := config.Default()
	dataDirArg, addrArg := defaults.DataDir, defaults.Address
	configPath := os.Getenv("RELAYLAB_CONFIG")
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.StringVar(&dataDirArg, "data-dir", dataDirArg, "数据目录")
	flags.StringVar(&addrArg, "addr", addrArg, "监听地址")
	flags.StringVar(&configPath, "config", configPath, "YAML 配置文件")
	output := flags.String("output", "", "备份 ZIP 输出路径")
	input := flags.String("input", "", "恢复 ZIP 路径")
	username := flags.String("username", "admin", "管理员账号")
	if e := flags.Parse(args[1:]); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return errors.New("命令包含未识别的参数")
	}
	cfg, e := config.Load(configPath)
	if e != nil {
		return e
	}
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "data-dir":
			cfg.DataDir = dataDirArg
		case "addr":
			cfg.Address = addrArg
		}
	})
	abs, e := filepath.Abs(cfg.DataDir)
	if e != nil {
		return e
	}
	cfg.DataDir = abs
	if args[0] == "restore" {
		return restore(cfg.DataDir, *input)
	}
	dbPath := filepath.Join(cfg.DataDir, "relaylab.db")
	_, statErr := os.Stat(dbPath)
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	existing := statErr == nil
	if !existing {
		if err := security.ValidateNewDataDirectory(cfg.DataDir); err != nil {
			return err
		}
	}
	vault, e := security.LoadVault(cfg.DataDir, existing)
	if e != nil {
		return e
	}
	var st *store.Store
	if args[0] == "worker" || args[0] == "backup" {
		st, e = store.OpenExisting(dbPath)
	} else {
		st, e = store.Open(dbPath)
	}
	if e != nil {
		return e
	}
	defer st.Close()
	// Fail fast on a mismatched master key instead of scheduling doomed requests.
	channels, e := st.ListChannels(context.Background())
	if e != nil {
		return e
	}
	for _, ch := range channels {
		if ch.EncryptedKey != "" {
			if _, e = vault.Decrypt(ch.EncryptedKey); e != nil {
				return e
			}
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if args[0] == "serve" || args[0] == "worker" {
		startedAt := time.Now()
		go func() {
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					for _, name := range []string{"stop.request", args[0] + ".stop.request"} {
						if info, err := os.Stat(filepath.Join(cfg.DataDir, name)); err == nil && !info.ModTime().Before(startedAt) {
							cancel()
							return
						}
					}
				}
			}
		}()
	}
	switch args[0] {
	case "init-admin":
		fmt.Print("管理员密码（至少12字节）: ")
		var password []byte
		if term.IsTerminal(int(os.Stdin.Fd())) {
			password, e = term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
		} else {
			line, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			password = []byte(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
		}
		if e != nil {
			return e
		}
		hash, e := security.PasswordHash(string(password))
		for i := range password {
			password[i] = 0
		}
		if e != nil {
			return e
		}
		if e = st.EnsureAdmin(ctx, *username, hash); e != nil {
			return e
		}
		fmt.Println("管理员已初始化")
		return nil
	case "serve":
		app := server.New(st, vault)
		app.SetMaintenanceFile(filepath.Join(cfg.DataDir, "maintenance.request"))
		updater, updateErr := update.New(cfg.Update, cfg.DataDir, update.BuildInfo{Version: version, Commit: commit, BuildTime: buildTime, OS: runtime.GOOS, Arch: runtime.GOARCH})
		if updateErr != nil {
			return updateErr
		}
		app.SetUpdateManager(updater)
		managed, protocol := wireLauncherUpdater(updater, cfg.Update.AutoRestart)
		app.ConfigureUpdates(ctx, managed, protocol)
		if cfg.Update.Enabled && cfg.Update.CheckInterval > 0 {
			go runUpdateChecks(ctx, updater, cfg.Update.CheckInterval)
		}
		hs := newHTTPServer(cfg.Address, app.Handler(), ctx)
		ln, e := net.Listen("tcp", cfg.Address)
		if e != nil {
			return e
		}
		log.Printf("Relay Lab %s 已启动 http://%s", version, ln.Addr())
		stopReport := runtimecontrol.StartReporter(ctx, cfg.DataDir, "serve", buildMetadata(), app.RuntimeState)
		defer stopReport()
		return serveHTTP(ctx, hs, ln)
	case "worker":
		w := worker.New(st, vault, upstream.New())
		w.SetMaintenanceFile(filepath.Join(cfg.DataDir, "maintenance.request"))
		stopReport := runtimecontrol.StartReporter(ctx, cfg.DataDir, "worker", buildMetadata(), w.RuntimeState)
		defer stopReport()
		err := w.Run(ctx)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case "backup":
		return backup(ctx, st, cfg.DataDir, *output)
	default:
		return fmt.Errorf("未知命令 %q", args[0])
	}
}

func backup(ctx context.Context, st *store.Store, dataDir, out string) error {
	if out == "" {
		out = filepath.Join("backups", "relaylab-"+time.Now().Format("20060102-150405")+".zip")
	}
	out, e := filepath.Abs(out)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(out), 0700); e != nil {
		return e
	}
	temp, e := os.MkdirTemp("", "relaylab-backup-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(temp)
	if e = security.ProtectDirectory(temp); e != nil {
		return e
	}
	db := filepath.Join(temp, "relaylab.db")
	if e = st.Backup(ctx, db); e != nil {
		return e
	}
	key, e := os.ReadFile(filepath.Join(dataDir, "master.key"))
	if e != nil {
		return e
	}
	f, e := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	if e = security.ProtectFile(out); e != nil {
		f.Close()
		os.Remove(out)
		return e
	}
	zw := zip.NewWriter(f)
	success := false
	defer func() {
		_ = zw.Close()
		_ = f.Close()
		if !success {
			_ = os.Remove(out)
		}
	}()
	for _, item := range []struct {
		name string
		data []byte
	}{{"master.key", key}} {
		w, e := zw.Create(item.name)
		if e != nil {
			return e
		}
		if _, e = w.Write(item.data); e != nil {
			return e
		}
	}
	w, e := zw.Create("relaylab.db")
	if e != nil {
		return e
	}
	df, e := os.Open(db)
	if e != nil {
		return e
	}
	_, e = io.Copy(w, df)
	_ = df.Close()
	if e != nil {
		return e
	}
	meta, _ := json.Marshal(map[string]any{"version": version, "created_at": time.Now().UTC(), "schema": 1})
	w, e = zw.Create("manifest.json")
	if e != nil {
		return e
	}
	if _, e = w.Write(meta); e != nil {
		return e
	}
	if e = zw.Close(); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	success = true
	fmt.Println("备份完成：" + out)
	return nil
}
func restore(dataDir, input string) error {
	if input == "" {
		return errors.New("请使用 --input 指定备份 ZIP")
	}
	if _, e := os.Stat(dataDir); e == nil {
		entries, e := os.ReadDir(dataDir)
		if e != nil {
			return e
		}
		if len(entries) > 0 {
			return errors.New("恢复目标目录必须为空；请选择新的数据目录")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	zr, e := zip.OpenReader(input)
	if e != nil {
		return e
	}
	defer zr.Close()
	parent := filepath.Dir(dataDir)
	if e = os.MkdirAll(parent, 0700); e != nil {
		return e
	}
	temp, e := os.MkdirTemp(parent, ".relaylab-restore-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(temp)
	if e = security.ProtectDirectory(temp); e != nil {
		return e
	}
	seen := map[string]bool{}
	for _, f := range zr.File {
		if f.Name != "relaylab.db" && f.Name != "master.key" && f.Name != "manifest.json" {
			return errors.New("备份包含未知文件")
		}
		if seen[f.Name] {
			return errors.New("备份包含重复文件")
		}
		seen[f.Name] = true
		if f.FileInfo().IsDir() || f.Mode()&os.ModeSymlink != 0 {
			return errors.New("备份成员必须为普通文件")
		}
		limit := int64(2 << 30)
		switch f.Name {
		case "master.key":
			limit = 32
			if f.UncompressedSize64 != 32 {
				return errors.New("备份主密钥必须为32字节")
			}
		case "manifest.json":
			limit = 64 << 10
		}
		if f.UncompressedSize64 > uint64(limit) {
			return errors.New("备份成员超过大小限制")
		}
		r, e := f.Open()
		if e != nil {
			return e
		}
		dest, e := os.OpenFile(filepath.Join(temp, f.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			r.Close()
			return e
		}
		var copied int64
		copied, e = io.Copy(dest, io.LimitReader(r, limit+1))
		if e == nil && (copied > limit || uint64(copied) != f.UncompressedSize64) {
			e = errors.New("备份成员实际长度与声明不符")
		}
		if syncErr := dest.Sync(); e == nil {
			e = syncErr
		}
		if closeErr := r.Close(); e == nil {
			e = closeErr
		}
		if closeErr := dest.Close(); e == nil {
			e = closeErr
		}
		if e != nil {
			return e
		}
	}
	if !seen["relaylab.db"] || !seen["master.key"] {
		return errors.New("备份缺少数据库或主密钥")
	}
	if seen["manifest.json"] {
		manifest, readErr := os.ReadFile(filepath.Join(temp, "manifest.json"))
		if readErr != nil {
			return readErr
		}
		var meta struct {
			Schema int `json:"schema"`
		}
		if readErr = json.Unmarshal(manifest, &meta); readErr != nil || meta.Schema != 1 {
			return errors.New("备份格式版本不受支持")
		}
	}
	v, e := security.LoadVault(temp, true)
	if e != nil {
		return e
	}
	st, e := store.OpenExisting(filepath.Join(temp, "relaylab.db"))
	if e != nil {
		return e
	}
	ch, e := st.ListChannels(context.Background())
	if e == nil {
		for _, c := range ch {
			if c.EncryptedKey != "" {
				if _, e = v.Decrypt(c.EncryptedKey); e != nil {
					break
				}
			}
		}
	}
	if e == nil {
		var message string
		e = st.DB.QueryRow("PRAGMA integrity_check").Scan(&message)
		if e == nil && message != "ok" {
			e = errors.New("数据库完整性校验失败")
		}
	}
	st.Close()
	if e != nil {
		return e
	}
	if _, statErr := os.Stat(dataDir); statErr == nil {
		// Remove only the already validated empty directory; a concurrent write
		// makes this fail rather than deleting any user data.
		if e = os.Remove(dataDir); e != nil {
			return e
		}
	}
	if e = os.Rename(temp, dataDir); e != nil {
		return e
	}
	fmt.Println("恢复完成：" + dataDir)
	return nil
}
