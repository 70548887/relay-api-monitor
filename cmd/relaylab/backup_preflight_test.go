package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	backupcore "relaylab/internal/backup"
	"relaylab/internal/durablejson"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

func TestVerifyBackupNeverReadsActiveConfigurationOrCreatesActiveData(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	st, e := store.Open(filepath.Join(source, "relaylab.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	if _, e = security.LoadVault(source, false); e != nil {
		t.Fatal(e)
	}
	identity, recipient, e := backupcore.GenerateIdentity()
	if e != nil {
		t.Fatal(e)
	}
	input := filepath.Join(root, "snapshot.zip.age")
	if e = backupcore.CreateFromStore(context.Background(), st, source, input, backupcore.Options{Recipient: recipient, Version: "1.5.0"}); e != nil {
		t.Fatal(e)
	}
	keyFile := filepath.Join(root, "recovery.key")
	if e = os.WriteFile(keyFile, []byte(identity), 0600); e != nil {
		t.Fatal(e)
	}
	active := filepath.Join(root, "must-not-exist")
	t.Setenv("RELAYLAB_DATA_DIR", active)
	t.Setenv("RELAYLAB_CONFIG", filepath.Join(root, "not-a-config.yaml"))
	var out bytes.Buffer
	staging := filepath.Join(root, "verify-staging")
	if e = verifyBackupTo(context.Background(), input, keyFile, staging, &out); e != nil {
		t.Fatal(e)
	}
	var result struct {
		Valid    bool                `json:"valid"`
		Manifest backupcore.Manifest `json:"manifest"`
	}
	if e = json.Unmarshal(out.Bytes(), &result); e != nil || !result.Valid || result.Manifest.DatabaseSchema != 4 {
		t.Fatal(out.String(), e)
	}
	if strings.Contains(out.String(), identity) || strings.Contains(out.String(), "master.key\":\"") {
		t.Fatal("secret exposed")
	}
	if _, e = os.Stat(active); !os.IsNotExist(e) {
		t.Fatal("active data touched", e)
	}
	entries, e := os.ReadDir(staging)
	if e != nil || len(entries) != 1 || entries[0].Name() != ".verify.lock" {
		t.Fatal("plaintext staging left", entries, e)
	}
	if e = run([]string{"verify-backup", "--input", input, "--identity-file", keyFile, "--staging-dir", staging}); e != nil {
		t.Fatal("CLI loaded unrelated config", e)
	}
}

func TestVerifyBackupRejectsBadIdentityWithoutPublishingData(t *testing.T) {
	if e := runVerifyBackup([]string{"--identity", "secret"}); e == nil {
		t.Fatal("plaintext identity argument accepted")
	}
	root := t.TempDir()
	key := filepath.Join(root, "key")
	if e := os.WriteFile(key, []byte(strings.Repeat("x", 4097)), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := readVerificationIdentity(key); e == nil {
		t.Fatal("oversized key accepted")
	}
	if e := verifyBackupTo(context.Background(), "", "", "", &bytes.Buffer{}); e == nil {
		t.Fatal("missing input accepted")
	}
}

func TestVerifyBackupCollectsOnlyExpiredManagedPlaintext(t *testing.T) {
	staging := filepath.Join(t.TempDir(), "cache")
	if e := os.MkdirAll(staging, 0700); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	expired, e := newVerificationStaging(staging, now.Add(-3*time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(expired, "master.key"), make([]byte, 32), 0600); e != nil {
		t.Fatal(e)
	}
	fresh, e := newVerificationStaging(staging, now)
	if e != nil {
		t.Fatal(e)
	}
	unknown := filepath.Join(staging, "verify-"+strings.Repeat("a", 32))
	if e = os.Mkdir(unknown, 0700); e != nil {
		t.Fatal(e)
	}
	foreign, e := newVerificationStaging(staging, now.Add(-3*time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if e = durablejson.Write(filepath.Join(foreign, verificationOwnerFile), verificationTempOwner{Version: 1, Root: "another-root", Name: filepath.Base(foreign), CreatedAt: now.Add(-3 * time.Hour)}); e != nil {
		t.Fatal(e)
	}
	legacy := filepath.Join(staging, "verify-123456789")
	if e = os.Mkdir(legacy, 0700); e != nil {
		t.Fatal(e)
	}
	input := filepath.Join(t.TempDir(), "bad.zip")
	if e = os.WriteFile(input, []byte("invalid archive"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = verifyBackupTo(context.Background(), input, "", staging, &bytes.Buffer{}); e == nil {
		t.Fatal("invalid backup accepted")
	}
	if _, e = os.Stat(expired); !os.IsNotExist(e) {
		t.Fatal("expired plaintext remained", e)
	}
	for _, path := range []string{fresh, unknown, foreign, legacy} {
		if _, e = os.Stat(path); e != nil {
			t.Fatal("unowned or fresh staging removed", path, e)
		}
	}
}

func TestVerificationCacheLockPreventsCleanupRacingLiveVerifier(t *testing.T) {
	root := t.TempDir()
	release, e := lockVerificationStaging(root)
	if e != nil {
		t.Fatal(e)
	}
	if second, e := lockVerificationStaging(root); e == nil {
		second()
		release()
		t.Fatal("concurrent cache lock accepted")
	}
	if e = release(); e != nil {
		t.Fatal(e)
	}
	release, e = lockVerificationStaging(root)
	if e != nil {
		t.Fatal("lock not released", e)
	}
	defer release()
}

func TestVerificationCleanupDoesNotFollowSymlink(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	sentinel := filepath.Join(target, "keep")
	if e := os.WriteFile(sentinel, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(root, "verify-"+strings.Repeat("b", 32))
	if e := os.Symlink(target, link); e != nil {
		t.Skip("symlink creation not available", e)
	}
	if e := cleanupVerificationStaging(root, time.Now().Add(24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(sentinel); e != nil {
		t.Fatal("symlink target changed", e)
	}
}
