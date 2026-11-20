package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/fingerprint"
	"relaylab/internal/upstream"
)

const (
	fingerprintCheckPath = "/api/v1/test/fingerprint"
	// memberConnectionKey is the key of the connection that privateConnectionForTest saves.
	memberConnectionKey = "sk-member-private-very-secret-key"
)

// checkNumbers is a reply of count numbers.
func checkNumbers(count int) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = strconv.Itoa(1 + (i*97)%355)
	}
	return strings.Join(parts, ", ")
}

// chatReply is a complete chat response of model whose reply is text.
func chatReply(model, text string) *http.Response {
	body, _ := json.Marshal(map[string]any{"model": model, "choices": []any{map[string]any{"message": map[string]any{"content": text}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 3, "completion_tokens": 5, "total_tokens": 8}})
	return visitorResponse(string(body), "application/json")
}

func failedReply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

// askedRequest is one request that the upstream of a member connection received.
type askedRequest struct {
	url    string
	header http.Header
	body   map[string]any
}

// prompt returns the question the request asks.
func (r askedRequest) prompt() string {
	messages, _ := r.body["messages"].([]any)
	if len(messages) != 1 {
		return ""
	}
	content, _ := messages[0].(map[string]any)["content"].(string)
	return content
}

var digits = regexp.MustCompile(`\d+`)

// asks returns how many numbers the question of the request asks for, or 0 when it names no count of a question.
func (r askedRequest) asks() int {
	for _, found := range digits.FindAllString(r.prompt(), -1) {
		if n, _ := strconv.Atoi(found); n >= fingerprint.MinExpectedCount && n <= fingerprint.MaxExpectedCount {
			return n
		}
	}
	return 0
}

// memberUpstream plays the upstream of a member connection. respond answers each request and is told the 1-based
// position in which the request arrived.
type memberUpstream struct {
	mu       sync.Mutex
	requests []askedRequest
	respond  func(n int, r askedRequest) *http.Response
}

func (u *memberUpstream) Do(r *http.Request) (*http.Response, error) {
	asked := askedRequest{url: r.URL.String(), header: r.Header.Clone()}
	_ = json.NewDecoder(r.Body).Decode(&asked.body)
	u.mu.Lock()
	u.requests = append(u.requests, asked)
	n, respond := len(u.requests), u.respond
	u.mu.Unlock()
	return respond(n, asked), nil
}

func (u *memberUpstream) all() []askedRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]askedRequest(nil), u.requests...)
}

// answered replies to a question with a hundred numbers fewer than it asks for and reports a model named after the
// question, so that every question of a round has a reply of its own.
func answered(_ int, r askedRequest) *http.Response {
	return chatReply(returnedModel(r.asks()), checkNumbers(r.asks()-100))
}

// returnedModel is the model that answered reports for the question that asks for count numbers.
func returnedModel(count int) string { return "returned-" + strconv.Itoa(count) }

// checkMember is a member with a saved connection.
type checkMember struct {
	cookie     *http.Cookie
	csrf       string
	member     domain.Member
	connection domain.UserConnection
}

// body is a valid request for a round through the connection of the member.
func (m checkMember) body() map[string]any {
	return map[string]any{"connection_id": m.connection.ID, "connection_revision": m.connection.Revision, "model": "claude-sonnet-4-6"}
}

// checkFixture is a server with a ready reference bank and a member whose connection reaches upstream.
type checkFixture struct {
	checkMember
	s        *Server
	bank     *fakeFingerprintBank
	upstream *memberUpstream
}

