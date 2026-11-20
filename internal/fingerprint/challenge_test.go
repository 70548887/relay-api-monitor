package fingerprint

import (
	"context"
	"errors"
	"testing"

	"relaylab/internal/domain"
)

func TestNewChallengesReturnsDistinctQuestions(t *testing.T) {
	for count := 1; count <= MaxChallenges; count++ {
		challenges, err := NewChallenges(count)
		if err != nil || len(challenges) != count {
			t.Fatalf("NewChallenges(%d) = %d questions, %v", count, len(challenges), err)
		}
		ids, counts := map[string]bool{}, map[int]bool{}
		for _, challenge := range challenges {
			if challenge.ID == "" || challenge.Prompt == "" || challenge.ExpectedCount < MinExpectedCount || challenge.ExpectedCount > MaxExpectedCount {
				t.Fatalf("NewChallenges(%d) returned an unusable question %+v", count, challenge)
			}
			ids[challenge.ID], counts[challenge.ExpectedCount] = true, true
		}
		if len(ids) != count || len(counts) != count {
			t.Fatalf("NewChallenges(%d) repeated a question: %+v", count, challenges)
		}
	}
	for _, count := range []int{-1, 0, MaxChallenges + 1} {
		if challenges, err := NewChallenges(count); !errors.Is(err, ErrInvalidChallengeCount) || challenges != nil {
			t.Fatalf("NewChallenges(%d) = %v, %v", count, challenges, err)
		}
	}
}

func TestQuestionsDefaultsToAFullRound(t *testing.T) {
	for configured, want := range map[int]int{-1: 3, 0: 3, 1: 1, 2: 2, 3: 3, 4: 3} {
		if got := Questions(domain.Model{FingerprintQuestions: configured}); got != want {
			t.Errorf("a model configured with %d questions is asked %d, want %d", configured, got, want)
		}
	}
}

func TestCatalogDownloadsTheOnlineBankOnFirstUse(t *testing.T) {
	ctx := context.Background()
	server := newDataServer(t, buildA)
	bank := onlineBank(t.TempDir(), server)

	status, models := bank.Catalog(ctx, "")
	if !status.Ready || status.Source != "online" || status.BuiltAt != buildA.builtAt || status.Models != len(buildA.models) || status.UpdatedAt == nil || status.Error != "" {
		t.Fatalf("catalog status = %+v", status)
	}
	if len(models) != len(buildA.models) || server.count() == 0 {
		t.Fatalf("catalog returned %d models after %d requests", len(models), server.count())
	}
	if again := bank.Status(ctx, ""); !again.Ready || again.Models != status.Models {
		t.Fatalf("status after the download = %+v", again)
	}
}

func TestCatalogAndJudgeReportAnUnavailableBank(t *testing.T) {
	ctx := context.Background()
	bank := offlineBank(t.TempDir())

	status, models := bank.Catalog(ctx, "")
	if status.Ready || status.Source != "online" || status.Error == "" || models != nil {
		t.Fatalf("catalog of an unreachable bank = %+v with %d models", status, len(models))
	}
	if _, err := bank.Judge(ctx, "", threeAnswers, ""); !errors.Is(err, ErrBankUnavailable) {
		t.Fatalf("judge with an unreachable bank: %v", err)
	}

	missing := t.TempDir()
	if status, models = bank.Catalog(ctx, missing); status.Ready || status.Source != "local" || status.Error == "" || models != nil {
		t.Fatalf("catalog of an empty directory = %+v with %d models", status, len(models))
	}
}

func TestBankJudgeValidatesTheRound(t *testing.T) {
	dir := t.TempDir()
	buildA.writeDir(t, dir)
	bank := NewBank(t.TempDir())
	if _, err := bank.Judge(context.Background(), dir, nil, ""); !errors.Is(err, ErrInvalidAnswers) {
		t.Fatalf("judge without answers: %v", err)
	}
}
