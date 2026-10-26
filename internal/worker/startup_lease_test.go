package worker

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedStartupWaitsForAbandonedLeaseBeforeSendingRequests(t *testing.T) {
	var calls atomic.Int32
	w, st, ctx, _, model := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) { calls.Add(1); chatResponse(rw, "OK") })
	enqueue(t, st, ctx, model, "probe", "")
	if ok, err := st.AcquireWorkerLease(ctx, "crashed-worker", time.Now().UTC()); err != nil || !ok {
		t.Fatal(ok, err)
	}
	until := time.Now().UTC().Add(400 * time.Millisecond)
	if _, err := st.DB.ExecContext(ctx, "UPDATE worker_lease SET lease_until=? WHERE owner=?", until.Format("2006-01-02T15:04:05.000000000Z"), "crashed-worker"); err != nil {
		t.Fatal(err)
	}
	w.SetStartupLeaseWait(3 * time.Second)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	defer func() { cancel(); <-done }()
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 0 || w.ready.Load() {
		t.Fatal("worker became active before lease expiry")
	}
	eventually(t, func() bool { return calls.Load() == 1 && w.ready.Load() })
}

func TestManagedStartupLeaseWaitHonorsCancellation(t *testing.T) {
	w, st, ctx, _, _ := workerFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream request") })
	if ok, err := st.AcquireWorkerLease(ctx, "live-worker", time.Now().UTC()); err != nil || !ok {
		t.Fatal(ok, err)
	}
	w.SetStartupLeaseWait(time.Minute)
	runCtx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := w.Run(runCtx)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("lease cancellation err=%v duration=%v", err, time.Since(start))
	}
	if w.ready.Load() {
		t.Fatal("worker marked ready without lease")
	}
}
