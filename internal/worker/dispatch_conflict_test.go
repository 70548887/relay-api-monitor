package worker

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"relaylab/internal/store"
)

func TestExpiredUndispatchedJobDoesNotInvalidateWorkerLease(t *testing.T) {
	var calls atomic.Int32
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		chatResponse(rw, "OK")
	})
	enqueue(t, st, ctx, m, "probe", "")
	if acquired, err := st.AcquireWorkerLease(ctx, w.owner, time.Now().UTC()); err != nil || !acquired {
		t.Fatal(acquired, err)
	}
	w.running.Store(true)
	defer w.running.Store(false)
	j, err := st.ClaimJob(ctx, "probe", w.owner, time.Now().UTC())
	if err != nil || j == nil {
		t.Fatal(j, err)
	}
	expired := time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
	if _, err = st.DB.ExecContext(ctx, "UPDATE jobs SET lease_until=?,data=json_set(data,'$.lease_until',?) WHERE id=?", expired, expired, j.ID); err != nil {
		t.Fatal(err)
	}
	err = w.execute(ctx, *j)
	if !errors.Is(err, store.ErrConflict) || errors.Is(err, ErrLeaseLost) {
		t.Fatalf("undispatched job conflict invalidated healthy worker: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("expired claim sent %d requests", calls.Load())
	}
	if err = st.CheckWorkerLease(ctx, w.owner); err != nil {
		t.Fatal("process lease changed", err)
	}
	if err = st.RecoverJobs(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if worked, err := w.WorkOne(ctx, "probe"); err != nil || !worked {
		t.Fatal("worker cannot process recovered unsent claim", worked, err)
	}
	if calls.Load() != 1 {
		t.Fatal("recovered unsent job was not dispatched exactly once", calls.Load())
	}
}

func TestDispatchConflictWithReplacedWorkerLeaseRemainsFatal(t *testing.T) {
	var calls atomic.Int32
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		chatResponse(rw, "OK")
	})
	enqueue(t, st, ctx, m, "probe", "")
	if acquired, err := st.AcquireWorkerLease(ctx, w.owner, time.Now().UTC()); err != nil || !acquired {
		t.Fatal(acquired, err)
	}
	j, err := st.ClaimJob(ctx, "probe", w.owner, time.Now().UTC())
	if err != nil || j == nil {
		t.Fatal(j, err)
	}
	if acquired, err := st.AcquireWorkerLease(ctx, "replacement-worker", time.Now().UTC().Add(time.Minute)); err != nil || !acquired {
		t.Fatal(acquired, err)
	}
	w.running.Store(true)
	defer w.running.Store(false)
	if err = w.execute(ctx, *j); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("replaced worker did not stop", err)
	}
	if calls.Load() != 0 {
		t.Fatal("stale worker dispatched after takeover", calls.Load())
	}
}
