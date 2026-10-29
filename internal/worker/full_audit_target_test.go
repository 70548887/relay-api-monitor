package worker

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"relaylab/internal/domain"
)

func TestAuditStaleSelectedTargetNeverReachesUpstream(t *testing.T) {
	for _, kind := range []string{"probe", "artifact"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			w, st, ctx, channel, model := workerFixture(t, func(rw http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				chatResponse(rw, "unexpected request")
			})
			controls := domain.DefaultModelControls()
			channel.ModelControls = &controls
			channel.IntelligenceModelID = model.ID
			if err := st.SaveChannelWithModelControls(ctx, channel, true, true); err != nil {
				t.Fatal(err)
			}
			other := model
			other.ID, other.UpstreamID = domain.NewID(), "explicit-new-target"
			if err := st.SaveModelWithSchedules(ctx, other); err != nil {
				t.Fatal(err)
			}
			plans, err := st.ListSchedules(ctx, model.ID)
			if err != nil {
				t.Fatal(err)
			}
			var plan domain.Schedule
			for _, candidate := range plans {
				if candidate.Kind == kind && (kind == "probe" || candidate.TemplateID == "cn_html") {
					plan = candidate
				}
			}
			if !plan.Enabled {
				t.Fatal("fixture schedule is not enabled", plan)
			}
			job, err := st.EnqueueJob(ctx, domain.Job{ModelID: model.ID, Kind: kind, TemplateID: plan.TemplateID, ScheduleID: plan.ID})
			if err != nil {
				t.Fatal(err)
			}
			current, err := st.GetChannel(ctx, channel.ID)
			if err != nil {
				t.Fatal(err)
			}
			current.IntelligenceModelID = other.ID
			current.IntelligenceSchedule.ModelID = other.ID
			raw, err := json.Marshal(current)
			if err != nil {
				t.Fatal(err)
			}
			// Deliberately preserve revision and queued schedule to exercise the
			// dispatch fence independently of normal reconciliation cancellation.
			if _, err := st.DB.ExecContext(ctx, "UPDATE channels SET data=? WHERE id=?", raw, channel.ID); err != nil {
				t.Fatal(err)
			}
			if worked, err := w.WorkOne(ctx, kind); err != nil || !worked {
				t.Fatal("stale automatic job was not settled", worked, err)
			}
			result, err := st.GetJob(ctx, job.ID)
			if err != nil || result.ErrorCode != "schedule_disabled" || calls.Load() != 0 {
				t.Fatal("stale target reached upstream", result, calls.Load(), err)
			}
		})
	}
}
