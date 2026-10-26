package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"relaylab/internal/domain"
)

// Drive the production tick path more frequently than the one-second wakeup so
// this verifies 100 independent models without waiting a minute for idle ticks.
func TestHundredModelsRespectIndependentAndSharedConcurrency(t *testing.T) {
	type observations struct {
		sync.Mutex
		activeProbe, activeArtifact, maxProbe, maxArtifact int
		activeChannels, maxChannels                        map[int]int
		completed                                          map[string]int
		violations                                         []string
	}
	stats := &observations{activeChannels: map[int]int{}, maxChannels: map[int]int{}, completed: map[string]int{}}
	// Hold the first wave until both pools and all three channel budgets are
	// occupied. A 12ms response alone cannot guarantee saturation on a runner
	// instrumented with -race; the concurrency invariant must not depend on CPU speed.
	firstWave := make(chan struct{})
	w, st, ctx, baseChannel, baseModel := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(rw, "bad fixture", 400)
			return
		}
		var index int
		if _, err := fmt.Sscanf(body.Model, "load-%03d", &index); err != nil {
			t.Error(err)
			return
		}
		channel := index % 3
		kind := "probe"
		if len(body.Messages) > 0 && body.Messages[0].Content == domain.ChinesePrompt {
			kind = "artifact"
		}
		stats.Lock()
		stats.activeChannels[channel]++
		if stats.activeChannels[channel] > stats.maxChannels[channel] {
			stats.maxChannels[channel] = stats.activeChannels[channel]
		}
		if kind == "probe" {
			stats.activeProbe++
			if stats.activeProbe > stats.maxProbe {
				stats.maxProbe = stats.activeProbe
			}
		} else {
			stats.activeArtifact++
			if stats.activeArtifact > stats.maxArtifact {
				stats.maxArtifact = stats.activeArtifact
			}
		}
		if stats.activeProbe > 4 || stats.activeArtifact > 2 || stats.activeChannels[channel] > 2 {
			stats.violations = append(stats.violations, fmt.Sprintf("probe=%d artifact=%d channel%d=%d", stats.activeProbe, stats.activeArtifact, channel, stats.activeChannels[channel]))
		}
		select {
		case <-firstWave:
		default:
			if stats.activeProbe == 4 && stats.activeArtifact == 2 {
				close(firstWave)
			}
		}
		stats.Unlock()
		defer func() {
			stats.Lock()
			defer stats.Unlock()
			stats.activeChannels[channel]--
			if kind == "probe" {
				stats.activeProbe--
			} else {
				stats.activeArtifact--
			}
			stats.completed[fmt.Sprintf("%s/%s", body.Model, kind)]++
		}()
		select {
		case <-firstWave:
		case <-r.Context().Done():
			return
		}
		delay := time.Duration(12+index%7) * time.Millisecond
		if kind == "artifact" {
			delay = time.Duration(35+index%17) * time.Millisecond
		}
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		if kind == "artifact" {
			chatResponse(rw, "<html><body>complete</body></html>")
		} else {
			chatResponse(rw, "OK")
		}
	})
	channels := []domain.Channel{baseChannel, baseChannel, baseChannel}
	for i := range channels {
		if i > 0 {
			channels[i].ID = fmt.Sprintf("load-channel-%d", i)
		}
		channels[i].GeneratePath = fmt.Sprintf("/channel/%d/chat/completions", i)
		if err := st.SaveChannel(ctx, channels[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		m := baseModel
		if i > 0 {
			m.ID = fmt.Sprintf("load-model-%03d", i)
		}
		m.ChannelID = channels[i%3].ID
		m.UpstreamID = fmt.Sprintf("load-%03d", i)
		if err := st.SaveModel(ctx, m); err != nil {
			t.Fatal(err)
		}
		enqueue(t, st, ctx, m, "probe", "")
		enqueue(t, st, ctx, m, "artifact", "cn_html")
	}
	settings, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.ProbeConcurrency != 4 || settings.ArtifactConcurrency != 2 || settings.ChannelConcurrency != 2 {
		t.Fatal("unexpected defaults", settings)
	}
	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer func() { cancel(); w.wg.Wait() }()
	for {
		if err = w.tick(runCtx, time.Now().UTC()); err != nil {
			t.Fatalf("scheduler or SQLite operation failed: %v", err)
		}
		w.mu.Lock()
		active := len(w.active)
		w.mu.Unlock()
		stats.Lock()
		completed := len(stats.completed)
		stats.Unlock()
		if completed == 200 && active == 0 {
			break
		}
		select {
		case <-runCtx.Done():
			t.Fatalf("100-model workload incomplete: finished=%d active=%d: %v", completed, active, runCtx.Err())
		case <-time.After(4 * time.Millisecond):
		}
	}
	w.wg.Wait()
	jobs, err := st.ListJobs(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 200 {
		t.Fatalf("jobs=%d", len(jobs))
	}
	for _, j := range jobs {
		if j.State != "succeeded" {
			t.Fatalf("job %s ended %s: %s", j.ID, j.State, j.ErrorMessage)
		}
		if strings.Contains(strings.ToLower(j.ErrorMessage), "locked") {
			t.Fatal("SQLite contention leaked", j.ErrorMessage)
		}
	}
	stats.Lock()
	defer stats.Unlock()
	if len(stats.violations) > 0 {
		t.Fatal("admission budget exceeded", stats.violations)
	}
	if stats.maxProbe != 4 || stats.maxArtifact != 2 {
		t.Fatalf("limits never saturated probe=%d artifact=%d", stats.maxProbe, stats.maxArtifact)
	}
	for channel := 0; channel < 3; channel++ {
		if stats.maxChannels[channel] != 2 {
			t.Fatalf("channel %d maximum=%d", channel, stats.maxChannels[channel])
		}
	}
	for key, count := range stats.completed {
		if count != 1 {
			t.Fatalf("%s sent %d times", key, count)
		}
	}
	t.Logf("100 models / 200 requests completed in %s; maxima probe=%d artifact=%d channels=%v; every model-kind executed exactly once", time.Since(started).Round(time.Millisecond), stats.maxProbe, stats.maxArtifact, stats.maxChannels)
}
