package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"relaylab/internal/domain"
)

// firstFingerprintRound is when the oldest round that storeFingerprintRounds stores for a model was made.
var firstFingerprintRound = time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)

// storeFingerprintRound stores round for the model the way the worker does and returns the ID of its job.
func storeFingerprintRound(t *testing.T, s *Server, modelID string, round domain.FingerprintResult) string {
	t.Helper()
	if _, err := s.Store.EnqueueJob(context.Background(), domain.Job{ModelID: modelID, Kind: "fingerprint"}); err != nil {
		t.Fatal(err)
	}
	return finishFingerprintJob(t, s, round).ID
}

// storeFingerprintRounds stores one round of the model for every verdict, oldest first and a minute apart. A verdict
// is written as "verdict/level". The rounds are named after the model: <model>-1 is the oldest.
func storeFingerprintRounds(t *testing.T, s *Server, modelID string, verdicts ...string) {
	t.Helper()
	for i, v := range verdicts {
		verdict, level, _ := strings.Cut(v, "/")
		storeFingerprintRound(t, s, modelID, domain.FingerprintResult{ID: fmt.Sprintf("%s-%d", modelID, i+1), Questions: 3,
			Analysis: domain.FingerprintAnalysis{Verdict: verdict, Level: level}, CreatedAt: firstFingerprintRound.Add(time.Duration(i) * time.Minute)})
	}
}

func publicFingerprintResults(t *testing.T, s *Server) (domain.FingerprintPublicResults, string) {
	t.Helper()
	w := publicGET(t, s, "/api/v1/public/fingerprint/results")
	var out domain.FingerprintPublicResults
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("results: %d %s %v", w.Code, w.Body.String(), err)
	}
	return out, w.Body.String()
}

func publicFingerprintRounds(t *testing.T, s *Server, modelID string) ([]domain.FingerprintResult, string) {
	t.Helper()
	w := publicGET(t, s, "/api/v1/public/fingerprint/models/"+modelID+"/results")
	var out []domain.FingerprintResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("rounds of %s: %d %s %v", modelID, w.Code, w.Body.String(), err)
	}
	return out, w.Body.String()
}

func roundIDs(rounds []domain.FingerprintResult) string {
	ids := make([]string, len(rounds))
	for i, round := range rounds {
		ids[i] = round.ID
	}
	return strings.Join(ids, " ")
}

