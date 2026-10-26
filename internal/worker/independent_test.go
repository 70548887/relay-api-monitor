package worker

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"relaylab/internal/domain"
)

func TestArtifactHistoryIsNotReusedAsNextPromptOrResponse(t *testing.T) {
	calls := 0
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 1 || body.Messages[0].Content != domain.ChinesePrompt || body.Messages[0].Role != "user" {
			t.Fatal("artifact history was used as request context", err)
		}
		text := "FIRST"
		if calls == 2 {
			text = "SECOND"
		}
		chatResponse(rw, `<!DOCTYPE html><html><body>`+text+`</body></html>`)
	})
	ids := []string{}
	for i := 0; i < 2; i++ {
		j := enqueue(t, st, ctx, m, "artifact", "cn_html")
		ids = append(ids, j.ID)
		if ok, err := w.WorkOne(ctx, "artifact"); err != nil || !ok {
			t.Fatal(ok, err)
		}
		stored, _ := st.GetJob(ctx, j.ID)
		if !strings.Contains(stored.Snapshot, `"context_policy":"independent"`) {
			t.Fatal("independent context was not recorded")
		}
	}
	arts, err := st.ListArtifacts(ctx, m.ID)
	if err != nil || calls != 2 || len(arts) != 2 || ids[0] == ids[1] || arts[0].Code == arts[1].Code {
		t.Fatalf("new requests/artifacts not independent: calls=%d arts=%d err=%v", calls, len(arts), err)
	}
}

func TestProbeIgnoresArtworkTokenAndTimeoutOverrides(t *testing.T) {
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["max_completion_tokens"] != float64(1024) {
			t.Errorf("probe used artwork limit: %v", body["max_completion_tokens"])
		}
		chatResponse(rw, "OK")
	})
	m.MaxTokens = 8192
	m.TimeoutSeconds = 600
	if err := st.SaveModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	j := enqueue(t, st, ctx, m, "probe", "")
	if _, err := w.WorkOne(ctx, "probe"); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.GetJob(ctx, j.ID)
	var snapshot requestSnapshot
	if json.Unmarshal([]byte(stored.Snapshot), &snapshot) != nil || snapshot.Parameters.MaxTokens != 1024 || snapshot.Parameters.TimeoutSeconds != 60 {
		t.Fatalf("probe inherited artwork settings: %+v", snapshot.Parameters)
	}
}
