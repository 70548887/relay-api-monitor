package worker

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestModelControlsProbeMasterIsCheckedImmediatelyBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	w, st, ctx, channel, model := workerFixture(t, func(rw http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		chatResponse(rw, "OK")
	})
	controls := domain.DefaultModelControls()
	controls.ArtifactChinese, controls.ArtifactEnglish = false, false
	channel.ModelControls = &controls
	channel.IntelligenceModelID = model.ID
	if err := st.SaveChannelWithModelControls(ctx, channel, true); err != nil {
		t.Fatal(err)
	}
	plans, err := st.ListSchedules(ctx, model.ID)
	if err != nil {
		t.Fatal(err)
	}
	var probePlan domain.Schedule
	for _, plan := range plans {
		if plan.Kind == "probe" {
			probePlan = plan
		}
	}
	if !probePlan.Enabled {
		t.Fatal("probe fixture was not enabled")
	}
	queued, err := st.EnqueueJob(ctx, domain.Job{ModelID: model.ID, Kind: "probe", ScheduleID: probePlan.ID})
	if err != nil {
		t.Fatal(err)
	}
	current, err := st.GetChannel(ctx, channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.ModelControls.ProbeEnabled = false
	raw, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the revision and old schedule unchanged to exercise the final
	// execution fence instead of passing via revision/cancellation checks.
	if _, err = st.DB.ExecContext(ctx, "UPDATE channels SET data=? WHERE id=?", raw, channel.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := w.WorkOne(ctx, "probe"); err != nil || !worked {
		t.Fatalf("worker did not settle the stale probe: %v %v", worked, err)
	}
	finished, err := st.GetJob(ctx, queued.ID)
	if err != nil || finished.ErrorCode != "schedule_disabled" || calls.Load() != 0 {
		t.Fatalf("disabled master reached upstream: calls=%d job=%+v err=%v", calls.Load(), finished, err)
	}
	results, err := st.ListProbeResults(ctx, model.ID, time.Time{})
	if err != nil || len(results) != 0 {
		t.Fatal("blocked request became an availability sample", results, err)
	}
	// A deliberately submitted manual probe remains independent of recurring
	// switches, and uses only the fixture's local HTTP server.
	enqueue(t, st, ctx, model, "probe", "")
	if worked, err := w.WorkOne(ctx, "probe"); err != nil || !worked || calls.Load() != 1 {
		t.Fatalf("automatic master blocked a manual test: worked=%v calls=%d err=%v", worked, calls.Load(), err)
	}
}