func fingerprintCheckFixture(t *testing.T) *checkFixture {
	t.Helper()
	s, handler := testServer(t)
	f := &checkFixture{s: s, bank: readyFingerprintBank(), upstream: &memberUpstream{respond: answered}}
	f.setSettings(t, func(*domain.Settings) {})
	confidence := 0.93
	f.bank.analysis = domain.FingerprintAnalysis{
		Verdict: domain.FingerprintMatch, Level: domain.FingerprintLevelFull, ValidAnswers: 3, ExpectedRank: 1, Confidence: &confidence,
		Expected: &domain.FingerprintModel{ID: "claude-sonnet-4.6", DisplayName: "claude-sonnet-4.6", Family: "claude", FamilyName: "Claude"},
	}
	f.bank.expect = func(m domain.Model) string {
		switch m.FingerprintMode {
		case domain.FingerprintModeManual:
			return m.FingerprintModel
		case domain.FingerprintModeUnlisted:
			return ""
		}
		return "claude-sonnet-4.6"
	}
	s.Fingerprint, s.Client = f.bank, upstream.NewTestClient(f.upstream)
	f.checkMember = f.join(t, handler, "fingerprint-member")
	return f
}

// setSettings saves the default settings with change applied. The per-minute limit that the endpoint shares with
// online tests is lifted, so that only a test that sets it meets it.
func (f *checkFixture) setSettings(t *testing.T, change func(*domain.Settings)) {
	t.Helper()
	settings := domain.DefaultSettings()
	settings.VisitorGenerationsPerMinute = 1000
	change(&settings)
	if err := f.s.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
}

// join registers a member and saves a connection for them.
func (f *checkFixture) join(t *testing.T, handler http.Handler, username string) checkMember {
	t.Helper()
	cookie, csrf, member := registerMemberForAuthTest(t, handler, username)
	return checkMember{cookie: cookie, csrf: csrf, member: member, connection: privateConnectionForTest(t, f.s, cookie, csrf)}
}

func (f *checkFixture) check(t *testing.T, m checkMember, body any) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, f.s.Handler(), http.MethodPost, fingerprintCheckPath, body, m.cookie, m.csrf)
}

// round runs a round that must succeed and returns its outcome.
func (f *checkFixture) round(t *testing.T, body any) domain.FingerprintCheck {
	t.Helper()
	w := f.check(t, f.checkMember, body)
	var check domain.FingerprintCheck
	if err := json.Unmarshal(w.Body.Bytes(), &check); err != nil || w.Code != 200 {
		t.Fatalf("check: %d %s %v", w.Code, w.Body.String(), err)
	}
	if strings.Contains(w.Body.String(), memberConnectionKey) || !strings.Contains(w.Body.String(), `"errors":[`) {
		t.Fatalf("check %s; want no key and a list of errors", w.Body.String())
	}
	return check
}