// hiddenFingerprintModels adds, beside the public model of the fixture, one model for every way a model is kept from
// visitors, stores a round for each of them and returns their IDs.
func hiddenFingerprintModels(t *testing.T, s *Server, p domain.Platform, ch domain.Channel) []string {
	t.Helper()
	ctx := context.Background()
	platforms := map[string]*domain.Platform{
		"hidden":   {ID: "hidden-platform", Name: "Hidden Platform", Public: true},
		"archived": {ID: "archived-platform", Name: "Archived Platform", Public: true},
	}
	channel := func(id, platformID string) *domain.Channel {
		return &domain.Channel{ID: id, PlatformID: platformID, Name: id + " name", Public: true, Enabled: true, Protocol: "chat", EncryptedKey: "encrypted-key-private", BaseURL: "https://internal-upstream-private.example/v1"}
	}
	channels := map[string]*domain.Channel{
		"hidden":               channel("hidden-channel", p.ID),
		"archived":             channel("archived-channel", p.ID),
		"of hidden platform":   channel("channel-of-hidden-platform", "hidden-platform"),
		"of archived platform": channel("channel-of-archived-platform", "archived-platform"),
	}
	model := func(id, channelID string) *domain.Model {
		return &domain.Model{ID: id, ChannelID: channelID, UpstreamID: id + "-upstream", DisplayName: id + " name", Public: true}
	}
	models := map[string]*domain.Model{
		"hidden":               model("hidden-model", ch.ID),
		"archived":             model("archived-model", ch.ID),
		"of hidden channel":    model("model-of-hidden-channel", "hidden-channel"),
		"of archived channel":  model("model-of-archived-channel", "archived-channel"),
		"of hidden platform":   model("model-of-hidden-platform", "channel-of-hidden-platform"),
		"of archived platform": model("model-of-archived-platform", "channel-of-archived-platform"),
	}
	save := func() {
		t.Helper()
		for _, platform := range platforms {
			if err := s.Store.SavePlatform(ctx, *platform); err != nil {
				t.Fatal(err)
			}
		}
		for _, c := range channels {
			if err := s.Store.SaveChannel(ctx, *c); err != nil {
				t.Fatal(err)
			}
		}
		for _, m := range models {
			if err := s.Store.SaveModel(ctx, *m); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A round can only be stored for a model that can run, so every model gets its round before it is hidden.
	save()
	var ids []string
	for _, m := range models {
		storeFingerprintRounds(t, s, m.ID, "match/full")
		ids = append(ids, m.ID)
	}
	platforms["hidden"].Public, platforms["archived"].Archived = false, true
	channels["hidden"].Public, channels["archived"].Archived = false, true
	models["hidden"].Public, models["archived"].Archived = false, true
	save()
	return ids
}

func TestPublicFingerprintResultsListThePublicModelsInTheOrderOfTheMonitorPage(t *testing.T) {
	s, p, ch, m := publicFixture(t)
	ctx := context.Background()
	hidden := hiddenFingerprintModels(t, s, p, ch)
	if err := s.Store.SavePlatform(ctx, domain.Platform{ID: "first-platform", Name: "First Platform", Public: true, Sort: -1}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []domain.Channel{
		{ID: "anthropic-channel", PlatformID: "first-platform", Name: "Anthropic Channel", Public: true, Enabled: true, Protocol: "anthropic", Sort: 2, EncryptedKey: "encrypted-key-private", BaseURL: "https://internal-upstream-private.example/v1"},
		{ID: "chat-channel", PlatformID: "first-platform", Name: "Chat Channel", Public: true, Enabled: true, Protocol: "chat", Sort: 1, EncryptedKey: "encrypted-key-private", BaseURL: "https://internal-upstream-private.example/v1"},
	} {
		if err := s.Store.SaveChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	for _, model := range []domain.Model{
		{ID: "late-model", ChannelID: "chat-channel", UpstreamID: "late-model-name", DisplayName: "Late model", Public: true, Sort: 2},
		{ID: "early-model", ChannelID: "chat-channel", UpstreamID: "early-model-name", DisplayName: "Early model", Public: true, Sort: 1, Protocol: "responses"},
		{ID: "claude-model", ChannelID: "anthropic-channel", UpstreamID: "claude-model-name", DisplayName: "Claude model", Public: true},
	} {
		if err := s.Store.SaveModel(ctx, model); err != nil {
			t.Fatal(err)
		}
	}

	results, body := publicFingerprintResults(t, s)
	var listed []string
	for _, row := range results.Models {
		listed = append(listed, row.ID)
	}
	// Platforms, then the channels of a platform, then the models of a channel, each by its sort number.
	if got, want := strings.Join(listed, " "), "early-model late-model claude-model "+m.ID; got != want {
		t.Fatalf("listed %q, want %q", got, want)
	}
	for _, id := range hidden {
		if strings.Contains(body, id) {
			t.Fatalf("%s is not public, but the list names it: %s", id, body)
		}
	}
	early, claude := results.Models[0], results.Models[2]
	if early.UpstreamID != "early-model-name" || early.DisplayName != "Early model" || early.ChannelID != "chat-channel" || early.ChannelName != "Chat Channel" || early.PlatformID != "first-platform" || early.PlatformName != "First Platform" {
		t.Fatalf("row names = %+v", early)
	}
	if early.Protocol != "responses" || results.Models[1].Protocol != "chat" || claude.Protocol != "anthropic" {
		t.Fatalf("protocols = %s %s %s, want the model's own and else that of its channel", early.Protocol, results.Models[1].Protocol, claude.Protocol)
	}
	// A model nobody has detected yet is listed without a conclusion.
	if early.Summary != nil || early.Round != nil || early.CheckedAt != nil || !strings.Contains(body, `"summary":null,"round":null,"checked_at":null`) {
		t.Fatalf("undetected model = %+v in %s", early, body)
	}
	for _, private := range []string{"encrypted-key-private", "key-mask-private", "internal-upstream-private"} {
		if strings.Contains(body, private) {
			t.Fatalf("the list gives away %s: %s", private, body)
		}
	}

	m.Public = false
	if err := s.Store.SaveModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.SavePlatform(ctx, domain.Platform{ID: "first-platform", Name: "First Platform", Sort: -1}); err != nil {
		t.Fatal(err)
	}
	if _, body = publicFingerprintResults(t, s); strings.TrimSpace(body) != `{"models":[]}` {
		t.Fatalf("without a public model the list is empty, got %s", body)
	}
}

func TestPublicFingerprintResultsSummarizeTheNewestThreeRounds(t *testing.T) {
	s, _, _, m := publicFixture(t)
	// Newest first the rounds are undetermined, match, mismatch and mismatch. Only the first three count, so match
	// and mismatch were reached once each, both by a full round, and the newer of the two wins.
	storeFingerprintRounds(t, s, m.ID, "mismatch/full", "mismatch/full", "match/full", "undetermined/")
	results, body := publicFingerprintResults(t, s)
	if len(results.Models) != 1 || results.Models[0].Summary == nil || results.Models[0].Round == nil || results.Models[0].CheckedAt == nil {
		t.Fatalf("results = %s", body)
	}
	row := results.Models[0]
	if got := *row.Summary; got.Verdict != domain.FingerprintMatch || got.Agree != 1 || strings.Join(got.Verdicts, " ") != "undetermined match mismatch" || got.RoundID != m.ID+"-3" {
		t.Fatalf("summary = %+v", got)
	}
	// The round shown is the one the conclusion rests on, which is not the newest here.
	if row.Round.ID != m.ID+"-3" || row.Round.Analysis.Verdict != domain.FingerprintMatch || row.Round.ModelID != m.ID {
		t.Fatalf("round = %+v", row.Round)
	}
	if want := firstFingerprintRound.Add(3 * time.Minute); !row.CheckedAt.Equal(want) {
		t.Fatalf("checked at %s, want the time of the newest round, %s", row.CheckedAt, want)
	}
}

func TestPublicFingerprintModelResultsListTheRoundsOfOnePublicModel(t *testing.T) {
	s, p, ch, m := publicFixture(t)
	if rounds, body := publicFingerprintRounds(t, s, m.ID); len(rounds) != 0 || strings.TrimSpace(body) != "[]" {
		t.Fatalf("a model without rounds: %s", body)
	}
	storeFingerprintRounds(t, s, m.ID, "mismatch/full", "match/reference", "undetermined/", "match/test")
	rounds, _ := publicFingerprintRounds(t, s, m.ID)
	if got, want := roundIDs(rounds), fmt.Sprintf("%[1]s-4 %[1]s-3 %[1]s-2 %[1]s-1", m.ID); got != want {
		t.Fatalf("rounds %q, want every stored round, newest first: %q", got, want)
	}
	if rounds[1].Analysis.Verdict != domain.FingerprintUndetermined || rounds[2].Analysis.Level != domain.FingerprintLevelReference || !rounds[3].CreatedAt.Equal(firstFingerprintRound) {
		t.Fatalf("rounds = %+v", rounds)
	}

	for _, id := range append(hiddenFingerprintModels(t, s, p, ch), "no-such-model") {
		w := publicGET(t, s, "/api/v1/public/fingerprint/models/"+id+"/results")
		if w.Code != 404 || errorCode(t, w) != "not_found" || strings.Contains(w.Body.String(), id+"-1") {
			t.Fatalf("%s is not public, but its rounds answer %d %s", id, w.Code, w.Body.String())
		}
	}
}

func TestPublicFingerprintRoundsKeepUpstreamMessagesAndJobsFromVisitors(t *testing.T) {
	s, _, _, m := publicFixture(t)
	const secret = "quota exceeded for account acct-private-42 at 10.0.0.7"
	job := storeFingerprintRound(t, s, m.ID, domain.FingerprintResult{ID: "failed-round", Questions: 3, ReturnedModel: "gpt-5.5-2026",
		Analysis:  domain.FingerprintAnalysis{Verdict: domain.FingerprintMatch, Level: domain.FingerprintLevelReference, ValidAnswers: 2},
		Errors:    []domain.FingerprintRequestError{{Question: 2, Code: "http_429", Message: secret}},
		CreatedAt: firstFingerprintRound})
	storeFingerprintRound(t, s, m.ID, domain.FingerprintResult{ID: "clean-round", Questions: 3,
		Analysis: domain.FingerprintAnalysis{Verdict: domain.FingerprintUndetermined}, CreatedAt: firstFingerprintRound.Add(time.Minute)})
	check := func(name, body string, round domain.FingerprintResult) {
		t.Helper()
		if strings.Contains(body, "acct-private-42") || strings.Contains(body, "10.0.0.7") || strings.Contains(body, job) {
			t.Fatalf("%s gives away the upstream message or the job: %s", name, body)
		}
		want := domain.FingerprintRequestError{Question: 2, Code: "http_429", Message: "上游请求失败"}
		if round.ID != "failed-round" || round.JobID != "" || len(round.Errors) != 1 || round.Errors[0] != want {
			t.Fatalf("%s: round = %+v, want the failure with its code and without its message", name, round)
		}
		if round.ReturnedModel != "gpt-5.5-2026" || round.Analysis.ValidAnswers != 2 || round.Questions != 3 {
			t.Fatalf("%s: round = %+v, want the rest of the round as stored", name, round)
		}
	}

	results, body := publicFingerprintResults(t, s)
	if len(results.Models) != 1 || results.Models[0].Round == nil {
		t.Fatalf("results = %s", body)
	}
	check("the list", body, *results.Models[0].Round)
	rounds, body := publicFingerprintRounds(t, s, m.ID)
	if roundIDs(rounds) != "clean-round failed-round" {
		t.Fatalf("rounds = %s", body)
	}
	check("the history", body, rounds[1])
	// A round without failures lists none, as an empty list.
	if rounds[0].JobID != "" || !strings.Contains(body, `"errors":[]`) {
		t.Fatalf("clean round = %s", body)
	}

	// An administrator still reads what the upstream said.
	h := s.Handler()
	cookie, _ := loginForTest(t, h)
	w := request(t, h, "GET", "/api/v1/admin/fingerprint/models/"+m.ID+"/results", nil, cookie, "")
	var stored []domain.FingerprintResult
	if err := json.Unmarshal(w.Body.Bytes(), &stored); err != nil || w.Code != 200 {
		t.Fatalf("admin results: %d %s %v", w.Code, w.Body.String(), err)
	}
	if len(stored) != 2 || stored[1].JobID != job || len(stored[1].Errors) != 1 || stored[1].Errors[0].Message != secret {
		t.Fatalf("admin rounds = %+v", stored)
	}
}

func TestAdminFingerprintOverviewShowsTheConclusionVisitorsSee(t *testing.T) {
	s, _, ch, m := publicFixture(t)
	if err := s.Store.SaveModel(context.Background(), domain.Model{ID: "undetected-model", ChannelID: ch.ID, UpstreamID: "undetected-model-name", DisplayName: "Undetected model"}); err != nil {
		t.Fatal(err)
	}
	s.Fingerprint = readyFingerprintBank()
	h := s.Handler()
	cookie, _ := loginForTest(t, h)
	overview, body := adminFingerprintOverview(t, h, cookie)
	if row := fingerprintRow(t, overview, "undetected-model"); row.Summary != nil || row.Latest != nil || !strings.Contains(body, `"summary":null`) {
		t.Fatalf("undetected model = %+v", row)
	}

	storeFingerprintRounds(t, s, m.ID, "mismatch/full", "mismatch/full", "match/full", "undetermined/")
	overview, _ = adminFingerprintOverview(t, h, cookie)
	row := fingerprintRow(t, overview, m.ID)
	if row.Summary == nil || row.Latest == nil {
		t.Fatalf("detected model = %+v", row)
	}
	if got := *row.Summary; got.Verdict != domain.FingerprintMatch || got.Agree != 1 || strings.Join(got.Verdicts, " ") != "undetermined match mismatch" || got.RoundID != m.ID+"-3" {
		t.Fatalf("summary = %+v", got)
	}
	// The latest round stays the newest one, whatever round the conclusion rests on.
	if row.Latest.ID != m.ID+"-4" || row.Latest.Analysis.Verdict != domain.FingerprintUndetermined {
		t.Fatalf("latest = %+v", row.Latest)
	}
	if other := fingerprintRow(t, overview, "undetected-model"); other.Summary != nil || other.Latest != nil {
		t.Fatalf("the rounds of one model reached another: %+v", other)
	}
}
