package worker

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestWorkerLeaseTakeoverCancelsHTTPWithoutCommittingResults(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	var calls atomic.Int32
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		rw.(http.Flusher).Flush()
		close(entered)
		<-r.Context().Done()
		close(canceled)
	})
	j := enqueue(t, st, ctx, m, "probe", "")
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	await(t, entered, "old worker upstream request")
	if acquired, err := st.AcquireWorkerLease(ctx, "replacement-worker", time.Now().UTC().Add(time.Minute)); err != nil || !acquired {
		t.Fatal(acquired, err)
	}
	await(t, canceled, "HTTP cancellation on worker takeover")
	select {
	case err := <-done:
		if !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("takeover returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old worker did not stop after lease takeover")
	}
	if calls.Load() != 1 {
		t.Fatal("request retried after takeover", calls.Load())
	}
	results, err := st.ListProbeResults(ctx, m.ID, time.Time{})
	if err != nil || len(results) != 0 {
		t.Fatal("old worker contributed statistics", results, err)
	}
	got, err := st.GetJob(ctx, j.ID)
	if err != nil || got.State != "dispatching" {
		t.Fatal("old worker committed terminal state", got, err)
	}
	health, err := st.WorkerHealth(ctx)
	if err != nil || health.Owner != "replacement-worker" {
		t.Fatal("old worker released replacement lease", health, err)
	}
}

func TestDiscoveryResponseFromOldConfigurationIsDiscarded(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	w, st, ctx, c, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"data":[{"id":"stale-model"}]}`))
	})
	c.LastSyncAt = nil
	if err := st.SaveChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); w.maintenance(ctx, time.Now().UTC()) }()
	await(t, entered, "discovery request")
	current, err := st.GetChannel(ctx, c.ID)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	current.ModelsPath = "/new-model-list"
	if err = st.SaveChannelWithSchedules(ctx, *current); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	await(t, done, "stale discovery completion")
	models, err := st.ListModels(ctx, c.ID)
	if err != nil || len(models) != 1 || models[0].ID != m.ID || models[0].Missing {
		t.Fatal("old discovery list mutated new configuration", models, err)
	}
	current, err = st.GetChannel(ctx, c.ID)
	if err != nil || current.LastSyncAt != nil || current.SyncError != "" {
		t.Fatal("old discovery wrote health", current, err)
	}
}

func TestDisablingScheduledRequestCancelsHTTPAndPreservesProbeStatistics(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		rw.(http.Flusher).Flush()
		close(entered)
		<-r.Context().Done()
		close(canceled)
	})
	m.ProbeEnabled = true
	if err := st.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	plans, err := st.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	var schedule domain.Schedule
	for _, p := range plans {
		if p.Kind == "probe" {
			schedule = p
		}
	}
	j, err := st.EnqueueJob(ctx, domain.Job{ModelID: m.ID, ScheduleID: schedule.ID, Kind: "probe"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := w.WorkOne(ctx, "probe"); done <- err }()
	await(t, entered, "scheduled request")
	m.ProbeEnabled = false
	if err = st.SaveModelWithSchedules(ctx, m); err != nil {
		t.Fatal(err)
	}
	await(t, canceled, "disabled schedule HTTP cancellation")
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disabled request did not finish")
	}
	got, err := st.GetJob(ctx, j.ID)
	if err != nil || got.State != "canceled" {
		t.Fatal(got, err)
	}
	results, err := st.ListProbeResults(ctx, m.ID, time.Time{})
	if err != nil || len(results) != 0 {
		t.Fatal("disabled request counted as failure", results, err)
	}
}

func TestDiscoveryConfigurationEditClearsOldSuccessAndRetryBackoff(t *testing.T) {
	var calls atomic.Int32
	w, st, ctx, c, _ := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/replacement-models" {
			http.Error(rw, "old endpoint failure", http.StatusServiceUnavailable)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"data":[{"id":"replacement-model"}]}`))
	})
	c.LastSyncAt = nil
	if err := st.SaveChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	w.maintenance(ctx, now)
	current, err := st.GetChannel(ctx, c.ID)
	if err != nil || current.SyncError == "" {
		t.Fatal(current, err)
	}
	current.ModelsPath = "/replacement-models"
	current.LastSyncAt = &now
	if err = st.SaveChannelWithSchedules(ctx, *current); err != nil {
		t.Fatal(err)
	}
	current, err = st.GetChannel(ctx, c.ID)
	if err != nil || current.LastSyncAt != nil || current.SyncError != "" {
		t.Fatal("new discovery configuration retained old health", current, err)
	}
	w.maintenance(ctx, now.Add(time.Minute))
	if calls.Load() != 2 {
		t.Fatal("old one-hour backoff suppressed new configuration", calls.Load())
	}
	current, err = st.GetChannel(ctx, c.ID)
	if err != nil || current.LastSyncAt == nil || current.SyncError != "" {
		t.Fatal("replacement discovery did not save success", current, err)
	}
}

func TestRequestTimeoutIsRecordedOnceWithoutAutomaticRetry(t *testing.T) {
	var calls atomic.Int32
	canceled := make(chan struct{})
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		rw.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	})
	m.TimeoutSeconds = 1
	if err := st.SaveModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	j := enqueue(t, st, ctx, m, "probe", "")
	if worked, err := w.WorkOne(ctx, "probe"); err != nil || !worked {
		t.Fatal(worked, err)
	}
	await(t, canceled, "timeout HTTP cancellation")
	got, err := st.GetJob(ctx, j.ID)
	if err != nil || got.State != "failed" {
		t.Fatal("timeout not recorded as failed", got, err)
	}
	results, err := st.ListProbeResults(ctx, m.ID, time.Time{})
	if err != nil || len(results) != 1 || results[0].Status != "failed" {
		t.Fatal(results, err)
	}
	if worked, err := w.WorkOne(ctx, "probe"); err != nil || worked {
		t.Fatal("timeout created retry work", worked, err)
	}
	if calls.Load() != 1 {
		t.Fatal("timeout request was resent", calls.Load())
	}
}
