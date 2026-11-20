package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/fingerprint"
	"relaylab/internal/store"
)

// fakeBank stands in for the reference bank: it records what a round asked of it and returns a fixed analysis.
type fakeBank struct {
	mu          sync.Mutex
	expected    string
	expectedErr error
	judgeErr    error
	analysis    domain.FingerprintAnalysis
	models      []domain.Model
	rounds      [][]fingerprint.Answer
	judged      []string
}

func (b *fakeBank) Expected(_ context.Context, _ string, m domain.Model) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.models = append(b.models, m)
	return b.expected, b.expectedErr
}

func (b *fakeBank) Judge(_ context.Context, _ string, answers []fingerprint.Answer, expected string) (domain.FingerprintAnalysis, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rounds = append(b.rounds, answers)
	b.judged = append(b.judged, expected)
	return b.analysis, b.judgeErr
}

// fingerprintFixture returns a worker with a fake reference bank; handler plays the upstream.
func fingerprintFixture(t *testing.T, handler http.HandlerFunc) (*Worker, *store.Store, context.Context, domain.Model, *fakeBank) {
	t.Helper()
	w, st, ctx, _, m := workerFixture(t, handler)
	bank := &fakeBank{expected: "reference-model", analysis: domain.FingerprintAnalysis{Verdict: domain.FingerprintMatch, Level: domain.FingerprintLevelFull, ValidAnswers: 3}}
	w.SetFingerprintBank(bank)
	return w, st, ctx, m, bank
}

func numberList(count int) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = strconv.Itoa(1 + i%355)
	}
	return strings.Join(parts, ", ")
}

// requestLog records the request bodies an upstream handler received.
type requestLog struct {
	mu     sync.Mutex
	bodies []map[string]any
}

// add stores the body of a request and returns its 1-based position.
func (l *requestLog) add(t *testing.T, r *http.Request) int {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Error(err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bodies = append(l.bodies, body)
	return len(l.bodies)
}

func (l *requestLog) all() []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]map[string]any(nil), l.bodies...)
}

func workFingerprint(t *testing.T, w *Worker, ctx context.Context) {
	t.Helper()
	if ok, err := w.WorkOne(ctx, "fingerprint"); err != nil || !ok {
		t.Fatalf("WorkOne = %v, %v", ok, err)
	}
}

