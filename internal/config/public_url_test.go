package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicURLConfigurationInterpolationEnvironmentAndClear(t *testing.T) {
	t.Setenv("TEST_PUBLIC_ORIGIN", "https://Relay.Example:8443/")
	t.Setenv("RELAYLAB_PUBLIC_URL", "")
	if err := os.Unsetenv("RELAYLAB_PUBLIC_URL"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  public_url: ${TEST_PUBLIC_ORIGIN}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.PublicURL != "https://relay.example:8443" {
		t.Fatalf("YAML public origin was not normalized: %q %v", cfg.PublicURL, err)
	}
	t.Setenv("RELAYLAB_PUBLIC_URL", "http://localhost:9911")
	cfg, err = Load(path)
	if err != nil || cfg.PublicURL != "http://localhost:9911" {
		t.Fatalf("environment did not override YAML: %q %v", cfg.PublicURL, err)
	}
	t.Setenv("RELAYLAB_PUBLIC_URL", "")
	cfg, err = Load(path)
	if err != nil || cfg.PublicURL != "" {
		t.Fatalf("explicit empty origin did not clear YAML: %q %v", cfg.PublicURL, err)
	}
}

func TestPublicURLRejectsAmbiguousOrCredentialBearingOrigins(t *testing.T) {
	for _, value := range []string{"ftp://relay.example", "https://fake-user:fake-password@relay.example", "https://relay.example/app", "https://relay.example?query=1", "https://relay.example?", "https://relay.example/#part", "https:relay.example", "//relay.example", "https://", "https://relay.example:70000", "https://relay.example:0", "https://relay.example\\evil"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("RELAYLAB_PUBLIC_URL", value)
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "server.public_url") || strings.Contains(err.Error(), "fake-password") {
				t.Fatalf("unsafe/accepted public origin: %v", err)
			}
		})
	}
}
