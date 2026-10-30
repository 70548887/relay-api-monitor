package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpstreamDNSYAMLInterpolationAndEnvironment(t *testing.T) {
	t.Setenv("TEST_DOH", "https://1.1.1.1/dns-query")
	t.Setenv("RELAYLAB_UPSTREAM_DNS_OVER_HTTPS", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("upstream:\n  dns_over_https: ${TEST_DOH}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Upstream.DNSOverHTTPS != "https://1.1.1.1/dns-query" {
		t.Fatalf("%+v %v", cfg.Upstream, err)
	}
	t.Setenv("RELAYLAB_UPSTREAM_DNS_OVER_HTTPS", "https://resolver.example/dns-query")
	cfg, err = Load(path)
	if err != nil || cfg.Upstream.DNSOverHTTPS != "https://resolver.example/dns-query" {
		t.Fatalf("%+v %v", cfg.Upstream, err)
	}
}

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
	if !cfg.Update.Enabled || cfg.Update.InstallMode != "binary" || cfg.Update.CheckInterval != 6*time.Hour || !cfg.Update.AllowsInstall() {
		t.Fatalf("unexpected update capabilities: %#v", cfg.Update)
	}
}

func TestUpdateInstallModeConfiguration(t *testing.T) {
	t.Setenv("RELAYLAB_UPDATE_INSTALL_MODE", "")
	t.Setenv("RELAYLAB_UPDATE_ENABLED", "")
	t.Setenv("RELAYLAB_UPDATE_CHECK_INTERVAL", "")
	for _, mode := range []string{"", "binary", "container"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("update:\n  enabled: true\n  install_mode: \""+mode+"\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil || cfg.Update.InstallMode == "" || cfg.Update.AllowsInstall() != (mode != "container") {
				t.Fatalf("mode %q: %+v %v", mode, cfg.Update, err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("update:\n  enabled: false\n  install_mode: binary\n  check_interval: 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELAYLAB_UPDATE_INSTALL_MODE", "container")
	cfg, err := Load(path)
	if err != nil || cfg.Update.Enabled || cfg.Update.AllowsInstall() || cfg.Update.CheckInterval != 0 || cfg.Update.InstallMode != "container" {
		t.Fatalf("explicit disable and environment mode: %+v %v", cfg.Update, err)
	}
	t.Setenv("RELAYLAB_UPDATE_INSTALL_MODE", "credential-must-not-appear")
	if _, err := Load(path); err == nil || strings.Contains(err.Error(), "credential-must-not-appear") {
		t.Fatalf("unsafe or missing install mode diagnostic: %v", err)
	}
}

func TestInterpolationCannotInjectYAML(t *testing.T) {
	for _, name := range []string{"RELAYLAB_UPDATE_ENABLED", "RELAYLAB_UPDATE_AUTO_RESTART", "RELAYLAB_UPDATE_CHECK_INTERVAL", "RELAYLAB_UPDATE_GITHUB_TOKEN"} {
		t.Setenv(name, "")
	}
	secret := "fake-secret: # credential\n  enabled: false\n  auto_restart: false\n  github_owner: injected"
	t.Setenv("TEST_SCALAR_SECRET", secret)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("update:\n  github_token: ${TEST_SCALAR_SECRET}\n  enabled: true\n  auto_restart: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Update.GitHubToken != secret || !cfg.Update.Enabled || !cfg.Update.AutoRestart || cfg.Update.GitHubOwner == "injected" {
		t.Fatal("interpolation changed configuration structure")
	}
}

func TestStrictEnvironmentValuesAndDurationOverflow(t *testing.T) {
	for _, test := range []struct{ name, value string }{
		{"RELAYLAB_UPDATE_ENABLED", "perhaps"}, {"RELAYLAB_UPDATE_AUTO_DOWNLOAD", "invalid"}, {"RELAYLAB_UPDATE_AUTO_RESTART", "invalid"},
		{"RELAYLAB_UPDATE_CHECK_INTERVAL", "-1"}, {"RELAYLAB_UPDATE_CHECK_INTERVAL", "9223372036854775807"}, {"RELAYLAB_UPDATE_CHECK_INTERVAL", "9223372037"}, {"RELAYLAB_UPDATE_CHECK_INTERVAL", "1.5"}, {"RELAYLAB_UPDATE_CHECK_INTERVAL", "not-a-number"},
	} {
		t.Run(test.name+test.value, func(t *testing.T) {
			for _, name := range []string{"RELAYLAB_UPDATE_ENABLED", "RELAYLAB_UPDATE_AUTO_DOWNLOAD", "RELAYLAB_UPDATE_AUTO_RESTART", "RELAYLAB_UPDATE_CHECK_INTERVAL"} {
				t.Setenv(name, "")
			}
			t.Setenv(test.name, test.value)
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), test.name) {
				t.Fatalf("invalid environment silently accepted: %v", err)
			}
		})
	}
}

func TestInvalidYAMLAndScalarErrorsNeverEchoSecrets(t *testing.T) {
	t.Setenv("RELAYLAB_UPDATE_CHECK_INTERVAL", "")
	for _, body := range []string{"update:\n  github_token: [fake-literal-secret\n", "update:\n  github_token: fake-literal-secret\n  enabled: fake-literal-secret\n", "update:\n  github_token: fake-literal-secret\n  check_interval: 9223372036854775807\n"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || strings.Contains(err.Error(), "fake-literal-secret") {
			t.Fatalf("unsafe YAML diagnostic: %v", err)
		}
	}
}

func TestScalarBooleanAndIntegerInterpolation(t *testing.T) {
	for _, name := range []string{"RELAYLAB_UPDATE_ENABLED", "RELAYLAB_UPDATE_CHECK_INTERVAL"} {
		t.Setenv(name, "")
	}
	t.Setenv("TEST_UPDATE_BOOL", "false")
	t.Setenv("TEST_UPDATE_SECONDS", "42")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("update:\n  enabled: ${TEST_UPDATE_BOOL}\n  check_interval: ${TEST_UPDATE_SECONDS}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Update.Enabled || cfg.Update.CheckInterval != 42*time.Second {
		t.Fatalf("scalar interpolation failed: %v", err)
	}
}
