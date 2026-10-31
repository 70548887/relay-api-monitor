package main

import (
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
	_ "time/tzdata" // Standalone Windows builds must resolve schedule timezones without a Go installation.

	"golang.org/x/term"
	backupcore "relaylab/internal/backup"
	"relaylab/internal/backups"
	"relaylab/internal/config"
	"relaylab/internal/operationlock"
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

type automaticUpdater interface {
	TryReserveOperation() (func(), bool)
	Check(context.Context) (update.Snapshot, error)
	Download(context.Context) (update.Snapshot, error)
	Config() config.UpdateConfig
}

func runAutomaticCheck(ctx context.Context, manager automaticUpdater) {
	cfg := manager.Config()
	if ctx.Err() != nil || !cfg.Enabled {
		return
	}
	release, reserved := manager.TryReserveOperation()
	if !reserved {
		return
	}
	defer release()
	snapshot, err := manager.Check(ctx)
	if err != nil {
		return
	}
	if cfg.AutoDownload && cfg.AllowsInstall() && snapshot.State == "available" && snapshot.Latest != nil && snapshot.Latest.Available && snapshot.Latest.Compatible {
		_, _ = manager.Download(ctx)
	}
}

func main() {
	if e := run(os.Args[1:]); e != nil {
		log.Printf("Relay Lab: %v", e)
		os.Exit(1)
	}
}

func runUpdateChecks(ctx context.Context, manager automaticUpdater, interval time.Duration) {
	if interval <= 0 || manager == nil || ctx.Err() != nil {
		return
	}
	runAutomaticCheck(ctx, manager)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runAutomaticCheck(ctx, manager)
		}
	}
}

func buildMetadata() runtimecontrol.Build {
	return runtimecontrol.Build{Version: version, Commit: commit, BuildTime: buildTime, OS: runtime.GOOS, Arch: runtime.GOARCH, LauncherProtocol: runtimecontrol.Protocol, DatabaseSchema: 4, MinDatabaseSchema: 1, UsageGuard: 1}
}

