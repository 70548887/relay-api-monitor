package worker

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpgradeFencePreservesQueuedWorkAndReleasesWorkerLease(t *testing.T) {
	var calls atomic.Int32
	w, st, ctx, _, model := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) { calls.Add(1); chatResponse(rw, "OK") })
	job := enqueue(t, st, ctx, model, "probe", "")
	fence := filepath.Join(t.TempDir(), "maintenance.request")
	if err := os.WriteFile(fence, []byte("upgrade"), 0600); err != nil {
		t.Fatal(err)
	}
	w.SetMaintenanceFile(fence)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	defer cancel()
	eventually(t, func() bool { paused, active, ready := w.RuntimeState(); return paused && active == 0 && ready })
	if j, err := st.GetJob(ctx, job.ID); err != nil || j.State != "queued" {
		t.Fatalf("queue changed: %v %v", j, err)
	}
	if calls.Load() != 0 {
		t.Fatal("started request during validation")
	}
	if err := os.Remove(fence); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { j, err := st.GetJob(ctx, job.ID); return err == nil && j.State == "succeeded" })
	if calls.Load() != 1 {
		t.Fatal("duplicated paid request")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop")
	}
	health, err := st.WorkerHealth(context.Background())
	if err != nil || health.Online {
		t.Fatalf("lease retained: %v %v", health, err)
	}
}
