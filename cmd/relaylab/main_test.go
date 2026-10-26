package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

func TestBackupRestoreIncludesMasterKeyAndRejectsExistingData(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	v, e := security.LoadVault(dir, false)
	if e != nil {
		t.Fatal(e)
	}
	st, e := store.Open(filepath.Join(dir, "relaylab.db"))
	if e != nil {
		t.Fatal(e)
	}
	cipher, e := v.Encrypt("backup-secret-key")
	if e != nil {
		t.Fatal(e)
	}
	p := domain.Platform{ID: domain.NewID(), Name: "backup"}
	if e = st.SavePlatform(ctx, p); e != nil {
		t.Fatal(e)
	}
	ch := domain.Channel{ID: domain.NewID(), PlatformID: p.ID, Name: "main", EncryptedKey: cipher, Revision: 1}
	if e = st.SaveChannel(ctx, ch); e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(t.TempDir(), "copy.zip")
	if e = backup(ctx, st, dir, out); e != nil {
		t.Fatal(e)
	}
	st.Close()
	target := filepath.Join(t.TempDir(), "restored")
	if e = restore(target, out); e != nil {
		t.Fatal(e)
	}
	rv, e := security.LoadVault(target, true)
	if e != nil {
		t.Fatal(e)
	}
	rs, e := store.OpenExisting(filepath.Join(target, "relaylab.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer rs.Close()
	rc, e := rs.GetChannel(ctx, ch.ID)
	if e != nil {
		t.Fatal(e)
	}
	plain, e := rv.Decrypt(rc.EncryptedKey)
	if e != nil || plain != "backup-secret-key" {
		t.Fatal("restore key mismatch", e)
	}
	if e = restore(target, out); e == nil {
		t.Fatal("existing directory overwritten")
	}
	if _, e = os.Stat(out); e != nil {
		t.Fatal(e)
	}
}
