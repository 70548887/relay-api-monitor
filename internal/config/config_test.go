package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadYAMLAndEnvironmentOverrides(t *testing.T) {
	t.Setenv("RELAYLAB_DATA_DIR", "")
	t.Setenv("RELAYLAB_ADDR", "")
	t.Setenv("RELAYLAB_UPDATE_GITHUB_OWNER", "")
	t.Setenv("RELAYLAB_UPDATE_GITHUB_REPO", "")
	t.Setenv("RELAYLAB_UPDATE_GITHUB_TOKEN", "")
	t.Setenv("RELAYLAB_UPDATE_PROXY", "")
	t.Setenv("RELAYLAB_UPDATE_AUTO_DOWNLOAD", "")
	t.Setenv("RELAYLAB_UPDATE_AUTO_RESTART", "")
	t.Setenv("RELAYLAB_UPDATE_BACKUP_DIR", "")
	d := t.TempDir()
	path := filepath.Join(d, "config.yaml")
	content := "server:\n  address: 127.0.0.1:9911\n  data_dir: ./runtime\nupdate:\n  enabled: false\n  github_owner: owner\n  github_repo: repo\n  github_token: ${TEST_UPDATE_TOKEN}\n  check_interval: 12\n  backup_dir: ./backups\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("TEST_UPDATE_TOKEN", "secret-value"); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("RELAYLAB_UPDATE_ENABLED", "true"); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("RELAYLAB_UPDATE_CHECK_INTERVAL", "7"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Unsetenv("TEST_UPDATE_TOKEN")
		os.Unsetenv("RELAYLAB_UPDATE_ENABLED")
		os.Unsetenv("RELAYLAB_UPDATE_CHECK_INTERVAL")
	})
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Update.Enabled || cfg.Update.CheckInterval != 7*time.Second || cfg.Update.GitHubToken != "secret-value" {
		t.Fatalf("unexpected update config: %#v", cfg.Update)
	}
	if cfg.DataDir != filepath.Join(d, "runtime") || cfg.Update.BackupDir != filepath.Join(d, "backups") {
		t.Fatalf("paths not resolved: %#v", cfg)
	}
}

func TestDefaultHasSafeUpdateRepository(t *testing.T) {
	cfg := Default()
	if cfg.Update.GitHubOwner != "70548887" || cfg.Update.GitHubRepo != "relay-api-monitor" {
		t.Fatalf("unexpected defaults: %#v", cfg.Update)
	}
}
