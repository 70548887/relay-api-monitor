package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/store"
	"relaylab/internal/upstream"
)

func workerFixture(t *testing.T, handler http.HandlerFunc) (*Worker, *store.Store, context.Context, domain.Channel, domain.Model) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	vault, err := security.LoadVault(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := vault.Encrypt("sk-test-very-secret-worker-key")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = st.SavePlatform(ctx, domain.Platform{ID: "p", Name: "platform"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	c := domain.Channel{ID: "c", PlatformID: "p", Name: "channel", BaseURL: server.URL, EncryptedKey: secret, Protocol: "chat", Enabled: true, LastSyncAt: &now}
	if err = st.SaveChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	m := domain.Model{ID: "m", ChannelID: c.ID, UpstreamID: "test-model", DisplayName: "Model"}
	if err = st.SaveModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	w := New(st, vault, upstream.NewTestClient(server.Client()))
	return w, st, ctx, c, m
}
func enqueue(t *testing.T, st *store.Store, ctx context.Context, m domain.Model, kind, template string) domain.Job {
	t.Helper()
	j, err := st.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: kind, TemplateID: template})
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func chatResponse(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"model": "returned-model", "choices": []any{map[string]any{"message": map[string]any{"content": text}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 3, "completion_tokens": 5, "total_tokens": 8}})
}
func await(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for " + label)
	}
}
func eventually(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestWorkOnePersistsMetricsArtifactsAndSanitizedSnapshot(t *testing.T) {
	var prompts []string
	var limits []float64
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		prompt := body["messages"].([]any)[0].(map[string]any)["content"].(string)
		prompts = append(prompts, prompt)
		limits = append(limits, body["max_completion_tokens"].(float64))
		time.Sleep(3 * time.Millisecond)
		if prompt == domain.ChinesePrompt {
			chatResponse(rw, "完整输出\n```html\n<!DOCTYPE html><html><body>pelican</body></html>\n```")
		} else if prompt == domain.EnglishPrompt {
			chatResponse(rw, "```svg\n<svg xmlns=\"http://www.w3.org/2000/svg\"><circle r=\"5\"/></svg>\n```")
		} else {
			chatResponse(rw, "OK")
		}
	})
	settings, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.SlowThresholdMS = 1
	if err = st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	p := enqueue(t, st, ctx, m, "probe", "")
	if ok, err := w.WorkOne(ctx, "probe"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	results, err := st.ListProbeResults(ctx, m.ID, time.Time{})
	if err != nil || len(results) != 1 {
		t.Fatal(results, err)
	}
	if results[0].Status != "slow" || results[0].Usage.TotalTokens == nil || *results[0].Usage.TotalTokens != 8 || results[0].ReturnedModel != "returned-model" {
		t.Fatalf("unexpected metrics %#v", results[0])
	}
	saved, err := st.GetJob(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != "succeeded" || strings.Contains(saved.Snapshot, "secret") || strings.Contains(saved.Snapshot, "http") || strings.Contains(saved.Snapshot, "api_key") {
		t.Fatal("unsafe snapshot", saved.Snapshot)
	}
	for _, id := range []string{"cn_html", "en_svg"} {
		enqueue(t, st, ctx, m, "artifact", id)
		if ok, err := w.WorkOne(ctx, "artifact"); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	arts, err := st.ListArtifacts(ctx, m.ID)
	if err != nil || len(arts) != 2 {
		t.Fatal(len(arts), err)
	}
	if len(prompts) != 3 || prompts[1] != domain.ChinesePrompt || prompts[2] != domain.EnglishPrompt || limits[0] != 1024 || limits[1] != 8192 {
		t.Fatal("defaults or original template changed", prompts, limits)
	}
	for _, a := range arts {
		if a.Code == "" || a.RawResponse == "" || a.TemplateVersion != 1 || a.RequestedModel != m.UpstreamID {
			t.Fatalf("incomplete artifact %#v", a)
		}
	}
}

func TestStaleChannelRevisionFailsWithoutHTTP(t *testing.T) {
	var calls atomic.Int32
	w, st, ctx, c, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) { calls.Add(1); chatResponse(rw, "OK") })
	j := enqueue(t, st, ctx, m, "probe", "")
	c.Name = "edited"
	if err := st.SaveChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := w.WorkOne(ctx, "probe"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetJob(ctx, j.ID)
	if err != nil || got.State != "failed" || got.ErrorCode != "configuration_changed" {
		t.Fatal(got, err)
	}
	if calls.Load() != 0 {
		t.Fatal("stale configuration sent request")
	}
	results, err := st.ListProbeResults(ctx, m.ID, time.Time{})
	if err != nil || len(results) != 0 {
		t.Fatal("preflight failure counted as probe", err)
	}
}

func TestRequestCancellationStopsHTTPAndDoesNotCountProbe(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		rw.(http.Flusher).Flush()
		close(entered)
		<-r.Context().Done()
		close(cancelled)
	})
	j := enqueue(t, st, ctx, m, "probe", "")
	done := make(chan error, 1)
	go func() { _, err := w.WorkOne(ctx, "probe"); done <- err }()
	await(t, entered, "upstream start")
	if err := st.RequestCancel(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	await(t, cancelled, "HTTP cancellation")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not finish cancellation")
	}
	got, err := st.GetJob(ctx, j.ID)
	if err != nil || got.State != "canceled" {
		t.Fatal(got, err)
	}
	results, err := st.ListProbeResults(ctx, m.ID, time.Time{})
	if err != nil || len(results) != 0 {
		t.Fatal("cancel contributed to rate", err)
	}
}

