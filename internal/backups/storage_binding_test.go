package backups

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func storageBindingFixture(t *testing.T, onPut func(*http.Request)) string {
	t.Helper()
	var mu sync.Mutex
	objects := map[string][]byte{}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && onPut != nil {
			onPut(r)
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
			w.WriteHeader(200)
		case http.MethodGet:
			if b, ok := objects[r.URL.Path]; ok {
				_, _ = w.Write(b)
			} else {
				w.WriteHeader(404)
			}
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(fixture.Close)
	return fixture.URL
}

func TestStorageResultKeepsRevisionWhenConfigChangesDuringRequest(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	endpoint := storageBindingFixture(t, func(r *http.Request) {
		enterOnce.Do(func() { close(entered) })
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	m, _ := newTestManager(t)
	allowStorageFixture(t, m, endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	in := storageAuditInput(endpoint)
	original, err := m.SaveStorage(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	type completion struct {
		result domain.BackupStorageTestResult
		err    error
	}
	completed := make(chan completion, 1)
	blank := in
	blank.AccessKeyID, blank.SecretAccessKey = "", ""
	go func() { result, e := m.TestStorageDetailed(ctx, blank); completed <- completion{result, e} }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("test request never reached local fixture")
	}
	changed := in
	changed.Prefix = "different-prefix"
	type saved struct {
		storage Storage
		err     error
	}
	saveDone := make(chan saved, 1)
	go func() { v, e := m.SaveStorage(ctx, changed); saveDone <- saved{v, e} }()
	var current Storage
	select {
	case outcome := <-saveDone:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		current = outcome.storage
	case <-time.After(3 * time.Second):
		t.Fatal("network operation must not hold configuration mutex")
	}
	unblock()
	var outcome completion
	select {
	case outcome = <-completed:
	case <-ctx.Done():
		t.Fatal("connection test did not finish")
	}
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if outcome.result.TestScope != "saved" || outcome.result.StorageID != original.ID || outcome.result.StorageRevision != original.Revision {
		t.Fatal("test result moved to another revision", outcome.result)
	}
	old, _, err := m.opts.Store.BackupStorage(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	active, _, err := m.opts.Store.BackupStorage(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if old.Health.Status != "ok" || active.ID != current.ID || active.Health.Status != "untested" {
		t.Fatal("late test result changed current configuration health")
	}
}

func TestStorageDraftResultNeverClaimsSavedRevision(t *testing.T) {
	endpoint := storageBindingFixture(t, nil)
	m, _ := newTestManager(t)
	allowStorageFixture(t, m, endpoint)
	ctx := context.Background()
	in := storageAuditInput(endpoint)
	original, err := m.SaveStorage(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.TestStorageDetailed(ctx, in)
	if err != nil || result.TestScope != "saved" || result.StorageID != original.ID {
		t.Fatal("saved input not bound", err, result)
	}
	draft := in
	draft.Prefix = "draft-prefix"
	result, err = m.TestStorageDetailed(ctx, draft)
	if err != nil || result.Status != "ok" || result.TestScope != "draft" || result.StorageID != "" || result.StorageRevision != 0 {
		t.Fatal("draft claimed saved revision", err, result)
	}
	current, _, err := m.opts.Store.BackupStorage(ctx, "")
	if err != nil || current.ID != original.ID || current.Health.Status != "ok" {
		t.Fatal("draft changed saved health", err)
	}
	created, err := m.SaveStorage(ctx, draft)
	if err != nil || created.Health.Status != "untested" {
		t.Fatal("saving draft fabricated verified health", err)
	}
}
