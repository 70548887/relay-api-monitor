package backups

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func saveStorageSeed(t *testing.T, m *Manager) (StorageInput, Storage) {
	t.Helper()
	in := StorageInput{Endpoint: "https://storage.example.com", Region: "auto", Bucket: "fixture-backups", Prefix: "backups", AccessKeyID: "fixture-save-access", SecretAccessKey: "fixture-save-secret"}
	stored, err := m.SaveStorage(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	health := domain.BackupStorageHealth{Status: "ok", Message: "fixture verified", Operation: "connection_test", CheckedAt: &now, Stage: "completed", DurationMS: 12}
	if err := m.opts.Store.UpdateBackupStorageHealth(context.Background(), stored.ID, health); err != nil {
		t.Fatal(err)
	}
	stored, _, err = m.opts.Store.BackupStorage(context.Background(), stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	return in, stored
}

func TestSaveIdenticalStoragePreservesCurrentRevisionAndLatestHealth(t *testing.T) {
	m, _ := newTestManager(t)
	in, first := saveStorageSeed(t, m)
	catalogPath := filepath.Join(m.opts.BackupDir, "storages", first.ID+".json")
	beforeCatalog, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, withCredentials := range []bool{true, false} {
		input := in
		input.Endpoint = "  https://STORAGE.EXAMPLE.COM/  "
		input.Region = ""
		input.Prefix = "/backups/"
		if !withCredentials {
			input.AccessKeyID, input.SecretAccessKey = "", ""
		}
		got, err := m.SaveStorage(context.Background(), input)
		if err != nil || got.ID != first.ID || got.Revision != first.Revision || !got.CreatedAt.Equal(first.CreatedAt) || !reflect.DeepEqual(got.Health, first.Health) {
			t.Fatalf("identical save reset identity/health: %+v err=%v", got, err)
		}
	}
	// A completed test can update only the DB while the external catalog still
	// has older health. A duplicate save must return that latest DB health.
	latest := first.Health
	latest.Status, latest.Message, latest.ErrorCode = "failed", "fixture newer result", "permission_denied"
	if err := m.opts.Store.UpdateBackupStorageHealth(context.Background(), first.ID, latest); err != nil {
		t.Fatal(err)
	}
	got, err := m.SaveStorage(context.Background(), in)
	if err != nil || !reflect.DeepEqual(got.Health, latest) {
		t.Fatalf("duplicate save overwrote latest health: %+v %v", got.Health, err)
	}
	afterCatalog, err := os.ReadFile(catalogPath)
	if err != nil || string(beforeCatalog) != string(afterCatalog) {
		t.Fatal("duplicate save rewrote catalog")
	}
	var count int
	if err := m.opts.Store.DB.QueryRow("SELECT COUNT(*) FROM backup_storages").Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate saves created revisions: %d %v", count, err)
	}
}

func TestConcurrentIdenticalStorageSavesShareOneRevision(t *testing.T) {
	m, _ := newTestManager(t)
	in, first := saveStorageSeed(t, m)
	var group sync.WaitGroup
	for index := 0; index < 8; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			got, err := m.SaveStorage(context.Background(), in)
			if err != nil || got.ID != first.ID || got.Revision != first.Revision || !reflect.DeepEqual(got.Health, first.Health) {
				t.Errorf("concurrent duplicate save reset revision: %+v %v", got, err)
			}
		}()
	}
	group.Wait()
	var count int
	if err := m.opts.Store.DB.QueryRow("SELECT COUNT(*) FROM backup_storages").Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent saves created %d revisions: %v", count, err)
	}
}

func TestChangedStorageConfigurationCreatesUntestedRevision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*StorageInput)
	}{
		{"secret", func(in *StorageInput) { in.AccessKeyID = ""; in.SecretAccessKey = "fixture-replaced-secret" }},
		{"prefix", func(in *StorageInput) { in.Prefix = "different"; in.AccessKeyID = ""; in.SecretAccessKey = "" }},
		{"bucket", func(in *StorageInput) { in.Bucket = "different-bucket" }},
		{"region", func(in *StorageInput) { in.Region = "us-west-2" }},
		{"path_style", func(in *StorageInput) { in.ForcePathStyle = true }},
		{"access_and_secret", func(in *StorageInput) {
			in.AccessKeyID = "fixture-new-access"
			in.SecretAccessKey = "fixture-new-secret"
		}},
		{"endpoint", func(in *StorageInput) { in.Endpoint = "https://new-storage.example.com" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newTestManager(t)
			in, first := saveStorageSeed(t, m)
			tc.change(&in)
			changed, err := m.SaveStorage(context.Background(), in)
			if err != nil || changed.ID == first.ID || changed.Revision != first.Revision+1 || changed.Health.Status != "untested" || changed.Health.CheckedAt != nil {
				t.Fatalf("changed configuration inherited prior verification: %+v %v", changed, err)
			}
			old, _, err := m.opts.Store.BackupStorage(context.Background(), first.ID)
			if err != nil || !reflect.DeepEqual(old.Health, first.Health) {
				t.Fatal("new revision changed old health")
			}
		})
	}
}