func TestSlowArtifactLeavesProbeCapacityAndWorkerLeaseIsExclusive(t *testing.T) {
	artEntered := make(chan struct{})
	probeEntered := make(chan struct{})
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		prompt := body["messages"].([]any)[0].(map[string]any)["content"].(string)
		if prompt == domain.ChinesePrompt {
			rw.Header().Set("Content-Type", "text/event-stream")
			rw.WriteHeader(http.StatusOK)
			rw.(http.Flusher).Flush()
			close(artEntered)
			<-r.Context().Done()
			return
		}
		close(probeEntered)
		chatResponse(rw, "OK")
	})
	cfg, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProbeConcurrency = 1
	cfg.ArtifactConcurrency = 1
	cfg.ChannelConcurrency = 2
	if err = st.SaveSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	art := enqueue(t, st, ctx, m, "artifact", "cn_html")
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	await(t, artEntered, "slow artifact start")
	second := New(st, w.vault, w.client)
	if err = second.Run(ctx); !errors.Is(err, ErrWorkerActive) {
		t.Fatal("second worker not refused", err)
	}
	probe := enqueue(t, st, ctx, m, "probe", "")
	await(t, probeEntered, "probe while artifact busy")
	eventually(t, func() bool { j, e := st.GetJob(ctx, probe.ID); return e == nil && j.State == "succeeded" })
	current, err := st.GetJob(ctx, art.ID)
	if err != nil || current.State != "dispatching" {
		t.Fatal("artifact did not remain independently active", current, err)
	}
	cancel()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not join children")
	}
	health, err := st.WorkerHealth(ctx)
	if err != nil || health.Online {
		t.Fatal("lease not released", health, err)
	}
	current, err = st.GetJob(ctx, art.ID)
	if err != nil || current.State != "canceled" {
		t.Fatal("shutdown left active job", current, err)
	}
}