func TestFingerprintCheckAsksTheQuestionsAtOnceAndScoresTheirReplies(t *testing.T) {
	f := fingerprintCheckFixture(t)
	var mu sync.Mutex
	waiting, together := 0, make(chan struct{})
	f.upstream.respond = func(n int, r askedRequest) *http.Response {
		mu.Lock()
		if waiting++; waiting == fingerprint.MaxChallenges {
			close(together)
		}
		mu.Unlock()
		select {
		case <-together:
		case <-time.After(5 * time.Second):
			t.Error("a question waited for the reply to another one")
		}
		return answered(n, r)
	}
	started := time.Now()
	check := f.round(t, f.body())

	requests := f.upstream.all()
	if len(requests) != 3 {
		t.Fatalf("%d requests, want one per question", len(requests))
	}
	prompts := map[string]bool{}
	for _, r := range requests {
		prompts[r.prompt()] = true
		if r.url != "https://public.example/v1/chat/completions" || r.header.Get("Authorization") != "Bearer "+memberConnectionKey ||
			r.body["model"] != "claude-sonnet-4-6" || r.body["max_tokens"] != float64(8192) || r.asks() == 0 {
			t.Fatalf("request to %s with %v, want a question sent to the connection of the member", r.url, r.body)
		}
	}
	if len(prompts) != 3 {
		t.Fatalf("%d different questions, want 3", len(prompts))
	}

	if len(f.bank.asked) != 1 || f.bank.asked[0].UpstreamID != "claude-sonnet-4-6" || f.bank.asked[0].FingerprintMode != domain.FingerprintModeAuto || f.bank.asked[0].FingerprintModel != "" {
		t.Fatalf("the bank was asked for %+v, want the reference model that the model name refers to", f.bank.asked)
	}
	if f.bank.judged != 1 || f.bank.expected != "claude-sonnet-4.6" || len(f.bank.answers) != 3 {
		t.Fatalf("the bank judged %d times, %d answers against %q", f.bank.judged, len(f.bank.answers), f.bank.expected)
	}
	counts := map[int]bool{}
	for i, answer := range f.bank.answers {
		counts[answer.ExpectedCount] = true
		if answer.Text != checkNumbers(answer.ExpectedCount-100) {
			t.Fatalf("answer %d for %d numbers is not the reply to its own question", i+1, answer.ExpectedCount)
		}
	}
	if len(counts) != 3 {
		t.Fatalf("answers for the counts %v, want one per question", counts)
	}

	if check.Questions != 3 || check.Analysis.Verdict != domain.FingerprintMatch || check.Analysis.Confidence == nil || *check.Analysis.Confidence != 0.93 || check.Analysis.Expected == nil {
		t.Fatalf("check %+v, want the analysis of the bank", check)
	}
	if len(check.Errors) != 0 || len(check.QuestionMS) != 3 || check.ReturnedModel != returnedModel(f.bank.answers[0].ExpectedCount) || check.DurationMS < 0 || check.CreatedAt.Before(started.Add(-time.Second)) {
		t.Fatalf("check %+v", check)
	}
	if usage := check.Usage; usage.InputTokens == nil || *usage.InputTokens != 9 || usage.OutputTokens == nil || *usage.OutputTokens != 15 || usage.TotalTokens == nil || *usage.TotalTokens != 24 {
		t.Fatalf("usage %+v, want the three requests added up", usage)
	}

	ctx := context.Background()
	rounds, err := f.s.Store.ListLatestFingerprintResults(ctx, 3)
	if err != nil || len(rounds) != 0 {
		t.Fatalf("%d stored rounds, %v; want the outcome returned and not stored", len(rounds), err)
	}
	records, err := f.s.Store.ListOnlineTestRuns(ctx, domain.OnlineTestFilter{})
	if err != nil || records.Total != 0 || len(records.Items) != 0 {
		t.Fatalf("%d test records, %v; want none", records.Total, err)
	}
}

