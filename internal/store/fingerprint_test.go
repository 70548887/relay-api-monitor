package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func finishFingerprint(t *testing.T, s *Store, ctx context.Context, m domain.Model, round *domain.FingerprintResult) domain.Job {
	t.Helper()
	j := dispatch(t, s, ctx, claimed(t, s, ctx, m, "fingerprint", ""))
	j.State = "succeeded"
	if e := s.FinishFingerprintJob(ctx, j, round); e != nil {
		t.Fatal(e)
	}
	return j
}

func TestFingerprintJobStoresItsRoundAndKeepsTheLatestRounds(t *testing.T) {
	s, ctx, _, m := fixture(t)
	base := nowUTC().Add(-time.Hour)
	var jobs []domain.Job
	for i := 0; i < FingerprintHistory+2; i++ {
		round := &domain.FingerprintResult{Questions: 3, Analysis: domain.FingerprintAnalysis{Verdict: domain.FingerprintMatch, Level: domain.FingerprintLevelFull, ValidAnswers: 3}, CreatedAt: base.Add(time.Duration(i) * time.Second)}
		jobs = append(jobs, finishFingerprint(t, s, ctx, m, round))
	}
	rounds, e := s.ListFingerprintResults(ctx, m.ID)
	if e != nil || len(rounds) != FingerprintHistory {
		t.Fatalf("kept %d rounds: %v", len(rounds), e)
	}
	for i, round := range rounds {
		want := jobs[len(jobs)-1-i]
		if round.ID == "" || round.JobID != want.ID || round.ModelID != m.ID || round.Questions != 3 || round.Analysis.Verdict != domain.FingerprintMatch || round.Analysis.ValidAnswers != 3 {
			t.Fatalf("round %d = %+v, want the round of job %s", i, round, want.ID)
		}
	}
	saved, e := s.GetJob(ctx, jobs[0].ID)
	if e != nil || saved.State != "succeeded" {
		t.Fatal("dropping an old round changed its job", saved, e)
	}
}

func TestFingerprintCompletionMustMatchTheJobKind(t *testing.T) {
	s, ctx, _, m := fixture(t)
	probe := dispatch(t, s, ctx, claimed(t, s, ctx, m, "probe", ""))
	probe.State = "succeeded"
	if e := s.FinishFingerprintJob(ctx, probe, &domain.FingerprintResult{Questions: 3}); e == nil || errors.Is(e, ErrConflict) {
		t.Fatalf("a probe job stored a fingerprint round: %v", e)
	}
	fingerprint := dispatch(t, s, ctx, claimed(t, s, ctx, m, "fingerprint", ""))
	fingerprint.State = "succeeded"
	if e := s.FinishJob(ctx, fingerprint, &domain.ProbeResult{Status: "ok"}, nil); e == nil || errors.Is(e, ErrConflict) {
		t.Fatalf("a fingerprint job stored a probe result: %v", e)
	}
	for _, j := range []domain.Job{probe, fingerprint} {
		saved, e := s.GetJob(ctx, j.ID)
		if e != nil || saved.State != "dispatching" {
			t.Fatal("a rejected completion changed the job", saved, e)
		}
	}
	rounds, e := s.ListFingerprintResults(ctx, m.ID)
	if e != nil || len(rounds) != 0 {
		t.Fatal("a rejected completion left a round", rounds, e)
	}
	results, e := s.ListProbeResults(ctx, m.ID, time.Time{})
	if e != nil || len(results) != 0 {
		t.Fatal("a rejected completion left a probe result", results, e)
	}
}

