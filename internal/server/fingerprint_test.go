package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/fingerprint"
)

// fakeFingerprintBank stands in for the reference bank, which needs a 5 MB download to score anything.
type fakeFingerprintBank struct {
	status   domain.FingerprintBank
	models   []domain.FingerprintModel
	analysis domain.FingerprintAnalysis
	err      error

	sources  []string
	answers  []fingerprint.Answer
	expected string
	judged   int
}

func (b *fakeFingerprintBank) Catalog(_ context.Context, source string) (domain.FingerprintBank, []domain.FingerprintModel) {
	b.sources = append(b.sources, source)
	return b.status, b.models
}

func (b *fakeFingerprintBank) Judge(_ context.Context, source string, answers []fingerprint.Answer, expected string) (domain.FingerprintAnalysis, error) {
	b.sources = append(b.sources, source)
	b.answers, b.expected = answers, expected
	b.judged++
	return b.analysis, b.err
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q: %v", w.Body.String(), err)
	}
	return body.Error.Code
}

func TestFingerprintOverview(t *testing.T) {
	s, handler := testServer(t)

	w := request(t, handler, "GET", "/api/v1/public/fingerprint", nil, nil, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"models":[]`) || !strings.Contains(w.Body.String(), `"ready":false`) {
		t.Fatalf("overview without a bank: %d %s", w.Code, w.Body.String())
	}

	updated := time.Date(2026, 11, 19, 6, 0, 0, 0, time.UTC)
	bank := &fakeFingerprintBank{
		status: domain.FingerprintBank{Ready: true, Source: "online", BuiltAt: "2026-11-19T05:26:09Z", Models: 2, UpdatedAt: &updated},
		models: []domain.FingerprintModel{{ID: "claude-sonnet-4.6", DisplayName: "claude-sonnet-4.6", Family: "claude", FamilyName: "Claude"}, {ID: "gpt-5.5", DisplayName: "gpt-5.5", Family: "gpt", FamilyName: "GPT"}},
	}
	s.Fingerprint = bank
	w = request(t, handler, "GET", "/api/v1/public/fingerprint", nil, nil, "")
	var overview domain.FingerprintOverview
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil || w.Code != 200 {
		t.Fatalf("overview: %d %s %v", w.Code, w.Body.String(), err)
	}
	if !overview.Bank.Ready || overview.Bank.Models != 2 || overview.Bank.BuiltAt != "2026-11-19T05:26:09Z" || len(overview.Models) != 2 || overview.Models[1].ID != "gpt-5.5" {
		t.Fatalf("overview = %+v", overview)
	}
	if len(bank.sources) != 1 || bank.sources[0] != "" {
		t.Fatalf("bank asked for sources %q, want the online bank", bank.sources)
	}

	bank.status = domain.FingerprintBank{Source: "local", Error: `open D:\secret\bank\unified_bank.json: file not found`}
	bank.models = nil
	w = request(t, handler, "GET", "/api/v1/public/fingerprint", nil, nil, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret") || !strings.Contains(w.Body.String(), `"error":""`) || !strings.Contains(w.Body.String(), `"models":[]`) {
		t.Fatalf("overview of a broken bank must not show the error: %d %s", w.Code, w.Body.String())
	}
}

func TestFingerprintChallenges(t *testing.T) {
	_, handler := testServer(t)
	for query, want := range map[string]int{"": 3, "?count=3": 3, "?count=2": 2, "?count=1": 1} {
		w := request(t, handler, "GET", "/api/v1/public/fingerprint/challenges"+query, nil, nil, "")
		var set domain.FingerprintChallengeSet
		if err := json.Unmarshal(w.Body.Bytes(), &set); err != nil || w.Code != 200 {
			t.Fatalf("challenges%s: %d %s %v", query, w.Code, w.Body.String(), err)
		}
		if len(set.Challenges) != want {
			t.Fatalf("challenges%s returned %d questions, want %d", query, len(set.Challenges), want)
		}
		ids, counts := map[string]bool{}, map[int]bool{}
		for _, challenge := range set.Challenges {
			if challenge.ID == "" || challenge.Prompt == "" || challenge.ExpectedCount < fingerprint.MinExpectedCount || challenge.ExpectedCount > fingerprint.MaxExpectedCount {
				t.Fatalf("challenges%s returned an unusable question %+v", query, challenge)
			}
			if !strings.Contains(challenge.Prompt, fmt.Sprint(challenge.ExpectedCount)) {
				t.Fatalf("question %q does not ask for %d numbers", challenge.Prompt, challenge.ExpectedCount)
			}
			ids[challenge.ID], counts[challenge.ExpectedCount] = true, true
		}
		if len(ids) != want || len(counts) != want {
			t.Fatalf("challenges%s repeated a question: %+v", query, set.Challenges)
		}
	}
	for _, query := range []string{"?count=0", "?count=4", "?count=-1", "?count=two", "?count=1.5"} {
		w := request(t, handler, "GET", "/api/v1/public/fingerprint/challenges"+query, nil, nil, "")
		if w.Code != 400 || errorCode(t, w) != "invalid_count" {
			t.Fatalf("challenges%s: %d %s", query, w.Code, w.Body.String())
		}
	}
}

func fingerprintRound(answers int) domain.FingerprintAnalyzeRequest {
	round := domain.FingerprintAnalyzeRequest{ExpectedModel: " gpt-5.5 "}
	for i := range answers {
		round.Answers = append(round.Answers, domain.FingerprintAnswerInput{Text: fmt.Sprintf("answer %d: 12, 300, 45", i), ExpectedCount: 300 + i})
	}
	return round
}

func TestFingerprintAnalyze(t *testing.T) {
	s, handler := testServer(t)
	confidence := 0.93
	bank := &fakeFingerprintBank{analysis: domain.FingerprintAnalysis{
		Verdict: domain.FingerprintMatch, Level: domain.FingerprintLevelFull, ValidAnswers: 3, ExpectedRank: 1, Confidence: &confidence,
		Expected:   &domain.FingerprintModel{ID: "gpt-5.5", DisplayName: "gpt-5.5", Family: "gpt", FamilyName: "GPT"},
		Candidates: []domain.FingerprintCandidate{{Model: "gpt-5.5", DisplayName: "gpt-5.5", Family: "gpt", FamilyName: "GPT", Score: 0.81, Probability: &confidence}},
		Answers:    []domain.FingerprintAnswer{{Numbers: 300, Minimum: 165, Valid: true}, {Numbers: 301, Minimum: 166, Valid: true}, {Numbers: 302, Minimum: 167, Valid: true}},
	}}
	s.Fingerprint = bank

	w := request(t, handler, "POST", "/api/v1/public/fingerprint/analyze", fingerprintRound(3), nil, "")
	var analysis domain.FingerprintAnalysis
	if err := json.Unmarshal(w.Body.Bytes(), &analysis); err != nil || w.Code != 200 {
		t.Fatalf("analyze: %d %s %v", w.Code, w.Body.String(), err)
	}
	if analysis.Verdict != domain.FingerprintMatch || analysis.Level != domain.FingerprintLevelFull || analysis.Confidence == nil || *analysis.Confidence != confidence || len(analysis.Candidates) != 1 || len(analysis.Answers) != 3 {
		t.Fatalf("analysis = %+v", analysis)
	}
	if bank.expected != "gpt-5.5" || len(bank.answers) != 3 || bank.answers[2] != (fingerprint.Answer{Text: "answer 2: 12, 300, 45", ExpectedCount: 302}) || bank.sources[0] != "" {
		t.Fatalf("bank judged %+v for %q from %q", bank.answers, bank.expected, bank.sources)
	}

	for answers := 1; answers <= 2; answers++ {
		if w = request(t, handler, "POST", "/api/v1/public/fingerprint/analyze", fingerprintRound(answers), nil, ""); w.Code != 200 || len(bank.answers) != answers {
			t.Fatalf("a round of %d answers: %d %s", answers, w.Code, w.Body.String())
		}
	}
}

func TestFingerprintAnalyzeRejectsInvalidRounds(t *testing.T) {
	s, handler := testServer(t)
	bank := &fakeFingerprintBank{}
	s.Fingerprint = bank
	settings := domain.DefaultSettings()
	settings.FingerprintManualPerMinute = 120
	if err := s.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*domain.FingerprintAnalyzeRequest)
		code   string
	}{
		{"no answers", func(b *domain.FingerprintAnalyzeRequest) { b.Answers = nil }, "invalid_answers"},
		{"four answers", func(b *domain.FingerprintAnalyzeRequest) { *b = fingerprintRound(4) }, "invalid_answers"},
		{"count below the range", func(b *domain.FingerprintAnalyzeRequest) {
			b.Answers[0].ExpectedCount = fingerprint.MinExpectedCount - 1
		}, "invalid_answers"},
		{"count above the range", func(b *domain.FingerprintAnalyzeRequest) {
			b.Answers[1].ExpectedCount = fingerprint.MaxExpectedCount + 1
		}, "invalid_answers"},
		{"missing count", func(b *domain.FingerprintAnalyzeRequest) { b.Answers[0].ExpectedCount = 0 }, "invalid_answers"},
		{"answer too long", func(b *domain.FingerprintAnalyzeRequest) {
			b.Answers[0].Text = strings.Repeat("1 ", fingerprintAnswerBytes/2+1)
		}, "answer_too_long"},
		{"model too long", func(b *domain.FingerprintAnalyzeRequest) { b.ExpectedModel = strings.Repeat("m", 201) }, "invalid_model"},
	}
	for _, tc := range cases {
		round := fingerprintRound(2)
		tc.change(&round)
		w := request(t, handler, "POST", "/api/v1/public/fingerprint/analyze", round, nil, "")
		if w.Code != 400 || errorCode(t, w) != tc.code {
			t.Errorf("%s: %d %s, want 400 %s", tc.name, w.Code, w.Body.String(), tc.code)
		}
	}
	if w := request(t, handler, "POST", "/api/v1/public/fingerprint/analyze", "not an object", nil, ""); w.Code != 400 {
		t.Errorf("a body that is not an object: %d %s", w.Code, w.Body.String())
	}
	if bank.judged != 0 {
		t.Fatalf("the bank judged %d invalid rounds", bank.judged)
	}

	longest := fingerprintRound(1)
	longest.Answers[0].Text = strings.Repeat("1 ", fingerprintAnswerBytes/2)
	if w := request(t, handler, "POST", "/api/v1/public/fingerprint/analyze", longest, nil, ""); w.Code != 200 || bank.judged != 1 {
		t.Fatalf("an answer of exactly the size limit: %d %s", w.Code, w.Body.String())
	}
}

func TestFingerprintAnalyzeWithoutUsableBank(t *testing.T) {
	s, handler := testServer(t)
	w := request(t, handler, "POST", "/api/v1/public/fingerprint/analyze", fingerprintRound(3), nil, "")
	if w.Code != 503 || errorCode(t, w) != "fingerprint_unavailable" || w.Header().Get("Retry-After") == "" {
		t.Fatalf("no bank: %d %s", w.Code, w.Body.String())
	}

	bank := &fakeFingerprintBank{err: fmt.Errorf("%w: dial tcp 10.1.2.3:443: connection refused", fingerprint.ErrBankUnavailable)}
	s.Fingerprint = bank
	w = request(t, handler, "POST", "/api/v1/public/fingerprint/analyze", fingerprintRound(3), nil, "")
	if w.Code != 503 || errorCode(t, w) != "fingerprint_unavailable" || strings.Contains(w.Body.String(), "10.1.2.3") {
		t.Fatalf("bank unavailable: %d %s", w.Code, w.Body.String())
	}

	bank.err = errors.New("scoring broke at D:\\secret")
	w = request(t, handler, "POST", "/api/v1/public/fingerprint/analyze", fingerprintRound(3), nil, "")
	if w.Code != 500 || errorCode(t, w) != "fingerprint_failed" || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("scoring failure: %d %s", w.Code, w.Body.String())
	}
}

func TestFingerprintAnalyzeRateLimit(t *testing.T) {
	s, handler := testServer(t)
	bank := &fakeFingerprintBank{}
	s.Fingerprint = bank
	settings := domain.DefaultSettings()
	settings.FingerprintManualPerMinute = 3
	if err := s.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}

	analyze := func(body any, address string) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/public/fingerprint/analyze", strings.NewReader(string(payload)))
		req.RemoteAddr, req.Host = address, "localhost"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://localhost")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	if w := analyze(fingerprintRound(3), "203.0.113.7:1000"); w.Code != 200 {
		t.Fatalf("first round: %d %s", w.Code, w.Body.String())
	}
	// An invalid round uses up the budget like a valid one, so it cannot be used to probe without limit.
	if w := analyze(domain.FingerprintAnalyzeRequest{}, "203.0.113.7:1001"); w.Code != 400 {
		t.Fatalf("invalid round: %d %s", w.Code, w.Body.String())
	}
	if w := analyze(fingerprintRound(1), "203.0.113.7:1002"); w.Code != 200 {
		t.Fatalf("third round: %d %s", w.Code, w.Body.String())
	}
	w := analyze(fingerprintRound(3), "203.0.113.7:1003")
	if w.Code != 429 || errorCode(t, w) != "rate_limited" || w.Header().Get("Retry-After") != "60" {
		t.Fatalf("fourth round within a minute: %d %s", w.Code, w.Body.String())
	}
	if bank.judged != 2 {
		t.Fatalf("the bank judged %d rounds, want 2", bank.judged)
	}
	if w = analyze(fingerprintRound(3), "203.0.113.8:1000"); w.Code != 200 {
		t.Fatalf("another visitor shares the budget: %d %s", w.Code, w.Body.String())
	}
	// Fetching questions and the page data is not limited by the scoring budget.
	if w = request(t, handler, "GET", "/api/v1/public/fingerprint/challenges", nil, nil, ""); w.Code != 200 {
		t.Fatalf("challenges: %d %s", w.Code, w.Body.String())
	}
}

func TestFingerprintSettings(t *testing.T) {
	s, handler := testServer(t)
	cookie, csrf := loginForTest(t, handler)
	ctx := context.Background()

	defaults, err := s.Store.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.FingerprintConcurrency != 1 || defaults.FingerprintIntervalSeconds != 86400 || defaults.FingerprintManualPerMinute != 10 || defaults.FingerprintAPIPerHour != 6 || defaults.FingerprintBankDir != "" {
		t.Fatalf("default fingerprint settings = %+v", defaults)
	}

	// A client from before the fingerprint feature sends none of the fields.
	old := defaults
	old.FingerprintConcurrency, old.FingerprintIntervalSeconds, old.FingerprintManualPerMinute, old.FingerprintAPIPerHour = 0, 0, 0, 0
	if w := request(t, handler, "PUT", "/api/v1/admin/settings", old, cookie, csrf); w.Code != 200 {
		t.Fatalf("settings without fingerprint fields: %d %s", w.Code, w.Body.String())
	}
	if saved, _ := s.Store.GetSettings(ctx); saved != defaults {
		t.Fatalf("settings without fingerprint fields saved as %+v, want %+v", saved, defaults)
	}

	dir := t.TempDir()
	changed := defaults
	changed.FingerprintConcurrency, changed.FingerprintIntervalSeconds, changed.FingerprintManualPerMinute, changed.FingerprintAPIPerHour = 8, 3600, 120, 60
	changed.FingerprintBankDir = "  " + dir + " "
	if w := request(t, handler, "PUT", "/api/v1/admin/settings", changed, cookie, csrf); w.Code != 200 {
		t.Fatalf("fingerprint settings: %d %s", w.Code, w.Body.String())
	}
	changed.FingerprintBankDir = dir
	if saved, _ := s.Store.GetSettings(ctx); saved != changed {
		t.Fatalf("fingerprint settings saved as %+v, want %+v", saved, changed)
	}

	bank := &fakeFingerprintBank{}
	s.Fingerprint = bank
	if w := request(t, handler, "GET", "/api/v1/public/fingerprint", nil, nil, ""); w.Code != 200 || len(bank.sources) != 1 || bank.sources[0] != dir {
		t.Fatalf("the configured bank directory is not used: %d %q", w.Code, bank.sources)
	}

	invalid := []func(*domain.Settings){
		func(v *domain.Settings) { v.FingerprintConcurrency = 9 },
		func(v *domain.Settings) { v.FingerprintConcurrency = -1 },
		func(v *domain.Settings) { v.FingerprintIntervalSeconds = 3599 },
		func(v *domain.Settings) { v.FingerprintIntervalSeconds = 2592001 },
		func(v *domain.Settings) { v.FingerprintManualPerMinute = 121 },
		func(v *domain.Settings) { v.FingerprintAPIPerHour = 61 },
		func(v *domain.Settings) { v.FingerprintBankDir = "bank" },
		func(v *domain.Settings) { v.FingerprintBankDir = "https://lm.ikale.io/data/" },
		func(v *domain.Settings) { v.FingerprintBankDir = dir + strings.Repeat("x", 1024) },
	}
	for i, change := range invalid {
		value := changed
		change(&value)
		if w := request(t, handler, "PUT", "/api/v1/admin/settings", value, cookie, csrf); w.Code != 400 || errorCode(t, w) != "invalid_settings" {
			t.Errorf("invalid fingerprint settings %d accepted: %d %s", i, w.Code, w.Body.String())
		}
	}
	if saved, _ := s.Store.GetSettings(ctx); saved != changed {
		t.Fatalf("a rejected update changed the settings to %+v", saved)
	}
}