func storedRounds(t *testing.T, st *store.Store, ctx context.Context, m domain.Model) []domain.FingerprintResult {
	t.Helper()
	rounds, err := st.ListFingerprintResults(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rounds
}

func savedJob(t *testing.T, st *store.Store, ctx context.Context, id string) domain.Job {
	t.Helper()
	j, err := st.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return *j
}

func TestFingerprintJobAsksItsQuestionsInTurnAndStoresTheRound(t *testing.T) {
	var requests requestLog
	var inFlight, overlapped atomic.Int32
	w, st, ctx, m, bank := fingerprintFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		if inFlight.Add(1) > 1 {
			overlapped.Store(1)
		}
		defer inFlight.Add(-1)
		n := requests.add(t, r)
		time.Sleep(3 * time.Millisecond)
		chatResponse(rw, fmt.Sprintf("answer %d: %s", n, numberList(200)))
	})
	j := enqueue(t, st, ctx, m, "fingerprint", "")
	workFingerprint(t, w, ctx)

	bodies := requests.all()
	if len(bodies) != 3 || overlapped.Load() != 0 {
		t.Fatalf("%d requests, overlapped %d; want 3 requests one after another", len(bodies), overlapped.Load())
	}
	var prompts []string
	for i, body := range bodies {
		messages := body["messages"].([]any)
		message := messages[0].(map[string]any)
		if len(messages) != 1 || message["role"] != "user" || body["model"] != m.UpstreamID || body["max_completion_tokens"] != float64(8192) {
			t.Fatalf("request %d is not one question to the model with the default output limit: %v", i+1, body)
		}
		for _, field := range []string{"reasoning_effort", "temperature"} {
			if _, sent := body[field]; sent {
				t.Fatalf("request %d sets %s", i+1, field)
			}
		}
		prompts = append(prompts, message["content"].(string))
	}
	if prompts[0] == prompts[1] || prompts[1] == prompts[2] || prompts[0] == prompts[2] {
		t.Fatalf("a question was asked twice: %q", prompts)
	}

	if len(bank.models) != 1 || bank.models[0].ID != m.ID || len(bank.rounds) != 1 || len(bank.rounds[0]) != 3 || bank.judged[0] != "reference-model" {
		t.Fatalf("the bank was asked about models %v, rounds %v, expected %v", bank.models, bank.rounds, bank.judged)
	}
	for i, answer := range bank.rounds[0] {
		if answer.Text != fmt.Sprintf("answer %d: %s", i+1, numberList(200)) || answer.ExpectedCount < fingerprint.MinExpectedCount || answer.ExpectedCount > fingerprint.MaxExpectedCount {
			t.Fatalf("answer %d reached the bank as %+v", i+1, answer)
		}
		if !strings.Contains(prompts[i], strconv.Itoa(answer.ExpectedCount)) {
			t.Fatalf("answer %d expects %d numbers, which question %q does not ask for", i+1, answer.ExpectedCount, prompts[i])
		}
	}

	rounds := storedRounds(t, st, ctx, m)
	if len(rounds) != 1 {
		t.Fatalf("%d rounds stored, want 1", len(rounds))
	}
	round := rounds[0]
	if round.JobID != j.ID || round.ModelID != m.ID || round.Questions != 3 || round.Analysis.Verdict != domain.FingerprintMatch || round.Analysis.ValidAnswers != 3 ||
		round.ReturnedModel != "returned-model" || round.Errors == nil || len(round.Errors) != 0 || round.CreatedAt.IsZero() {
		t.Fatalf("stored round %+v", round)
	}
	usage := round.Usage
	if usage.InputTokens == nil || *usage.InputTokens != 9 || usage.OutputTokens == nil || *usage.OutputTokens != 15 || usage.TotalTokens == nil || *usage.TotalTokens != 24 || usage.CachedInputTokens != nil {
		t.Fatalf("the round does not add up the usage of its three requests: %+v", usage)
	}

	saved := savedJob(t, st, ctx, j.ID)
	if saved.State != "succeeded" || saved.ErrorCode != "" || saved.ReturnedModel != "returned-model" || saved.HTTPStatus != 200 || saved.Usage.TotalTokens == nil || *saved.Usage.TotalTokens != 24 {
		t.Fatalf("finished job %+v", saved)
	}
	var snapshot struct {
		Prompt     string `json:"prompt"`
		Parameters struct {
			MaxTokens      int `json:"max_tokens"`
			TimeoutSeconds int `json:"timeout_seconds"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(saved.Snapshot), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Prompt != strings.Join(prompts, "\n\n") || snapshot.Parameters.MaxTokens != 8192 || snapshot.Parameters.TimeoutSeconds != 180 || strings.Contains(saved.Snapshot, "sk-test") {
		t.Fatalf("request snapshot %s", saved.Snapshot)
	}
}

func TestFingerprintRoundIsScoredOnTheRepliesThatArrived(t *testing.T) {
	var requests requestLog
	w, st, ctx, m, bank := fingerprintFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		if requests.add(t, r) == 2 {
			http.Error(rw, "blocked", http.StatusForbidden)
			return
		}
		chatResponse(rw, numberList(200))
	})
	j := enqueue(t, st, ctx, m, "fingerprint", "")
	workFingerprint(t, w, ctx)

	if got := len(requests.all()); got != 3 {
		t.Fatalf("%d requests, want the third question asked after the second failed", got)
	}
	if len(bank.rounds) != 1 || len(bank.rounds[0]) != 3 || bank.rounds[0][0].Text != numberList(200) || bank.rounds[0][1].Text != "" || bank.rounds[0][2].Text != numberList(200) {
		t.Fatalf("the bank scored %+v, want the first and third reply and an empty second one", bank.rounds)
	}
	rounds := storedRounds(t, st, ctx, m)
	if len(rounds) != 1 || rounds[0].Questions != 3 || len(rounds[0].Errors) != 1 ||
		rounds[0].Errors[0] != (domain.FingerprintRequestError{Question: 2, Code: "authentication_error", Message: "Upstream returned HTTP 403"}) {
		t.Fatalf("stored rounds %+v, want one round that reports the failed second question", rounds)
	}
	if usage := rounds[0].Usage; usage.TotalTokens == nil || *usage.TotalTokens != 16 {
		t.Fatalf("usage %+v, want the two answered requests added up", usage)
	}
	if saved := savedJob(t, st, ctx, j.ID); saved.State != "succeeded" || saved.ErrorCode != "" {
		t.Fatalf("job %+v, want it to succeed on two replies", saved)
	}
}

func TestFingerprintJobFailsWhenNoQuestionIsAnswered(t *testing.T) {
	var requests requestLog
	w, st, ctx, m, bank := fingerprintFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		requests.add(t, r)
		http.Error(rw, "down", http.StatusBadGateway)
	})
	j := enqueue(t, st, ctx, m, "fingerprint", "")
	workFingerprint(t, w, ctx)

	if got := len(requests.all()); got != 3 {
		t.Fatalf("%d requests, want every question asked once", got)
	}
	saved := savedJob(t, st, ctx, j.ID)
	if saved.State != "failed" || saved.ErrorCode != "upstream_unavailable" || saved.ErrorMessage != "Upstream returned HTTP 502" || saved.HTTPStatus != 502 {
		t.Fatalf("job %+v, want it to fail with the error of the first question", saved)
	}
	if rounds := storedRounds(t, st, ctx, m); len(rounds) != 0 || len(bank.rounds) != 0 {
		t.Fatalf("a round without any reply was scored (%d) or stored (%d)", len(bank.rounds), len(rounds))
	}
}

func TestFingerprintReplyStopsOnceItHoldsMoreNumbersThanAsked(t *testing.T) {
	var requests requestLog
	stopped := make(chan struct{})
	w, st, ctx, m, bank := fingerprintFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		requests.add(t, r)
		rw.Header().Set("Content-Type", "text/event-stream")
		giveUp := time.Now().Add(5 * time.Second)
		for i := 0; time.Now().Before(giveUp); i++ {
			chunk, _ := json.Marshal(map[string]any{"model": "returned-model", "choices": []any{map[string]any{"delta": map[string]any{"content": strconv.Itoa(1+i%355) + ", "}}}})
			if _, err := fmt.Fprintf(rw, "data: %s\n\n", chunk); err != nil || r.Context().Err() != nil {
				close(stopped)
				return
			}
			rw.(http.Flusher).Flush()
		}
		t.Error("the request for an endless reply was never stopped")
	})
	m.FingerprintQuestions, m.MaxTokens = 1, 2048
	if err := st.SaveModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	j := enqueue(t, st, ctx, m, "fingerprint", "")
	workFingerprint(t, w, ctx)
	await(t, stopped, "the end of the upstream request")

	bodies := requests.all()
	if len(bodies) != 1 || bodies[0]["max_completion_tokens"] != float64(2048) {
		t.Fatalf("requests %v, want one question with the output limit of the model", bodies)
	}
	if len(bank.rounds) != 1 || len(bank.rounds[0]) != 1 {
		t.Fatalf("the bank scored %+v, want one answer", bank.rounds)
	}
	answer := bank.rounds[0][0]
	if answer.Text != numberList(answer.ExpectedCount) {
		t.Fatalf("the answer holds %d numbers, want exactly the %d asked for", strings.Count(answer.Text, ",")+1, answer.ExpectedCount)
	}
	rounds := storedRounds(t, st, ctx, m)
	if len(rounds) != 1 || rounds[0].Questions != 1 || len(rounds[0].Errors) != 0 || rounds[0].ReturnedModel != "returned-model" {
		t.Fatalf("stored rounds %+v, want one round of one question without an error", rounds)
	}
	if saved := savedJob(t, st, ctx, j.ID); saved.State != "succeeded" || saved.ProgressChars == 0 {
		t.Fatalf("job %+v", saved)
	}
}

func TestFingerprintJobSendsNoRequestWithoutAUsableBank(t *testing.T) {
	for name, prepare := range map[string]func(*Worker, *fakeBank){
		"no bank":          func(w *Worker, _ *fakeBank) { w.SetFingerprintBank(nil) },
		"bank unavailable": func(_ *Worker, bank *fakeBank) { bank.expectedErr = fingerprint.ErrBankUnavailable },
	} {
		t.Run(name, func(t *testing.T) {
			var requests requestLog
			w, st, ctx, m, bank := fingerprintFixture(t, func(rw http.ResponseWriter, r *http.Request) {
				requests.add(t, r)
				chatResponse(rw, numberList(200))
			})
			prepare(w, bank)
			j := enqueue(t, st, ctx, m, "fingerprint", "")
			workFingerprint(t, w, ctx)
			saved := savedJob(t, st, ctx, j.ID)
			if saved.State != "failed" || saved.ErrorCode != "fingerprint_bank_unavailable" || saved.ErrorMessage == "" || saved.Snapshot != "" {
				t.Fatalf("job %+v, want it to fail before dispatch", saved)
			}
			if got := len(requests.all()); got != 0 || len(storedRounds(t, st, ctx, m)) != 0 {
				t.Fatalf("%d requests were sent although the replies could not be scored", got)
			}
		})
	}
}

func TestFingerprintJobFailsWhenScoringFails(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		code string
	}{
		"scoring error":  {errors.New("fingerprint: scoring failed"), "fingerprint_scoring_failed"},
		"bank went away": {fmt.Errorf("%w: file removed", fingerprint.ErrBankUnavailable), "fingerprint_bank_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			w, st, ctx, m, bank := fingerprintFixture(t, func(rw http.ResponseWriter, r *http.Request) { chatResponse(rw, numberList(200)) })
			bank.judgeErr = c.err
			j := enqueue(t, st, ctx, m, "fingerprint", "")
			workFingerprint(t, w, ctx)
			if saved := savedJob(t, st, ctx, j.ID); saved.State != "failed" || saved.ErrorCode != c.code || saved.ErrorMessage == "" || strings.Contains(saved.ErrorMessage, "file removed") {
				t.Fatalf("job %+v, want it to fail with %s", saved, c.code)
			}
			if rounds := storedRounds(t, st, ctx, m); len(rounds) != 0 {
				t.Fatalf("%d rounds stored without an analysis", len(rounds))
			}
		})
	}
}

func TestCanceledFingerprintRoundAsksNoFurtherQuestion(t *testing.T) {
	var requests requestLog
	entered := make(chan struct{})
	w, st, ctx, m, bank := fingerprintFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		if requests.add(t, r) == 1 {
			chatResponse(rw, numberList(200))
			return
		}
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		rw.(http.Flusher).Flush()
		close(entered)
		<-r.Context().Done()
	})
	j := enqueue(t, st, ctx, m, "fingerprint", "")
	done := make(chan error, 1)
	go func() { _, err := w.WorkOne(ctx, "fingerprint"); done <- err }()
	await(t, entered, "the second question")
	if err := st.RequestCancel(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not finish the canceled round")
	}
	if saved := savedJob(t, st, ctx, j.ID); saved.State != "canceled" {
		t.Fatalf("job %+v, want it canceled", saved)
	}
	if got := len(requests.all()); got != 2 || len(bank.rounds) != 0 || len(storedRounds(t, st, ctx, m)) != 0 {
		t.Fatalf("%d requests, %d scored rounds; want the round to end at the second question without a result", got, len(bank.rounds))
	}
}

func TestFingerprintJobsRunUnderTheirOwnConcurrencyLimit(t *testing.T) {
	entered := make(chan struct{}, 2)
	w, st, ctx, m, _ := fingerprintFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		rw.(http.Flusher).Flush()
		entered <- struct{}{}
		<-r.Context().Done()
	})
	other := m
	other.ID, other.UpstreamID = "second-model", "second-upstream"
	if err := st.SaveModel(ctx, other); err != nil {
		t.Fatal(err)
	}
	cfg, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.FingerprintConcurrency = 1
	if err = st.SaveSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	enqueue(t, st, ctx, m, "fingerprint", "")
	second := enqueue(t, st, ctx, other, "fingerprint", "")
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	await(t, entered, "the first round")
	time.Sleep(1500 * time.Millisecond)
	if j := savedJob(t, st, ctx, second.ID); j.State != "queued" {
		t.Fatalf("the second round is %s while the limit is one round at a time", j.State)
	}
	cfg.FingerprintConcurrency = 2
	if err = st.SaveSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	await(t, entered, "the second round after the limit was raised")
	cancel()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not stop")
	}
}

// scheduledFingerprintJob switches scheduled detection on for the model, makes its schedule due and lets the
// scheduler queue the detection.
func scheduledFingerprintJob(t *testing.T, st *store.Store, ctx context.Context, m domain.Model) domain.Job {
	t.Helper()
	if err := st.SaveFingerprintModelSettings(ctx, m.ID, domain.FingerprintModelSettings{Mode: domain.FingerprintModeAuto, Questions: 3, Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	plans, err := st.ListSchedules(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, plan := range plans {
		if plan.Kind == "fingerprint" {
			plan.NextRunAt = now.Add(-time.Minute)
			if err = st.SaveSchedule(ctx, plan); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = st.TickSchedules(ctx, now); err != nil {
		t.Fatal(err)
	}
	jobs, err := st.ListActiveJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.Kind == "fingerprint" && j.ScheduleID != "" {
			return j
		}
	}
	t.Fatal("the due schedule queued no detection")
	return domain.Job{}
}

func TestScheduledFingerprintDetectionRunsOnlyWhileSwitchedOn(t *testing.T) {
	t.Run("switched on", func(t *testing.T) {
		var requests requestLog
		w, st, ctx, m, _ := fingerprintFixture(t, func(rw http.ResponseWriter, r *http.Request) {
			requests.add(t, r)
			chatResponse(rw, numberList(200))
		})
		j := scheduledFingerprintJob(t, st, ctx, m)
		workFingerprint(t, w, ctx)
		saved, rounds := savedJob(t, st, ctx, j.ID), storedRounds(t, st, ctx, m)
		if saved.State != "succeeded" || len(rounds) != 1 || rounds[0].JobID != j.ID || len(requests.all()) != 3 {
			t.Fatalf("job %+v, %d rounds, %d requests; want the scheduled detection to store its round", saved, len(rounds), len(requests.all()))
		}
	})
	t.Run("switched off before it starts", func(t *testing.T) {
		var requests requestLog
		w, st, ctx, m, _ := fingerprintFixture(t, func(rw http.ResponseWriter, r *http.Request) {
			requests.add(t, r)
			chatResponse(rw, numberList(200))
		})
		j := scheduledFingerprintJob(t, st, ctx, m)
		// SaveModel stores the switch without reconciling the schedule, so the queued job is still there.
		stored, err := st.GetModel(ctx, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		stored.FingerprintScheduled = false
		if err = st.SaveModel(ctx, *stored); err != nil {
			t.Fatal(err)
		}
		workFingerprint(t, w, ctx)
		saved := savedJob(t, st, ctx, j.ID)
		if saved.State != "failed" || saved.ErrorCode != "schedule_disabled" || len(requests.all()) != 0 || len(storedRounds(t, st, ctx, m)) != 0 {
			t.Fatalf("job %+v, %d requests; want it refused before any request", saved, len(requests.all()))
		}
	})
}
