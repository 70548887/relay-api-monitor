package backups

import (
	"context"
	"relaylab/internal/domain"
	"testing"
)

func TestStoredStorageIsRevalidatedAfterRestore(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	id := "restored-storage"
	cipher, e := m.opts.Vault.EncryptBackupCredentials(id, `{"access_key_id":"fixture-access","secret_access_key":"fixture-secret"}`)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.opts.Store.SaveBackupStorage(ctx, domain.BackupStorage{ID: id, Revision: 1, Endpoint: "http://127.0.0.1:9", Bucket: "fixture-bucket", Region: "auto", Prefix: "backups"}, cipher); e != nil {
		t.Fatal(e)
	}
	_, client, e := m.storedClient(ctx, id)
	if e == nil || client != nil {
		t.Fatal("restored unsafe endpoint bypassed policy")
	}
}

func TestProtectedStorageHostnamePolicyIsCaseInsensitive(t *testing.T) {
	for _, host := range []string{"api.zxcbug.com", "API.ZXCBUG.COM.", "bucket.api.zxcbug.com"} {
		if !forbiddenStorageHost(host) {
			t.Fatal("forbidden hostname bypassed local policy")
		}
	}
}
