package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"relaylab/internal/launcher"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("Relay Lab launcher: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("relaylab-launcher", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	base, _ := os.Getwd()
	if exe, err := os.Executable(); err == nil {
		base = filepath.Dir(exe)
	}
	defaultRelay := filepath.Join(base, "relaylab")
	if filepath.Ext(defaultRelay) == "" && os.PathSeparator == '\\' {
		defaultRelay += ".exe"
	}
	dataDir := filepath.Join(base, "data")
	address := "127.0.0.1:42783"
	controlAddress := envOr("RELAYLAB_LAUNCHER_ADDR", "127.0.0.1:42784")
	flags.StringVar(&defaultRelay, "relaylab", defaultRelay, "relaylab executable path")
	flags.StringVar(&dataDir, "data-dir", envOr("RELAYLAB_DATA_DIR", dataDir), "data directory")
	flags.StringVar(&address, "addr", envOr("RELAYLAB_ADDR", address), "web listen address")
	flags.StringVar(&controlAddress, "control-addr", controlAddress, "loopback control address")
	configPath := flags.String("config", os.Getenv("RELAYLAB_CONFIG"), "external configuration path")
	if *configPath == "" {
		candidate := filepath.Join(base, "config.yaml")
		if _, err := os.Stat(candidate); err == nil {
			*configPath = candidate
		}
	}
	token := flags.String("control-token", os.Getenv("RELAYLAB_LAUNCHER_TOKEN"), "control token")
	showVersion := flags.Bool("version", false, "print version")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unrecognized arguments: %v", flags.Args())
	}
	absRelay, err := filepath.Abs(defaultRelay)
	if err != nil {
		return err
	}
	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	working := filepath.Dir(absRelay)
	sup, err := launcher.New(launcher.Options{RelayLabPath: absRelay, DataDir: absData, Address: address, ControlAddress: controlAddress, ControlToken: *token, ConfigPath: *configPath, WorkingDir: working})
	if err != nil {
		return err
	}
	if _, err = sup.WriteTokenFile(); err != nil {
		return err
	}
	if err = sup.WriteStateFile(); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	log.Printf("Relay Lab launcher %s control=%s", version, controlAddress)
	return sup.Serve(ctx)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
