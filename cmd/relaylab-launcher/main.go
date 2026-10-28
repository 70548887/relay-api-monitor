package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"relaylab/internal/config"
	"relaylab/internal/launcher"
)

var version = "dev"
var commit = "unknown"
var buildTime = "unknown"

func main() {
	if e := run(os.Args[1:]); e != nil {
		log.Printf("Relay Lab launcher: %v", e)
		os.Exit(1)
	}
}
func run(args []string) error {
	flags := flag.NewFlagSet("relaylab-launcher", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	base, e := os.Getwd()
	if e != nil {
		return e
	}
	if exe, e := os.Executable(); e == nil {
		base = filepath.Dir(exe)
	}
	defaultRelay := filepath.Join(base, "relaylab")
	if runtime.GOOS == "windows" {
		defaultRelay += ".exe"
	}
	relay := flags.String("relaylab", defaultRelay, "relaylab executable path")
	data := flags.String("data-dir", "", "data directory; defaults to YAML/env")
	address := flags.String("addr", "", "web listen address; defaults to YAML/env")
	publicURL := flags.String("public-url", "", "external HTTP/HTTPS origin; defaults to YAML/env")
	control := flags.String("control-addr", envOr("RELAYLAB_LAUNCHER_ADDR", "127.0.0.1:0"), "loopback control address; random port by default")
	configPath := flags.String("config", os.Getenv("RELAYLAB_CONFIG"), "external configuration path")
	token := flags.String("control-token", os.Getenv("RELAYLAB_LAUNCHER_TOKEN"), "control token")
	showVersion := flags.Bool("version", false, "print launcher version")
	printConfig := flags.Bool("print-config", false, "print effective configuration as JSON and exit")
	if e = flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unrecognized arguments: %v", flags.Args())
	}
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if *configPath == "" {
		candidate := filepath.Join(base, "config.yaml")
		if _, e = os.Stat(candidate); e == nil {
			*configPath = candidate
		}
	}
	cfg, e := config.Load(*configPath)
	if e != nil {
		return e
	}
	if *data != "" {
		cfg.DataDir, e = filepath.Abs(*data)
		if e != nil {
			return e
		}
		if cfg.ConfigPath == "" && os.Getenv("RELAYLAB_UPDATE_BACKUP_DIR") == "" {
			cfg.Update.BackupDir = filepath.Join(cfg.DataDir, "backups")
		}
	}
	if *address != "" {
		cfg.Address = *address
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "public-url" {
			cfg.PublicURL = *publicURL
		}
	})
	if cfg.PublicURL, e = config.NormalizePublicURL(cfg.PublicURL); e != nil {
		return e
	}
	absRelay, e := filepath.Abs(*relay)
	if e != nil {
		return e
	}
	if *printConfig {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"address": cfg.Address, "public_url": cfg.PublicURL, "data_dir": cfg.DataDir, "config": cfg.ConfigPath, "relaylab": absRelay, "control_address": *control, "backup_dir": cfg.Update.BackupDir, "protocol": launcher.Protocol, "version": version, "commit": commit, "build_time": buildTime})
	}
	sup, e := launcher.New(launcher.Options{RelayLabPath: absRelay, DataDir: cfg.DataDir, Address: cfg.Address, PublicURL: &cfg.PublicURL, ControlAddress: *control, ControlToken: *token, ConfigPath: cfg.ConfigPath, WorkingDir: filepath.Dir(absRelay), BackupDir: cfg.Update.BackupDir, LauncherVersion: version})
	if e != nil {
		return e
	}
	defer sup.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	log.Printf("Relay Lab launcher %s protocol=%d", version, launcher.Protocol)
	return sup.Serve(ctx)
}
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
