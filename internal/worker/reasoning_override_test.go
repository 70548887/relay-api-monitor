package worker

import (
	"encoding/json"
	"net/http"
	"relaylab/internal/domain"
	"testing"
)

func TestManualJobReasoningOverrideIsSentAndSnapshotted(t *testing.T) {
	var requests []map[string]any
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)
		chatResponse(rw, "OK")
	})
	m.ReasoningEffort = "low"
	if err := st.SaveModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	j, err := st.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: "artifact", TemplateID: "cn_html", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := w.WorkOne(ctx, "artifact"); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if requests[0]["reasoning_effort"] != "high" {
		t.Fatalf("request override missing: %+v", requests[0])
	}
	got, err := st.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "failed" && got.State != "succeeded" {
		t.Fatalf("unexpected state %+v", got)
	}
	var snapshot struct {
		Parameters struct {
			ReasoningEffort string `json:"reasoning_effort"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(got.Snapshot), &snapshot); err != nil || snapshot.Parameters.ReasoningEffort != "high" {
		t.Fatalf("snapshot override missing: %s", got.Snapshot)
	}
	defaultJob, err := st.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: "artifact", TemplateID: "cn_html", ReasoningEffort: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := w.WorkOne(ctx, "artifact"); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if _, exists := requests[1]["reasoning_effort"]; exists {
		t.Fatalf("explicit upstream default did not clear the model override: %+v", requests[1])
	}
	defaultJobResult, err := st.GetJob(ctx, defaultJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(defaultJobResult.Snapshot), &snapshot); err != nil || snapshot.Parameters.ReasoningEffort != "default" {
		t.Fatalf("explicit default override was not recorded: %s", defaultJobResult.Snapshot)
	}
}