func TestCrashAfterDispatchIsRecoveredWithoutResending(t *testing.T) {
	var calls atomic.Int32
	w, st, ctx, c, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) { calls.Add(1); chatResponse(rw, "OK") })
	j := enqueue(t, st, ctx, m, "probe", "")
	claimed, err := st.ClaimJob(ctx, "probe", "crashed-owner", time.Now().UTC())
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	if err = st.MarkDispatching(ctx, j.ID, claimed.LeaseToken, time.Now().UTC().Add(200*time.Millisecond), "snapshot"); err != nil {
		t.Fatal(err)
	}
	key, err := w.vault.Decrypt(c.EncryptedKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.client.Generate(ctx, upstream.Request{Config: configFor(c, key, "chat", ""), Model: m.UpstreamID, Prompt: "OK", MaxTokens: 10, Timeout: time.Second}, nil); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	eventually(t, func() bool { got, e := st.GetJob(ctx, j.ID); return e == nil && got.State == "uncertain" })
	if calls.Load() != 1 {
		t.Fatal("dispatched crash was retried", calls.Load())
	}
	results, err := st.ListProbeResults(ctx, m.ID, time.Time{})
	if err != nil || len(results) != 0 {
		t.Fatal("uncertain counted as result", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestDiscoveryFailurePreservesModelsAndConfigurationRevision(t *testing.T) {
	var calls atomic.Int32
	w, st, ctx, c, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(rw, "temporary failure", http.StatusServiceUnavailable)
	})
	current, err := st.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.LastSyncAt = nil
	if err = st.SaveChannel(ctx, *current); err != nil {
		t.Fatal(err)
	}
	before, err := st.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	w.maintenance(ctx, now)
	w.maintenance(ctx, now.Add(30*time.Minute))
	if calls.Load() != 1 {
		t.Fatal("discovery did not back off one hour", calls.Load())
	}
	after, err := st.GetChannel(ctx, c.ID)
	if err != nil || after.SyncError == "" || after.Revision != before.Revision {
		t.Fatal("sync failure changed config revision", after, err)
	}
	got, err := st.GetModel(ctx, m.ID)
	if err != nil || got.Missing {
		t.Fatal("failed discovery marked model absent", got, err)
	}
}

func TestModelProtocolOverridesReachAllSupportedEndpoints(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				rw.Header().Set("Content-Type", "application/json")
				switch protocol {
				case "chat":
					if r.URL.Path != "/v1/chat/completions" || body["max_completion_tokens"] != float64(1024) {
						t.Error("chat request mismatch", r.URL.Path, body)
					}
					chatResponse(rw, "OK")
				case "responses":
					if r.URL.Path != "/v1/responses" || body["max_output_tokens"] != float64(1024) {
						t.Error("responses request mismatch", r.URL.Path, body)
					}
					_, _ = rw.Write([]byte(`{"model":"served","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}`))
				case "anthropic":
					if r.URL.Path != "/v1/messages" || body["max_tokens"] != float64(1024) || r.Header.Get("x-api-key") == "" {
						t.Error("anthropic request mismatch", r.URL.Path, body)
					}
					_, _ = rw.Write([]byte(`{"type":"message","model":"served","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn"}`))
				}
			})
			m.Protocol = protocol
			if err := st.SaveModel(ctx, m); err != nil {
				t.Fatal(err)
			}
			enqueue(t, st, ctx, m, "probe", "")
			if ok, err := w.WorkOne(ctx, "probe"); err != nil || !ok {
				t.Fatal(ok, err)
			}
			results, err := st.ListProbeResults(ctx, m.ID, time.Time{})
			if err != nil || len(results) != 1 || results[0].Protocol != protocol || results[0].Status != "ok" {
				t.Fatal(results, err)
			}
		})
	}
}

func TestInvalidArtifactDoesNotReplaceLastSuccessfulPreview(t *testing.T) {
	var calls atomic.Int32
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			chatResponse(rw, "<html><body>complete</body></html>")
		} else {
			chatResponse(rw, "<html><body>partial")
		}
	})
	enqueue(t, st, ctx, m, "artifact", "cn_html")
	if _, err := w.WorkOne(ctx, "artifact"); err != nil {
		t.Fatal(err)
	}
	first, err := st.ListArtifacts(ctx, m.ID)
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	failed := enqueue(t, st, ctx, m, "artifact", "cn_html")
	if _, err = w.WorkOne(ctx, "artifact"); err != nil {
		t.Fatal(err)
	}
	after, err := st.ListArtifacts(ctx, m.ID)
	if err != nil || len(after) != 1 || after[0].ID != first[0].ID {
		t.Fatal("bad result replaced valid preview", after, err)
	}
	j, err := st.GetJob(ctx, failed.ID)
	if err != nil || j.State != "failed" || j.ErrorCode != "invalid_artifact" {
		t.Fatal(j, err)
	}
}

func TestConcurrencySettingsApplyWithoutRestart(t *testing.T) {
	entered := make(chan struct{}, 2)
	w, st, ctx, _, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		rw.(http.Flusher).Flush()
		entered <- struct{}{}
		<-r.Context().Done()
	})
	other := m
	other.ID = "second-model"
	other.UpstreamID = "second-upstream"
	if err := st.SaveModel(ctx, other); err != nil {
		t.Fatal(err)
	}
	cfg, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ArtifactConcurrency = 1
	cfg.ChannelConcurrency = 1
	if err = st.SaveSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	enqueue(t, st, ctx, m, "artifact", "cn_html")
	second := enqueue(t, st, ctx, other, "artifact", "cn_html")
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	await(t, entered, "first artifact")
	j, err := st.GetJob(ctx, second.ID)
	if err != nil || j.State != "queued" {
		t.Fatal("channel or artifact budget exceeded", j, err)
	}
	cfg.ArtifactConcurrency = 2
	cfg.ChannelConcurrency = 2
	if err = st.SaveSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	await(t, entered, "second artifact after live limit change")
	cancel()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dynamic worker did not stop")
	}
}