func TestCanceledFingerprintJobKeepsNoRound(t *testing.T) {
	s, ctx, _, m := fixture(t)
	j := dispatch(t, s, ctx, claimed(t, s, ctx, m, "fingerprint", ""))
	if e := s.RequestCancel(ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	j.State = "succeeded"
	if e := s.FinishFingerprintJob(ctx, j, &domain.FingerprintResult{Questions: 3}); e != nil {
		t.Fatal(e)
	}
	saved, e := s.GetJob(ctx, j.ID)
	if e != nil || saved.State != "canceled" {
		t.Fatal("the cancellation was not kept", saved, e)
	}
	rounds, e := s.ListFingerprintResults(ctx, m.ID)
	if e != nil || len(rounds) != 0 {
		t.Fatal("a canceled job left a round", rounds, e)
	}
}

func TestLatestFingerprintResultsTakeTheNewestRoundsOfEveryModel(t *testing.T) {
	s, ctx, c, m := fixture(t)
	other := domain.Model{ID: "other-model", ChannelID: c.ID, UpstreamID: "other-upstream", DisplayName: "Other", Source: "manual"}
	if e := s.SaveModel(ctx, other); e != nil {
		t.Fatal(e)
	}
	base := nowUTC().Add(-time.Hour)
	var first []domain.Job
	for i := 0; i < 4; i++ {
		first = append(first, finishFingerprint(t, s, ctx, m, &domain.FingerprintResult{Questions: 3, CreatedAt: base.Add(time.Duration(i) * time.Minute)}))
	}
	second := finishFingerprint(t, s, ctx, other, &domain.FingerprintResult{Questions: 1, CreatedAt: base})
	rounds, e := s.ListLatestFingerprintResults(ctx, 3)
	if e != nil {
		t.Fatal(e)
	}
	var got []string
	for _, round := range rounds {
		got = append(got, round.ModelID+":"+round.JobID)
	}
	want := []string{m.ID + ":" + first[3].ID, m.ID + ":" + first[2].ID, m.ID + ":" + first[1].ID, other.ID + ":" + second.ID}
	if !slices.Equal(got, want) {
		t.Fatalf("latest rounds = %v, want %v", got, want)
	}
}

func TestSchemaFourDatabaseGainsTheFingerprintTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-four.db")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{"DROP TABLE fingerprint_results", "PRAGMA user_version=4"} {
		if _, e = s.DB.Exec(q); e != nil {
			s.Close()
			t.Fatal(e)
		}
	}
	s.Close()
	// A worker never migrates: it has to wait for the server to do it.
	if existing, e := OpenExisting(path); e == nil {
		existing.Close()
		t.Fatal("a database without the fingerprint table was opened without migrating")
	} else if !strings.Contains(e.Error(), "migration required") {
		t.Fatal(e)
	}
	s, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var version int
	if e = s.DB.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != 5 {
		t.Fatal("migration version", version, e)
	}
	if rounds, e := s.ListFingerprintResults(context.Background(), "model"); e != nil || len(rounds) != 0 {
		t.Fatal(rounds, e)
	}
}

func TestSaveFingerprintModelSettingsLeavesTheOtherFieldsAsStored(t *testing.T) {
	s, ctx, _, m := fixture(t)
	// The caller may hold an older copy of the model; only the fingerprint settings may come from the request.
	renamed := m
	renamed.DisplayName = "Renamed meanwhile"
	if e := s.SaveModel(ctx, renamed); e != nil {
		t.Fatal(e)
	}
	want := domain.FingerprintModelSettings{Mode: domain.FingerprintModeManual, Model: "gpt-5.5", Questions: 2, Scheduled: true}
	if e := s.SaveFingerprintModelSettings(ctx, m.ID, want); e != nil {
		t.Fatal(e)
	}
	got, e := s.GetModel(ctx, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	if got.FingerprintMode != want.Mode || got.FingerprintModel != want.Model || got.FingerprintQuestions != want.Questions || !got.FingerprintScheduled {
		t.Fatalf("stored fingerprint settings = %+v", got)
	}
	if got.DisplayName != "Renamed meanwhile" || got.UpstreamID != m.UpstreamID || !got.ProbeEnabled || !got.ArtifactChinese || got.MaxTokens != m.MaxTokens {
		t.Fatalf("saving the fingerprint settings changed other fields: %+v", got)
	}
	if e = s.SaveFingerprintModelSettings(ctx, "no-such-model", want); !errors.Is(e, sql.ErrNoRows) {
		t.Fatalf("unknown model: %v", e)
	}
}

func TestLatestFingerprintJobsTakeTheNewestJobOfEveryModel(t *testing.T) {
	s, ctx, c, m := fixture(t)
	other := domain.Model{ID: "other", ChannelID: c.ID, UpstreamID: "other-upstream"}
	if e := s.SaveModel(ctx, other); e != nil {
		t.Fatal(e)
	}
	if jobs, e := s.ListLatestFingerprintJobs(ctx); e != nil || len(jobs) != 0 {
		t.Fatalf("jobs before any detection = %+v, %v", jobs, e)
	}
	finishFingerprint(t, s, ctx, m, &domain.FingerprintResult{Questions: 3})
	newest, e := s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: "fingerprint", CreatedAt: nowUTC().Add(time.Second)})
	if e != nil {
		t.Fatal(e)
	}
	// A newer job of another kind is not a detection.
	if _, e = s.EnqueueJob(ctx, domain.Job{ModelID: m.ID, Kind: "probe", CreatedAt: nowUTC().Add(2 * time.Second)}); e != nil {
		t.Fatal(e)
	}
	jobs, e := s.ListLatestFingerprintJobs(ctx)
	if e != nil || len(jobs) != 1 || jobs[0].ID != newest.ID || jobs[0].State != "queued" || jobs[0].Kind != "fingerprint" {
		t.Fatalf("jobs = %+v, %v; want the queued job %s", jobs, e, newest.ID)
	}
	second, e := s.EnqueueJob(ctx, domain.Job{ModelID: other.ID, Kind: "fingerprint"})
	if e != nil {
		t.Fatal(e)
	}
	jobs, e = s.ListLatestFingerprintJobs(ctx)
	if e != nil || len(jobs) != 2 {
		t.Fatalf("jobs = %+v, %v", jobs, e)
	}
	byModel := map[string]string{}
	for _, j := range jobs {
		byModel[j.ModelID] = j.ID
	}
	if byModel[m.ID] != newest.ID || byModel[other.ID] != second.ID {
		t.Fatalf("newest job by model = %v", byModel)
	}
}
