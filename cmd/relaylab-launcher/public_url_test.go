package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestLauncherPublicURLFlagOverridesEnvironmentAndYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  public_url: https://yaml.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELAYLAB_PUBLIC_URL", "https://environment.example")
	for _, value := range []string{"https://cli.example:8443", ""} {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		previous := os.Stdout
		os.Stdout = write
		runErr := run([]string{"--config", path, "--public-url", value, "--print-config"})
		os.Stdout = previous
		_ = write.Close()
		raw, readErr := io.ReadAll(read)
		_ = read.Close()
		if runErr != nil || readErr != nil {
			t.Fatal(runErr, readErr)
		}
		var cfg struct {
			PublicURL string `json:"public_url"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil || cfg.PublicURL != value {
			t.Fatalf("CLI origin not preserved: %s %v", raw, err)
		}
	}
}