func TestFingerprintCheckIsScoredOnTheRepliesThatArrived(t *testing.T) {
	f := fingerprintCheckFixture(t)
	f.upstream.respond = func(n int, r askedRequest) *http.Response {
		switch n {
		case 2:
			return failedReply(429, `{"error":{"message":"quota used up for `+memberConnectionKey+`"}}`)
		case 3:
			return failedReply(502, "down")
		}
		return answered(n, r)
	}
	check := f.round(t, f.body())

	// The requests arrive in any order, so a question is told apart by the count it asks for.
	requests := f.upstream.all()
	reasons := map[int]domain.FingerprintRequestError{
		requests[1].asks(): {Code: "rate_limited", Message: "quota used up for [redacted]"},
		requests[2].asks(): {Code: "upstream_unavailable", Message: "Upstream returned HTTP 502"},
	}
	if len(check.Errors) != 2 || check.Errors[0].Question >= check.Errors[1].Question {
		t.Fatalf("errors %+v, want the two failed questions in their order", check.Errors)
	}
	for _, failure := range check.Errors {
		if failure.Question < 1 || failure.Question > 3 {
			t.Fatalf("error %+v names no question of the round", failure)
		}
		want := reasons[f.bank.answers[failure.Question-1].ExpectedCount]
		if want.Question = failure.Question; failure != want {
			t.Fatalf("error %+v, want %+v: the reason the connection gave for that question, without the key", failure, want)
		}
	}
	for i, answer := range f.bank.answers {
		want := checkNumbers(answer.ExpectedCount - 100)
		if _, failed := reasons[answer.ExpectedCount]; failed {
			want = ""
		}
		if answer.Text != want {
			t.Fatalf("answer %d holds %d characters, want %d", i+1, len(answer.Text), len(want))
		}
	}
	if check.Questions != 3 || check.ReturnedModel != returnedModel(requests[0].asks()) || check.Usage.TotalTokens == nil || *check.Usage.TotalTokens != 8 {
		t.Fatalf("check %+v, want three questions and the model and usage of the answered one", check)
	}

	f.upstream.respond = func(int, askedRequest) *http.Response { return failedReply(502, "down") }
	f.bank.analysis = domain.FingerprintAnalysis{Verdict: domain.FingerprintUndetermined, Reason: domain.FingerprintNoValidAnswers}
	check = f.round(t, f.body())
	if check.Analysis.Verdict != domain.FingerprintUndetermined || len(check.Errors) != 3 || check.ReturnedModel != "" || check.Usage.TotalTokens != nil {
		t.Fatalf("check %+v, want an undetermined round that reports every question", check)
	}
	for i, failure := range check.Errors {
		if failure != (domain.FingerprintRequestError{Question: i + 1, Code: "upstream_unavailable", Message: "Upstream returned HTTP 502"}) || f.bank.answers[i].Text != "" {
			t.Fatalf("error %d is %+v with the answer %q", i+1, failure, f.bank.answers[i].Text)
		}
	}

	f.upstream.respond = func(int, askedRequest) *http.Response {
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": checkNumbers(50)}, "finish_reason": "length"}}})
		return visitorResponse(string(body), "application/json")
	}
	body := f.body()
	body["questions"] = 1
	check = f.round(t, body)
	want := domain.FingerprintRequestError{Question: 1, Code: "truncated", Message: "上游输出达到本次 8192 Tokens 上限后被截断；模型可能先思考了较长时间"}
	if len(check.Errors) != 1 || check.Errors[0] != want || len(f.bank.answers) != 1 || f.bank.answers[0].Text != checkNumbers(50) {
		t.Fatalf("errors %+v, answers %+v; want the cut reply scored and the limit named", check.Errors, f.bank.answers)
	}
}

func TestFingerprintCheckFollowsTheRequestedModeAndQuestionCount(t *testing.T) {
	f := fingerprintCheckFixture(t)
	for _, tc := range []struct {
		name      string
		change    map[string]any
		asked     domain.Model
		expected  string
		questions int
	}{
		{"the mode and the count left out", map[string]any{"model": "  gpt-5.5-2026  "}, domain.Model{UpstreamID: "gpt-5.5-2026", FingerprintMode: "auto"}, "claude-sonnet-4.6", 3},
		{"a picked reference model", map[string]any{"mode": "manual", "expected_model": " gpt-5.5 ", "questions": 2}, domain.Model{UpstreamID: "claude-sonnet-4-6", FingerprintMode: "manual", FingerprintModel: "gpt-5.5"}, "gpt-5.5", 2},
		{"a model the bank does not list", map[string]any{"mode": "unlisted", "expected_model": "gpt-5.5", "questions": 1}, domain.Model{UpstreamID: "claude-sonnet-4-6", FingerprintMode: "unlisted"}, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(f.upstream.all())
			body := f.body()
			for key, value := range tc.change {
				body[key] = value
			}
			check := f.round(t, body)
			requests := f.upstream.all()[before:]
			if check.Questions != tc.questions || len(check.QuestionMS) != tc.questions || len(requests) != tc.questions || len(f.bank.answers) != tc.questions {
				t.Fatalf("%d questions, %d requests, %d answers; want %d", check.Questions, len(requests), len(f.bank.answers), tc.questions)
			}
			if asked := f.bank.asked[len(f.bank.asked)-1]; asked != tc.asked || f.bank.expected != tc.expected || requests[0].body["model"] != tc.asked.UpstreamID {
				t.Fatalf("asked the bank for %+v and judged against %q, sent the model %v", asked, f.bank.expected, requests[0].body["model"])
			}
		})
	}
}

