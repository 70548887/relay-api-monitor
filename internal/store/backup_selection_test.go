package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"relaylab/internal/domain"
)

func TestAllocatedBackupStorageImportsDoNotChangeSelection(t *testing.T) {
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "data.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	active, e := s.SaveBackupStorageAllocated(ctx, domain.BackupStorage{ID: "active"}, "cipher", true)
	if e != nil {
		t.Fatal(e)
	}
	imported, e := s.SaveBackupStorageAllocated(ctx, domain.BackupStorage{ID: "imported", Revision: active.Revision}, "cipher", false)
	if e != nil {
		t.Fatal(e)
	}
	if imported.Revision <= active.Revision {
		t.Fatal("display revision was reused")
	}
	v, _, e := s.BackupStorage(ctx, "")
	if e != nil || v.ID != active.ID {
		t.Fatal("import selected another configuration", v, e)
	}
}

func TestAllocatedBackupStorageRevisionsSerializeConcurrentSaves(t *testing.T) {
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "data.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var wg sync.WaitGroup
	results := make(chan domain.BackupStorage, 12)
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.SaveBackupStorageAllocated(ctx, domain.BackupStorage{ID: domain.NewID()}, "fixture", false)
			if e != nil {
				failures <- e
				return
			}
			results <- v
		}()
	}
	wg.Wait()
	close(failures)
	close(results)
	for e := range failures {
		t.Fatal(e)
	}
	seen := map[int]bool{}
	for v := range results {
		if seen[v.Revision] {
			t.Fatal("duplicate revision", v.Revision)
		}
		seen[v.Revision] = true
	}
	if len(seen) != 12 {
		t.Fatal(len(seen))
	}
}
