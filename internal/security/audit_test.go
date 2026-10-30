package security

import (
	"fmt"
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

func TestNewDataDirectoryAllowsOnlyManagedWriterTemporaryFiles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		managed string
		dir     bool
		allowed bool
	}{
		{".launcher-write-0", "1", false, true},
		{".launcher-write-4294967295", "1", false, true},
		{"launcher.process.json.0123456789abcdef0123456789abcdef.tmp", "1", false, true},
		{".launcher-write-123", "", false, false},
		{"launcher.process.json.0123456789abcdef0123456789abcdef.tmp", "", false, false},
		{".launcher-write-123", "true", false, false},
		{".launcher-write-123", "1", true, false},
		{"launcher.process.json.0123456789abcdef0123456789abcdef.tmp", "1", true, false},
		{".launcher-write-", "1", false, false},
		{".launcher-write-4294967296", "1", false, false},
		{".launcher-write-01234567890", "1", false, false},
		{".launcher-write-123.txt", "1", false, false},
		{".launcher-write-+1", "1", false, false},
		{"launcher.process.json.0123456789abcdef.tmp", "1", false, false},
		{"launcher.process.json.0123456789abcdef0123456789abcdeg.tmp", "1", false, false},
		{"launcher.process.json.0123456789abcdef0123456789abcdef.tmp.extra", "1", false, false},
		{".unrelated-123", "1", false, false},
	} {
		t.Run(tc.name+"/managed="+tc.managed+fmt.Sprint("/directory=", tc.dir), func(t *testing.T) {
			t.Setenv("RELAYLAB_LAUNCHER", tc.managed)
			dir := t.TempDir()
			path := filepath.Join(dir, tc.name)
			if tc.dir {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := ValidateNewDataDirectory(dir); (err == nil) != tc.allowed {
				t.Fatalf("allowed=%t: %v", tc.allowed, err)
			}
			if !tc.dir {
				if b, err := os.ReadFile(path); err != nil || string(b) != "preserve" {
					t.Fatal("guard modified existing temporary file")
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "master.key")); !os.IsNotExist(err) {
				t.Fatal("guard created a master key")
			}
		})
	}
}

func TestManagedInitializationTemporaryRejectsSymlink(t *testing.T) {
	t.Setenv("RELAYLAB_LAUNCHER", "1")
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "unrelated")
	if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ".launcher-write-123")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if err := ValidateNewDataDirectory(dir); err == nil {
		t.Fatal("managed temporary symlink accepted")
	}
}
