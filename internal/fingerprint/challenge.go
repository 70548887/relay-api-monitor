package fingerprint

import (
	"errors"

	lmfpd "github.com/Ikaleio/lm-detector/sdk/go"
	"relaylab/internal/domain"
)

// A question asks for a number count within this range.
const (
	MinExpectedCount = 292
	MaxExpectedCount = 332
)

// MaxChallenges is the number of questions in a full round.
const MaxChallenges = 3

// ErrInvalidChallengeCount means a round was asked to hold fewer than one or more than three questions.
var ErrInvalidChallengeCount = errors.New("fingerprint: a round holds one to three questions")

// Questions returns how many questions a round of a channel model asks: the count configured for the model, or a
// full round when none is configured.
func Questions(m domain.Model) int {
	if m.FingerprintQuestions < 1 || m.FingerprintQuestions > MaxChallenges {
		return MaxChallenges
	}
	return m.FingerprintQuestions
}

// NewChallenges returns the questions of one round. Each question is random and asks for a different number count.
func NewChallenges(count int) ([]domain.FingerprintChallenge, error) {
	if count < 1 || count > MaxChallenges {
		return nil, ErrInvalidChallengeCount
	}
	generated, err := lmfpd.GenerateChallenges(count)
	if err != nil {
		return nil, ErrInvalidChallengeCount
	}
	challenges := make([]domain.FingerprintChallenge, len(generated))
	for i, challenge := range generated {
		challenges[i] = domain.FingerprintChallenge{ID: challenge.ID, Prompt: challenge.Prompt, ExpectedCount: challenge.ExpectedCount}
	}
	return challenges, nil
}