func TestIdenticalSaveNeverImportsHealthFromAnOlderRevision(t *testing.T) {
	m, _ := newTestManager(t)
	in, old := saveStorageSeed(t, m)
	changed := in
	changed.Prefix = "another-prefix"
	if _, err := m.SaveStorage(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	current, err := m.SaveStorage(context.Background(), in)
	if err != nil || current.ID == old.ID || current.Health.Status != "untested" {
		t.Fatalf("reverting config incorrectly selected historical verification: %+v %v", current, err)
	}
	duplicate, err := m.SaveStorage(context.Background(), in)
	if err != nil || duplicate.ID != current.ID || duplicate.Health.Status != "untested" {
		t.Fatal("duplicate save promoted old verification")
	}
}

func TestStorageSaveFailsClosedOnCurrentReadOrDecryptionErrors(t *testing.T) {
	for _, corruptCipher := range []bool{false, true} {
		t.Run(map[bool]string{false: "malformed_current_config", true: "corrupt_current_credentials"}[corruptCipher], func(t *testing.T) {
			m, _ := newTestManager(t)
			in, first := saveStorageSeed(t, m)
			query := "UPDATE backup_storages SET data='{' WHERE id=?"
			if corruptCipher {
				query = "UPDATE backup_storages SET encrypted_credentials='broken' WHERE id=?"
			}
			if _, err := m.opts.Store.DB.Exec(query, first.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := m.SaveStorage(context.Background(), in); err == nil {
				t.Fatal("unreadable current configuration treated as different/new")
			}
			var count int
			_ = m.opts.Store.DB.QueryRow("SELECT COUNT(*) FROM backup_storages").Scan(&count)
			if count != 1 {
				t.Fatal("save created new revision after read/decryption error")
			}
		})
	}
}

func TestChangedEndpointOrAccessKeyCannotReuseSavedSecret(t *testing.T) {
	m, _ := newTestManager(t)
	in, _ := saveStorageSeed(t, m)
	for _, endpointChanged := range []bool{false, true} {
		input := in
		input.SecretAccessKey = ""
		if endpointChanged {
			input.Endpoint = "https://different.example.com"
			input.AccessKeyID = ""
		} else {
			input.AccessKeyID = "fixture-different-access"
		}
		_, err := m.SaveStorage(context.Background(), input)
		if err == nil || strings.Contains(err.Error(), in.SecretAccessKey) {
			t.Fatal("changed endpoint/key reused or exposed secret")
		}
	}
	// Verify the healthy current row was not rewritten by the failed saves.
	current, _, err := m.opts.Store.BackupStorage(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(current)
	if current.Health.Status != "ok" || strings.Contains(string(public), in.SecretAccessKey) {
		t.Fatal("failed saves changed health or exposed credentials")
	}
}

func TestIdenticalStorageRetryRepairsCatalogAfterDatabaseCommit(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	storagesDir := filepath.Join(m.opts.BackupDir, "storages")
	if err := os.Remove(storagesDir); err != nil {
		t.Fatal(err)
	}
	// Simulate an unwritable catalog destination after the SQL transaction.
	if err := os.WriteFile(storagesDir, []byte("fixture obstruction"), 0600); err != nil {
		t.Fatal(err)
	}
	in := StorageInput{Endpoint: "https://storage.example.com", Region: "auto", Bucket: "fixture-backups", Prefix: "backups", AccessKeyID: "fixture-access", SecretAccessKey: "fixture-secret"}
	committed, err := m.SaveStorage(ctx, in)
	if err == nil || committed.ID == "" {
		t.Fatalf("expected committed SQL row and failed catalog write: %+v %v", committed, err)
	}
	if err := os.Remove(storagesDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(storagesDir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	latest := domain.BackupStorageHealth{Status: "ok", Message: "fixture test completed before retry", CheckedAt: &now, Stage: "completed", Operation: "connection_test"}
	if err := m.opts.Store.UpdateBackupStorageHealth(ctx, committed.ID, latest); err != nil {
		t.Fatal(err)
	}
	input := in
	input.AccessKeyID, input.SecretAccessKey = "", ""
	retried, err := m.SaveStorage(ctx, input)
	if err != nil || retried.ID != committed.ID || retried.Revision != committed.Revision || !retried.CreatedAt.Equal(committed.CreatedAt) || !reflect.DeepEqual(retried.Health, latest) {
		t.Fatalf("retry did not preserve committed revision: %+v %v", retried, err)
	}
	raw, err := readCatalogFile(filepath.Join(storagesDir, committed.ID+".json"), 16384)
	var catalog storageCatalog
	if err != nil || json.Unmarshal(raw, &catalog) != nil || catalog.Storage.ID != committed.ID || !reflect.DeepEqual(catalog.Storage.Health, latest) {
		t.Fatalf("missing recovery catalog not repaired with latest state: %+v %v", catalog.Storage, err)
	}
	var count int
	_ = m.opts.Store.DB.QueryRow("SELECT COUNT(*) FROM backup_storages").Scan(&count)
	if count != 1 {
		t.Fatal("retry created a second revision")
	}
}

func TestIdenticalStorageDoesNotOverwriteCorruptOrMismatchedCatalog(t *testing.T) {
	for _, mutation := range []string{"malformed", "id", "revision", "endpoint", "cipher"} {
		t.Run(mutation, func(t *testing.T) {
			m, _ := newTestManager(t)
			in, stored := saveStorageSeed(t, m)
			path := filepath.Join(m.opts.BackupDir, "storages", stored.ID+".json")
			raw, err := readCatalogFile(path, 16384)
			if err != nil {
				t.Fatal(err)
			}
			var catalog storageCatalog
			if err := json.Unmarshal(raw, &catalog); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "id":
				catalog.Storage.ID = strings.Repeat("0", 32)
			case "revision":
				catalog.Storage.Revision++
			case "endpoint":
				catalog.Storage.Endpoint = "https://other.example.com"
			case "cipher":
				catalog.Cipher = "unrelated-cipher"
			}
			raw, _ = json.Marshal(catalog)
			if mutation == "malformed" {
				raw = []byte("{")
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.SaveStorage(context.Background(), in); err == nil {
				t.Fatal("untrusted catalog accepted or silently overwritten")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(raw) {
				t.Fatal("untrusted catalog changed")
			}
		})
	}
}

func TestCatalogRepairAndConcurrentHealthUpdatePreserveLatestResult(t *testing.T) {
	m, _ := newTestManager(t)
	in, stored := saveStorageSeed(t, m)
	path := filepath.Join(m.opts.BackupDir, "storages", stored.ID+".json")
	for attempt := 0; attempt < 8; attempt++ {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var tasks sync.WaitGroup
		tasks.Add(2)
		go func() {
			defer tasks.Done()
			<-start
			if _, err := m.SaveStorage(context.Background(), in); err != nil {
				t.Errorf("repair save failed: %v", err)
			}
		}()
		go func() {
			defer tasks.Done()
			<-start
			m.recordStorageTestHealth(stored.ID, domain.BackupStorageTestResult{Status: "failed", Stage: "reading", ErrorCode: "permission_denied", Message: "fixture latest test failure", DurationMS: int64(attempt + 1)})
		}()
		close(start)
		tasks.Wait()
		current, _, err := m.opts.Store.BackupStorage(context.Background(), stored.ID)
		if err != nil || current.Health.ErrorCode != "permission_denied" || current.Health.DurationMS != int64(attempt+1) {
			t.Fatalf("repair overwrote latest SQL health: %+v %v", current.Health, err)
		}
		raw, err := readCatalogFile(path, 16384)
		var catalog storageCatalog
		if err != nil || json.Unmarshal(raw, &catalog) != nil || !reflect.DeepEqual(catalog.Storage.Health, current.Health) {
			t.Fatalf("catalog health older than SQL result: %+v %v", catalog.Storage.Health, err)
		}
	}
}
