package security

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentInitializersPublishOneCompleteKey(t *testing.T) {
	dir := t.TempDir()
	const count = 16
	vaults := make([]*Vault, count)
	errs := make([]error, count)
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) { defer wg.Done(); <-gate; vaults[i], errs[i] = LoadVault(dir, false) }(i)
	}
	close(gate)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	key, err := os.ReadFile(filepath.Join(dir, "master.key"))
	if err != nil || len(key) != 32 {
		t.Fatal("master key incomplete", err)
	}
	for _, v := range vaults {
		encrypted, err := v.Encrypt("concurrent-key-check")
		if err != nil {
			t.Fatal(err)
		}
		plain, err := vaults[0].Decrypt(encrypted)
		if err != nil || plain != "concurrent-key-check" {
			t.Fatal("initializers used different master keys", err)
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, ".master-key-*"))
	if err != nil || len(files) != 0 {
		t.Fatal("temporary key not cleaned up", files, err)
	}
}

func TestExistingMasterKeyIsNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadVault(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadVault(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "master.key"))
	if string(before) != string(after) {
		t.Fatal("existing key replaced")
	}
	c, _ := first.Encrypt("saved-key")
	if _, err = second.Decrypt(c); err != nil {
		t.Fatal(err)
	}
}

func TestNewVaultDoesNotClaimUnrelatedDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "unrelated-settings.json")
	if err := os.WriteFile(file, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNewDataDirectory(dir); err == nil {
		t.Fatal("unrelated directory accepted")
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "preserve" {
		t.Fatal("unrelated file changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "master.key")); !os.IsNotExist(err) {
		t.Fatal("key was created in unrelated directory")
	}
}
