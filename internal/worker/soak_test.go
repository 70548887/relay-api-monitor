package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/server"
)

// Explicit opt-in: normal unit/CI runs never wait for a soak. Only loopback
// test fixtures are used; no configured production endpoint or key is read.
func TestLongRunningWorkerStability(t *testing.T) {
	durationText := os.Getenv("RELAYLAB_SOAK_DURATION")
	if durationText == "" {
		t.Skip("set RELAYLAB_SOAK_DURATION=30m to run isolated stability testing")
	}
	duration, err := time.ParseDuration(durationText)
	if err != nil || duration < time.Minute || duration > 24*time.Hour {
		t.Fatal("soak duration must be between 1m and 24h")
	}
	output := os.Getenv("RELAYLAB_SOAK_OUTPUT")
	if output == "" {
		t.Fatal("RELAYLAB_SOAK_OUTPUT must name a dedicated evidence directory")
	}
	if err = os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	type sample struct {
		ElapsedSeconds              int64  `json:"elapsed_seconds"`
		Goroutines                  int    `json:"goroutines"`
		HeapBytes                   uint64 `json:"heap_bytes"`
		Requests                    int64  `json:"requests"`
		Succeeded, Failed, Canceled int
		Queue                       int `json:"queued"`
		DatabaseBytes, WALBytes     int64
	}
	type observations struct {
		sync.Mutex
		active        map[string]int
		channelActive map[string]int
		kindActive    map[string]int
		maxKind       map[string]int
		maxChannel    int
		maxSameModel  int
		violations    []string
		served        map[string]int
	}
	stats := &observations{active: map[string]int{}, channelActive: map[string]int{}, kindActive: map[string]int{}, maxKind: map[string]int{}, served: map[string]int{}}
	var requests atomic.Int64
	var injected429, injectedTruncation, injectedTimeout atomic.Int64
	w, st, ctx, baseChannel, baseModel := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		var b struct {
			Model    string
			Messages []struct{ Content string }
		}
		if json.NewDecoder(r.Body).Decode(&b) != nil || len(b.Messages) != 1 {
			http.Error(rw, "invalid fixture request", 400)
			return
		}
		kind := "probe"
		if b.Messages[0].Content == domain.ChinesePrompt || b.Messages[0].Content == domain.EnglishPrompt {
			kind = "artifact"
		}
		var index int
		if _, err := fmt.Sscanf(b.Model, "soak-%03d", &index); err != nil {
			http.Error(rw, "invalid fixture model", 400)
			return
		}
		channel := fmt.Sprint(index % 5)
		key := b.Model + "/" + kind
		stats.Lock()
		stats.active[key]++
		stats.kindActive[kind]++
		stats.channelActive[channel]++
		if stats.active[key] > stats.maxSameModel {
			stats.maxSameModel = stats.active[key]
		}
		if stats.kindActive[kind] > stats.maxKind[kind] {
			stats.maxKind[kind] = stats.kindActive[kind]
		}
		if stats.channelActive[channel] > stats.maxChannel {
			stats.maxChannel = stats.channelActive[channel]
		}
		if stats.active[key] > 1 || stats.kindActive["probe"] > 4 || stats.kindActive["artifact"] > 2 || stats.channelActive[channel] > 2 {
			stats.violations = append(stats.violations, fmt.Sprintf("concurrency %s: model=%d probe=%d artifact=%d channel=%d", key, stats.active[key], stats.kindActive["probe"], stats.kindActive["artifact"], stats.channelActive[channel]))
		}
		stats.served[key]++
		stats.Unlock()
		defer func() {
			stats.Lock()
			stats.active[key]--
			stats.kindActive[kind]--
			stats.channelActive[channel]--
			stats.Unlock()
		}()
		count := requests.Add(1)
		if count%53 == 0 {
			injected429.Add(1)
			http.Error(rw, "controlled fixture rate limit", 429)
			return
		}
		if count%97 == 0 {
			injectedTruncation.Add(1)
			rw.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(rw, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
			return
		}
		delay := time.Duration(40+index%17) * time.Millisecond
		if kind == "artifact" {
			delay = time.Duration(160+index%61) * time.Millisecond
		}
		if count%211 == 0 {
			injectedTimeout.Add(1)
			rw.Header().Set("Content-Type", "text/event-stream")
			rw.WriteHeader(200)
			rw.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		text := "OK"
		if kind == "artifact" {
			text = "<!DOCTYPE html><html><body><svg xmlns=\"http://www.w3.org/2000/svg\"><text>soak</text></svg></body></html>"
			if b.Messages[0].Content == domain.EnglishPrompt {
				text = "<svg xmlns=\"http://www.w3.org/2000/svg\"><text>soak</text></svg>"
			}
		}
		chatResponse(rw, text)
	})
	channels := make([]domain.Channel, 5)
	platform, err := st.GetPlatform(ctx, baseChannel.PlatformID)
	if err != nil {
		t.Fatal(err)
	}
	platform.Public = true
	if err = st.SavePlatform(ctx, *platform); err != nil {
		t.Fatal(err)
	}
	for i := range channels {
		channels[i] = baseChannel
		channels[i].ID = fmt.Sprintf("soak-channel-%d", i)
		channels[i].Name = channels[i].ID
		channels[i].Public = true
		if err = st.SaveChannel(ctx, channels[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		m := baseModel
		m.ID = fmt.Sprintf("soak-model-%03d", i)
		m.ChannelID = channels[i%5].ID
		m.UpstreamID = fmt.Sprintf("soak-%03d", i)
		m.Public = true
		m.ProbeEnabled = true
		m.ArtifactChinese = true
		m.ArtifactEnglish = true
		m.ProbeIntervalSeconds = 30
		m.ArtifactIntervalSeconds = 60
		m.MaxTokens = 1024
		m.TimeoutSeconds = 5
		if err = st.SaveModelWithSchedules(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.ProbeTimeoutSeconds = 5
	settings.ArtifactTimeoutSeconds = 5
	if err = st.SaveSettingsAndReconcile(ctx, settings); err != nil {
		t.Fatal(err)
	}
	web := server.New(st, w.vault)
	web.Client = w.client
	publicServer := httptest.NewServer(web.Handler())
	t.Cleanup(publicServer.Close)
	publicClient := &http.Client{Timeout: 15 * time.Second}
	var httpQueries int
	var maxHTTPQuery time.Duration
	started := time.Now()
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("soak worker did not stop")
		}
	}()
	var samples []sample
	var cancellationRequests, workerRestarts int
	writeJSON := func(name string, v any) {
		b, e := json.MarshalIndent(v, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(output, name)
		if e = os.WriteFile(path+".tmp", b, 0600); e != nil {
			t.Fatal(e)
		}
		if e = os.Rename(path+".tmp", path); e != nil {
			t.Fatal(e)
		}
	}
	collect := func() sample {
		counts, e := st.JobStateCounts(ctx)
		if e != nil {
			t.Fatal(e)
		}
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		result := sample{ElapsedSeconds: int64(time.Since(started).Seconds()), Goroutines: runtime.NumGoroutine(), HeapBytes: mem.HeapAlloc, Requests: requests.Load(), Succeeded: counts["succeeded"], Failed: counts["failed"], Canceled: counts["canceled"], Queue: counts["queued"]}
		var databaseFiles []string
		rows, e := st.DB.QueryContext(ctx, "PRAGMA database_list")
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var seq int
			var name, path string
			if e = rows.Scan(&seq, &name, &path); e != nil {
				rows.Close()
				t.Fatal(e)
			}
			if name == "main" {
				databaseFiles = append(databaseFiles, path)
			}
		}
		rows.Close()
		if len(databaseFiles) > 0 {
			if info, e := os.Stat(databaseFiles[0]); e == nil {
				result.DatabaseBytes = info.Size()
			}
			if info, e := os.Stat(databaseFiles[0] + "-wal"); e == nil {
				result.WALBytes = info.Size()
			}
		}
		return result
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	nextSample := time.Time{}
	nextCancel := started.Add(45 * time.Second)
	nextRestart := started.Add(duration / 3)
	nextHTTP := started
	for time.Since(started) < duration {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("worker exited during soak: %v", err)
		case now := <-ticker.C:
			if now.After(nextHTTP) {
				for _, route := range []string{"/api/v1/public/monitor", "/api/v1/public/intelligence?template_id=cn_html"} {
					began := time.Now()
					response, e := publicClient.Get(publicServer.URL + route)
					if e != nil {
						t.Fatalf("public HTTP query failed: %v", e)
					}
					_, readErr := io.Copy(io.Discard, response.Body)
					response.Body.Close()
					if response.StatusCode != http.StatusOK || readErr != nil {
						t.Fatalf("public HTTP query status=%d err=%v", response.StatusCode, readErr)
					}
					httpQueries++
					if elapsed := time.Since(began); elapsed > maxHTTPQuery {
						maxHTTPQuery = elapsed
					}
				}
				nextHTTP = now.Add(5 * time.Second)
			}
			if now.After(nextSample) {
				current := collect()
				samples = append(samples, current)
				writeJSON("progress.json", current)
				fmt.Printf("SOAK elapsed=%ds requests=%d success=%d failed=%d canceled=%d queued=%d goroutines=%d heap=%d\n", current.ElapsedSeconds, current.Requests, current.Succeeded, current.Failed, current.Canceled, current.Queue, current.Goroutines, current.HeapBytes)
				nextSample = now.Add(time.Minute)
			}
			if now.After(nextCancel) {
				jobs, e := st.ListJobs(ctx, 400)
				if e != nil {
					t.Fatal(e)
				}
				for _, j := range jobs {
					if j.State == "queued" {
						if e = st.RequestCancel(ctx, j.ID); e != nil {
							t.Fatal(e)
						}
						cancellationRequests++
						break
					}
				}
				nextCancel = now.Add(time.Minute)
			}
			if now.After(nextRestart) && workerRestarts < 2 {
				cancel()
				select {
				case e := <-done:
					if e != nil && !errors.Is(e, context.Canceled) {
						t.Fatal(e)
					}
				case <-time.After(15 * time.Second):
					t.Fatal("graceful worker restart stalled")
				}
				workerRestarts++
				w = New(st, w.vault, w.client)
				runCtx, cancel = context.WithCancel(ctx)
				go func() { done <- w.Run(runCtx) }()
				nextRestart = started.Add(time.Duration(workerRestarts+1) * duration / 3)
			}
		}
	}
	// Stop scheduling, drain, then audit committed state. Exclude normal
	// shutdown cancellation from availability conclusions.
	cancel()
	select {
	case e := <-done:
		if e != nil && !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
		done <- e
	case <-time.After(15 * time.Second):
		t.Fatal("final drain stalled")
	}
	final := collect()
	samples = append(samples, final)
	var integrity string
	if err = st.DB.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("SQLite integrity=%s err=%v", integrity, err)
	}
	// Query the full run, including failures older than a bounded latest-jobs
	// page. A 24-hour soak can produce substantially more than 100,000 jobs.
	failureRows, err := st.DB.QueryContext(ctx, "SELECT data FROM jobs WHERE state IN ('failed','claimed','dispatching') AND (state!='failed' OR COALESCE(json_extract(data,'$.error_code'),'') NOT IN ('rate_limited','invalid_response','timeout','truncated','incomplete_stream'))")
	if err != nil {
		t.Fatal(err)
	}
	var jobs []domain.Job
	for failureRows.Next() {
		var encoded string
		var j domain.Job
		if err = failureRows.Scan(&encoded); err != nil {
			failureRows.Close()
			t.Fatal(err)
		}
		if err = json.Unmarshal([]byte(encoded), &j); err != nil {
			failureRows.Close()
			t.Fatal(err)
		}
		jobs = append(jobs, j)
	}
	err = failureRows.Err()
	failureRows.Close()
	if err != nil {
		t.Fatal(err)
	}
	unexpected := []string{}
	running := 0
	for _, j := range jobs {
		if j.State == "claimed" || j.State == "dispatching" {
			running++
		}
		if strings.Contains(strings.ToLower(j.ErrorMessage), "locked") || j.ErrorCode == "storage_error" {
			unexpected = append(unexpected, j.ID+":"+j.ErrorCode)
		}
		if j.State == "failed" && j.ErrorCode != "rate_limited" && j.ErrorCode != "invalid_response" && j.ErrorCode != "timeout" && j.ErrorCode != "truncated" && j.ErrorCode != "incomplete_stream" {
			unexpected = append(unexpected, j.ID+":"+j.ErrorCode)
		}
	}
	stats.Lock()
	violations := append([]string(nil), stats.violations...)
	coverage := len(stats.served)
	maxProbe, maxArtifact, maxChannel, maxSame := stats.maxKind["probe"], stats.maxKind["artifact"], stats.maxChannel, stats.maxSameModel
	stats.Unlock()
	var successfulCombinations int
	if err = st.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT model_id,kind,json_extract(data,'$.template_id') FROM jobs WHERE state='succeeded' GROUP BY model_id,kind,json_extract(data,'$.template_id'))").Scan(&successfulCombinations); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"duration_seconds": time.Since(started).Seconds(), "requested_duration": durationText, "models": 100, "channels": 5, "requests": requests.Load(), "samples": samples, "worker_restarts": workerRestarts, "cancel_requests": cancellationRequests, "injected_429": injected429.Load(), "injected_truncations": injectedTruncation.Load(), "injected_timeouts": injectedTimeout.Load(), "model_kind_coverage": coverage, "max_probe": maxProbe, "max_artifact": maxArtifact, "max_channel": maxChannel, "max_same_model_kind": maxSame, "concurrency_violations": violations, "unexpected_failures": unexpected, "sqlite_integrity": integrity, "running_after_shutdown": running, "real_upstream_requests": 0}
	report["successful_model_template_combinations"] = successfulCombinations
	report["public_http_queries"] = httpQueries
	report["max_public_http_query_ms"] = maxHTTPQuery.Milliseconds()
	writeJSON("report.json", report)
	if len(violations) > 0 || len(unexpected) > 0 || running != 0 || (duration >= 5*time.Minute && (coverage != 200 || successfulCombinations != 300)) {
		t.Fatalf("soak invariants failed; see %s/report.json", output)
	}
	if final.Goroutines > 300 {
		t.Fatalf("unexpected goroutine retention: %d", final.Goroutines)
	}
	fmt.Printf("SOAK PASSED duration=%s requests=%d restarts=%d coverage=%d integrity=%s\n", time.Since(started).Round(time.Second), requests.Load(), workerRestarts, coverage, integrity)
}