func TestFingerprintCheckUsesTheProtocolOfTheConnection(t *testing.T) {
	f := fingerprintCheckFixture(t)
	const key = "sk-ant-member-claude-secret-key"
	w := visitorPOSTWithMember(t, f.s, "/api/v1/user/connections", map[string]any{"name": "Claude", "base_url": "https://claude.example/v1", "api_key": key, "protocol": "anthropic"}, "", f.cookie, f.csrf)
	var connection domain.UserConnection
	if err := json.Unmarshal(w.Body.Bytes(), &connection); err != nil || w.Code != 200 {
		t.Fatalf("connection: %d %s %v", w.Code, w.Body.String(), err)
	}
	f.upstream.respond = func(_ int, r askedRequest) *http.Response {
		body, _ := json.Marshal(map[string]any{"type": "message", "model": "claude-returned", "content": []any{map[string]string{"type": "text", "text": checkNumbers(r.asks() - 100)}}, "stop_reason": "end_turn"})
		return visitorResponse(string(body), "application/json")
	}
	check := f.round(t, map[string]any{"connection_id": connection.ID, "connection_revision": connection.Revision, "model": "claude-sonnet-4-6"})

	requests := f.upstream.all()
	if len(requests) != 3 || len(check.Errors) != 0 || check.ReturnedModel != "claude-returned" {
		t.Fatalf("%d requests, check %+v", len(requests), check)
	}
	for i, r := range requests {
		if r.url != "https://claude.example/v1/messages" || r.header.Get("x-api-key") != key || r.header.Get("Authorization") != "" || r.body["max_tokens"] != float64(32000) {
			t.Fatalf("request to %s with %v, want a message request with the larger output limit", r.url, r.body)
		}
		if f.bank.answers[i].Text != checkNumbers(f.bank.answers[i].ExpectedCount-100) {
			t.Fatalf("answer %d is not the reply to its question", i+1)
		}
	}
}