func run(args []string) error {
	processStartedAt := time.Now()
	if stamp, err := time.Parse(time.RFC3339Nano, os.Getenv("RELAYLAB_CHILD_STARTED_AT")); err == nil {
		processStartedAt = stamp
	}
	if len(args) == 0 {
		fmt.Println("Relay Lab — 中转 API 监控与模型测试\n命令: serve | worker | init-admin | backup | verify-backup | restore | version\n运行 scripts/start.ps1 启动网站和任务进程。")
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
	if args[0] == "verify-backup" {
		return runVerifyBackup(args[1:])
	}
	switch args[0] {
	case "serve", "worker", "init-admin", "backup", "restore":
	default:
		return fmt.Errorf("未知命令 %q", args[0])
	}
	defaults := config.Default()
	dataDirArg, addrArg := defaults.DataDir, defaults.Address
	publicURLArg := defaults.PublicURL
	configPath := os.Getenv("RELAYLAB_CONFIG")
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.StringVar(&dataDirArg, "data-dir", dataDirArg, "数据目录")
	flags.StringVar(&addrArg, "addr", addrArg, "监听地址")
	flags.StringVar(&publicURLArg, "public-url", publicURLArg, "外部访问 HTTP/HTTPS 源地址，用于反代同源验证")
	flags.StringVar(&configPath, "config", configPath, "YAML 配置文件")
	output := flags.String("output", "", "备份 ZIP 输出路径")
	input := flags.String("input", "", "恢复 ZIP 或加密备份路径")
	recipient := flags.String("recipient", "", "加密备份的 age 恢复公钥（省略保持兼容 ZIP）")
	identityFile := flags.String("identity-file", "", "恢复私钥文件，仅用于加密备份")
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
		case "public-url":
			cfg.PublicURL = publicURLArg
		}
	})
	if cfg.PublicURL, e = config.NormalizePublicURL(cfg.PublicURL); e != nil {
		return e
	}
	abs, e := filepath.Abs(cfg.DataDir)
	if e != nil {
		return e
	}
	cfg.DataDir = abs
	if args[0] == "restore" {
		return restoreWithIdentity(cfg.DataDir, *input, *identityFile)
	}
	if args[0] == "backup" {
		// Lock before opening the vault/database, and release only after the DB
		// defer closes: an installer must never replace files under this handle.
		releaseBackup, lockErr := operationlock.Acquire(cfg.DataDir)
		if lockErr != nil {
			return lockErr
		}
		defer releaseBackup()
	}
	if args[0] == "serve" || args[0] == "worker" {
		releaseUsage, guardErr := runtimecontrol.AcquireUsage(cfg.DataDir, args[0])
		if guardErr != nil {
			return guardErr
		}
		// Registered before any database defers: this lock outlives every DB
		// handle and the runtime report, including startup and shutdown.
		defer releaseUsage()
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
	var stopReport func()
	defer func() {
		_ = st.Close()
		if stopReport != nil {
			stopReport()
		}
	}()
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
	if e = validateUserConnectionKeys(st, vault); e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if args[0] == "serve" || args[0] == "worker" {
		checkStop := func() bool {
			for _, name := range []string{"stop.request", args[0] + ".stop.request"} {
				if info, err := os.Stat(filepath.Join(cfg.DataDir, name)); err == nil && !info.ModTime().Before(processStartedAt) {
					cancel()
					return true
				}
			}
			return false
		}
		if checkStop() {
			return nil
		}
		go func() {
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if checkStop() {
						return
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
		if e = app.SetPublicURL(cfg.PublicURL); e != nil {
			return e
		}
		app.Client, e = upstream.NewWithDNS(cfg.Upstream.DNSOverHTTPS)
		if e != nil {
			return e
		}
		app.SetMaintenanceFile(filepath.Join(cfg.DataDir, "maintenance.request"))
		backupManager, backupErr := backups.New(backups.Options{Store: st, Vault: vault, DataDir: cfg.DataDir, BackupDir: operationlock.BackupRoot(cfg.DataDir), BuildVersion: version, Commit: commit, Enter: app.EnterBackupActivity, DNSOverHTTPS: cfg.Upstream.DNSOverHTTPS, AllowedStorageHosts: strings.FieldsFunc(os.Getenv("RELAYLAB_BACKUP_ALLOWED_STORAGE_HOSTS"), func(r rune) bool { return r == ',' || r == ';' })})
		if backupErr != nil {
			return backupErr
		}
		app.ConfigureBackups(ctx, backupManager, cfg.DataDir)
		wireLauncherRestore(app)
		backupManager.Start(ctx)
		defer backupManager.Close()
		if e = app.StartPrivateTestMaintenance(ctx); e != nil {
			return e
		}
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
		stopReport = runtimecontrol.StartReporter(context.Background(), cfg.DataDir, "serve", buildMetadata(), app.RuntimeState)
		return serveHTTP(ctx, hs, ln)
	case "worker":
		client, clientErr := upstream.NewWithDNS(cfg.Upstream.DNSOverHTTPS)
		if clientErr != nil {
			return clientErr
		}
		w := worker.New(st, vault, client)
		w.SetMaintenanceFile(filepath.Join(cfg.DataDir, "maintenance.request"))
		if os.Getenv("RELAYLAB_LAUNCHER_INSTANCE") != "" {
			w.SetStartupLeaseWait(store.WorkerLeaseDuration + 5*time.Second)
		}
		stopReport = runtimecontrol.StartReporter(context.Background(), cfg.DataDir, "worker", buildMetadata(), w.RuntimeState)
		err := w.Run(ctx)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case "backup":
		return writeBackup(ctx, st, cfg.DataDir, *output, *recipient)
	default:
		return fmt.Errorf("未知命令 %q", args[0])
	}
}

func backup(ctx context.Context, st *store.Store, dataDir, out string) error {
	return backupWithOptions(ctx, st, dataDir, out, "")
}

func backupWithOptions(ctx context.Context, st *store.Store, dataDir, out, recipient string) error {
	release, err := operationlock.Acquire(dataDir)
	if err != nil {
		return err
	}
	defer release()
	return writeBackup(ctx, st, dataDir, out, recipient)
}

func writeBackup(ctx context.Context, st *store.Store, dataDir, out, recipient string) error {
	if out == "" {
		ext := ".zip"
		if recipient != "" {
			ext += ".age"
		}
		out = filepath.Join(filepath.Dir(dataDir), "backups", "relaylab-"+time.Now().UTC().Format("20060102T150405Z")+ext)
	}
	if err := backupcore.CreateFromStore(ctx, st, dataDir, out, backupcore.Options{Recipient: recipient, Version: version, Commit: commit, Legacy: recipient == ""}); err != nil {
		return err
	}
	fmt.Println("备份完成：" + out)
	return nil
}

func restore(dataDir, input string) error {
	return restoreWithIdentity(dataDir, input, "")
}

func restoreWithIdentity(dataDir, input, identityFile string) error {
	identity := ""
	if identityFile != "" {
		info, err := os.Lstat(identityFile)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 4096 {
			return errors.New("恢复私钥文件无效")
		}
		secret, err := os.ReadFile(identityFile)
		if err != nil {
			return err
		}
		defer clear(secret)
		for _, line := range strings.Split(string(secret), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if identity != "" {
				return errors.New("恢复私钥文件只能包含一个私钥")
			}
			identity = line
		}
	}
	if err := backupcore.Restore(context.Background(), dataDir, input, identity); err != nil {
		return err
	}
	fmt.Println("恢复完成：" + dataDir)
	return nil
}

func validateUserConnectionKeys(st *store.Store, v *security.Vault) error {
	connections, err := st.ListUserConnections(context.Background(), "")
	if err != nil {
		return err
	}
	for _, c := range connections {
		if _, err = v.DecryptUserConnection(c.UserID, c.ID, c.EncryptedKey); err != nil {
			return err
		}
	}
	return nil
}
