package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestArtifactReasoningAndVerifiedGroupSnapshotsProbeOmitsReasoning(t *testing.T) {
	var requests []map[string]any
	w, st, ctx, c, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		requests = append(requests, request)
		chatResponse(rw, `<svg xmlns="http://www.w3.org/2000/svg"><text>你😊</text></svg>`)
	})
	c.ReasoningEffort = "low"
	if err := st.SaveChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	channel, err := st.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SetGroupVerification(ctx, c.ID, channel.Revision, "verified", "premium", c.GroupProvider, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	m.ReasoningEffort, m.MaxTokens, m.TimeoutSeconds = "high", 9000, 17
	if err = st.SaveModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	probe := enqueue(t, st, ctx, m, "probe", "")
	if _, err = w.WorkOne(ctx, "probe"); err != nil {
		t.Fatal(err)
	}
	j := enqueue(t, st, ctx, m, "artifact", "en_svg")
	if _, err = w.WorkOne(ctx, "artifact"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0]["reasoning_effort"] != nil || requests[1]["reasoning_effort"] != "high" {
		t.Fatal("request reasoning", requests)
	}
	got, err := st.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot requestSnapshot
	if err = json.Unmarshal([]byte(got.Snapshot), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Parameters.ReasoningEffort != "high" || snapshot.Parameters.MaxTokens != 9000 || snapshot.Parameters.TimeoutSeconds != 17 || snapshot.VerifiedGroup != "premium" || snapshot.GroupVerificationRevision != channel.Revision {
		t.Fatal("wrong effective snapshot", snapshot)
	}
	probeJob, err := st.GetJob(ctx, probe.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(probeJob.Snapshot, "reasoning_effort") {
		t.Fatal("probe carried billable reasoning", probeJob.Snapshot)
	}
	arts, err := st.ListArtifacts(ctx, m.ID)
	if err != nil || len(arts) != 1 {
		t.Fatal(arts, err)
	}
	if arts[0].ReasoningEffort != "high" || arts[0].VerifiedGroup != "premium" || arts[0].GroupVerificationStatus != "verified" || !strings.Contains(arts[0].Parameters, `"timeout_seconds":17`) {
		t.Fatal("artifact omitted effective parameters", arts[0])
	}
}

func TestArtifactFallbackTimeoutAndStaleVerificationSnapshot(t *testing.T) {
	w, st, ctx, c, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) { chatResponse(rw, "<html><body>ok</body></html>") })
	settings, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.ArtifactTimeoutSeconds = 0
	if err = st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB.ExecContext(ctx, "UPDATE channels SET data=json_set(data,'$.verified_group','stale','$.group_verification_status','verified','$.group_verification_revision',0) WHERE id=?", c.ID); err != nil {
		t.Fatal(err)
	}
	j := enqueue(t, st, ctx, m, "artifact", "cn_html")
	if _, err = w.WorkOne(ctx, "artifact"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot requestSnapshot
	if err = json.Unmarshal([]byte(got.Snapshot), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Parameters.TimeoutSeconds != 600 || snapshot.VerifiedGroup != "" || snapshot.GroupVerificationStatus != "" {
		t.Fatal(snapshot)
	}
	arts, err := st.ListArtifacts(ctx, m.ID)
	if err != nil || len(arts) != 1 || arts[0].VerifiedGroup != "" {
		t.Fatal(arts, err)
	}
}

func TestInvalidArtifactReasoningFailsBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	w, st, ctx, c, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) { calls.Add(1); chatResponse(rw, "<html>ok</html>") })
	c.ThinkingMode, c.ThinkingBudget = "enabled", 2048
	if err := st.SaveChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	j := enqueue(t, st, ctx, m, "artifact", "cn_html")
	if _, err := w.WorkOne(ctx, "artifact"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetJob(ctx, j.ID)
	if err != nil || got.State != "failed" || got.ErrorCode != "invalid_reasoning" || calls.Load() != 0 {
		t.Fatal(got, calls.Load(), err)
	}
}

func TestStreamProgressCountsVisibleRunesAndThrottlesWrites(t *testing.T) {
	first := `<svg xmlns="http://www.w3.org/2000/svg"><text>你😊`
	remaining := "界</text></svg>"
	entered, release := make(chan struct{}), make(chan struct{})
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/event-stream")
		write := func(text, finish string) {
			payload, _ := json.Marshal(map[string]any{"model": "returned-progress", "choices": []any{map[string]any{"delta": map[string]string{"content": text}, "finish_reason": finish}}})
			fmt.Fprintf(rw, "data: %s\n\n", payload)
			rw.(http.Flusher).Flush()
		}
		write(first, "")
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		for _, runeValue := range remaining {
			write(string(runeValue), "")
		}
		write("", "stop")
		fmt.Fprint(rw, "data: [DONE]\n\n")
		rw.(http.Flusher).Flush()
	})
	if _, err := st.DB.ExecContext(ctx, "CREATE TABLE progress_updates(value INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "CREATE TRIGGER progress_write AFTER UPDATE ON jobs WHEN NEW.state='dispatching' AND COALESCE(json_extract(NEW.data,'$.progress_chars'),0)>COALESCE(json_extract(OLD.data,'$.progress_chars'),0) BEGIN INSERT INTO progress_updates VALUES(json_extract(NEW.data,'$.progress_chars')); END"); err != nil {
		t.Fatal(err)
	}
	j := enqueue(t, st, ctx, m, "artifact", "en_svg")
	done := make(chan error, 1)
	go func() { _, err := w.WorkOne(ctx, "artifact"); done <- err }()
	await(t, entered, "first stream chunk")
	eventually(t, func() bool {
		got, err := st.GetJob(ctx, j.ID)
		return err == nil && got.State == "dispatching" && got.ProgressChars == int64(utf8.RuneCountInString(first)) && got.ReturnedModel == "returned-progress"
	})
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not finish")
	}
	got, err := st.GetJob(ctx, j.ID)
	if err != nil || got.State != "succeeded" || got.ProgressChars != int64(utf8.RuneCountInString(first+remaining)) || got.ProgressAt == nil {
		t.Fatal(got, err)
	}
	var updates int
	if err = st.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM progress_updates").Scan(&updates); err != nil || updates != 1 {
		t.Fatal("progress callbacks exceeded one write per second", updates, err)
	}
	arts, err := st.ListArtifacts(ctx, m.ID)
	if err != nil || len(arts) != 1 || arts[0].RawResponse != first+remaining {
		t.Fatal(arts, err)
	}
}