func TestFingerprintCheckSpendsTheHourlyBudgetOnlyOnRoundsItSends(t *testing.T) {
	f := fingerprintCheckFixture(t)
	f.setSettings(t, func(settings *domain.Settings) { settings.FingerprintAPIPerHour = 2 })
	other := f.join(t, f.s.Handler(), "other-fingerprint-member")
	ctx := context.Background()
	refused := func(name string, w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || errorCode(t, w) != code || strings.Contains(w.Body.String(), memberConnectionKey) {
			t.Fatalf("%s: %d %s, want %d %s", name, w.Code, w.Body.String(), status, code)
		}
		if sent := len(f.upstream.all()); sent != 0 || f.bank.judged != 0 {
			t.Fatalf("%s: %d requests sent and %d rounds judged, want none", name, sent, f.bank.judged)
		}
	}

	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		status int
		code   string
	}{
		{"no connection", func(b map[string]any) { delete(b, "connection_id") }, 400, "connection_required"},
		{"an unknown connection", func(b map[string]any) { b["connection_id"] = "missing" }, 404, "not_found"},
		{"the connection of another member", func(b map[string]any) { b["connection_id"] = other.connection.ID }, 404, "not_found"},
		{"no revision", func(b map[string]any) { delete(b, "connection_revision") }, 400, "connection_revision_required"},
		{"an outdated revision", func(b map[string]any) { b["connection_revision"] = f.connection.Revision + 1 }, 409, "connection_changed"},
		{"no model", func(b map[string]any) { b["model"] = "  " }, 400, "invalid_model"},
		{"a model name that is too long", func(b map[string]any) { b["model"] = strings.Repeat("m", 257) }, 400, "invalid_model"},
		{"an unknown mode", func(b map[string]any) { b["mode"] = "exact" }, 400, "invalid_fingerprint_settings"},
		{"four questions", func(b map[string]any) { b["questions"] = 4 }, 400, "invalid_fingerprint_settings"},
		{"a negative question count", func(b map[string]any) { b["questions"] = -1 }, 400, "invalid_fingerprint_settings"},
		{"the manual mode without a model", func(b map[string]any) { b["mode"] = "manual" }, 400, "invalid_fingerprint_model"},
		{"the manual mode with a model the bank does not list", func(b map[string]any) { b["mode"], b["expected_model"] = "manual", "gpt-9" }, 400, "invalid_fingerprint_model"},
	} {
		body := f.body()
		tc.change(body)
		refused(tc.name, f.check(t, f.checkMember, body), tc.status, tc.code)
	}
	refused("a body that is not a request", f.check(t, f.checkMember, "round"), 400, "invalid_request")

	if err := f.s.Store.SetUserConnectionDisabled(ctx, f.connection.ID, true); err != nil {
		t.Fatal(err)
	}
	refused("a disabled connection", f.check(t, f.checkMember, f.body()), 403, "connection_disabled")
	if err := f.s.Store.SetUserConnectionDisabled(ctx, f.connection.ID, false); err != nil {
		t.Fatal(err)
	}
	// Switching a connection off and on again gives it a new revision.
	current, err := f.s.Store.GetUserConnection(ctx, f.member.ID, f.connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.connection = *current
	f.bank.expectErr = fingerprint.ErrBankUnavailable
	refused("a bank that cannot be read", f.check(t, f.checkMember, f.body()), 503, "fingerprint_unavailable")
	f.bank.expectErr = nil
	f.s.Fingerprint = nil
	refused("no bank", f.check(t, f.checkMember, f.body()), 503, "fingerprint_unavailable")
	f.s.Fingerprint = f.bank

	if w := request(t, f.s.Handler(), http.MethodPost, fingerprintCheckPath, f.body(), nil, ""); w.Code != 401 {
		t.Fatalf("a visitor: %d %s, want 401", w.Code, w.Body.String())
	}
	if w := request(t, f.s.Handler(), http.MethodPost, fingerprintCheckPath, f.body(), f.cookie, ""); w.Code != 403 {
		t.Fatalf("a member without the CSRF token: %d %s, want 403", w.Code, w.Body.String())
	}
	if w := visitorPOSTWithMember(t, f.s, fingerprintCheckPath, f.body(), "https://elsewhere.example", f.cookie, f.csrf); w.Code != 403 {
		t.Fatalf("a request from another origin: %d %s, want 403", w.Code, w.Body.String())
	}
	if sent := len(f.upstream.all()); sent != 0 {
		t.Fatalf("%d requests sent for refused rounds", sent)
	}

	// A round whose requests all fail was sent, so it counts.
	f.upstream.respond = func(int, askedRequest) *http.Response { return failedReply(502, "down") }
	f.round(t, f.body())
	f.upstream.respond = answered
	f.round(t, f.body())
	w := f.check(t, f.checkMember, f.body())
	wait, _ := strconv.Atoi(w.Header().Get("Retry-After"))
	if w.Code != 429 || errorCode(t, w) != "rate_limited" || wait < 3590 || wait > 3600 || !strings.Contains(errorMessage(t, w), "请 60 分钟后再试") {
		t.Fatalf("a third round within the hour: %d %s, Retry-After %q", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
	}
	if sent := len(f.upstream.all()); sent != 6 || f.bank.judged != 2 {
		t.Fatalf("%d requests and %d judged rounds, want the refused round to send nothing", sent, f.bank.judged)
	}

	if w = f.check(t, other, other.body()); w.Code != 200 {
		t.Fatalf("the first round of another member: %d %s", w.Code, w.Body.String())
	}

	bucket := f.s.rates["fingerprint-api:"+f.member.ID]
	bucket.Start = bucket.Start.Add(-59 * time.Minute)
	w = f.check(t, f.checkMember, f.body())
	if wait, _ = strconv.Atoi(w.Header().Get("Retry-After")); w.Code != 429 || wait < 50 || wait > 60 || !strings.Contains(errorMessage(t, w), "请 1 分钟后再试") {
		t.Fatalf("a round in the last minute of the hour: %d %s, Retry-After %q", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
	}
	bucket.Start = bucket.Start.Add(-time.Minute)
	f.round(t, f.body())
	f.round(t, f.body())
	if w = f.check(t, f.checkMember, f.body()); w.Code != 429 {
		t.Fatalf("a third round in the next hour: %d %s", w.Code, w.Body.String())
	}
}

func TestFingerprintCheckSharesThePerMinuteLimitOfOnlineTests(t *testing.T) {
	f := fingerprintCheckFixture(t)
	f.setSettings(t, func(settings *domain.Settings) { settings.VisitorGenerationsPerMinute = 2 })
	f.round(t, f.body())
	if w := f.check(t, f.checkMember, map[string]any{}); w.Code != 400 {
		t.Fatalf("an empty request: %d %s", w.Code, w.Body.String())
	}
	w := f.check(t, f.checkMember, f.body())
	if w.Code != 429 || errorCode(t, w) != "rate_limited" || w.Header().Get("Retry-After") != "60" {
		t.Fatalf("a third request within the minute: %d %s", w.Code, w.Body.String())
	}
	if bucket := f.s.rates["fingerprint-api:"+f.member.ID]; bucket == nil || bucket.Count != 1 || len(f.upstream.all()) != 3 {
		t.Fatalf("hourly budget %+v after one round that was sent", bucket)
	}
}

func TestFingerprintFailureGivesTheReasonWithoutTheKey(t *testing.T) {
	// A key that does not look like one is still the key of the member.
	const key = "plain-member-token-0001"
	for _, tc := range []struct {
		name    string
		err     error
		code    string
		message string
	}{
		{"a refusal that repeats the key", &upstream.Error{Code: "upstream_http", Message: "refused the token " + key}, "upstream_http", "refused the token [redacted]"},
		{"a reply cut at the output limit", &upstream.Error{Code: "truncated", Message: "Upstream output was truncated"}, "truncated", "上游输出达到本次 32000 Tokens 上限后被截断；模型可能先思考了较长时间"},
		{"an error without a code", &upstream.Error{Message: "closed"}, "upstream_error", "closed"},
		{"an error that did not come from upstream", errors.New("dial " + key), "upstream_error", "上游请求失败，请稍后重试"},
	} {
		if code, message := fingerprintFailure(tc.err, key, 32000); code != tc.code || message != tc.message {
			t.Fatalf("%s: %s %q, want %s %q", tc.name, code, message, tc.code, tc.message)
		}
	}
}

func TestFingerprintCheckReportsAScoringFailure(t *testing.T) {
	f := fingerprintCheckFixture(t)
	f.bank.err = fingerprint.ErrBankUnavailable
	if w := f.check(t, f.checkMember, f.body()); w.Code != 503 || errorCode(t, w) != "fingerprint_unavailable" || w.Header().Get("Retry-After") == "" {
		t.Fatalf("a bank that went away: %d %s", w.Code, w.Body.String())
	}
	f.bank.err = errors.New("scoring broke")
	if w := f.check(t, f.checkMember, f.body()); w.Code != 500 || errorCode(t, w) != "fingerprint_failed" || strings.Contains(w.Body.String(), "scoring broke") {
		t.Fatalf("a failed scoring: %d %s", w.Code, w.Body.String())
	}
}
