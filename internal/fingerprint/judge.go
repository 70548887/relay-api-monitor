package fingerprint

import (
	"errors"
	"fmt"

	lmfpd "github.com/Ikaleio/lm-detector/sdk/go"
	"relaylab/internal/domain"
)

// ErrInvalidAnswers means a round did not hold one to three answers, each with the number count its question asked
// for.
var ErrInvalidAnswers = errors.New("fingerprint: a round needs one to three answers, each with a positive expected count")

// candidateLimit is how many of the closest reference models an analysis keeps.
const candidateLimit = 5

// Answer is one reply and the number count its question asked for.
type Answer struct {
	Text          string
	ExpectedCount int
}

// Judge scores one round of answers and compares the closest reference model with the expected one. expected is a
// bank model ID; when it is empty or not in this build of the bank, the verdict is unlisted. Every valid answer
// counts: three give a full verdict with a confidence, two a reference verdict, one a test verdict.
func (r *Reference) Judge(answers []Answer, expected string) (domain.FingerprintAnalysis, error) {
	if len(answers) < 1 || len(answers) > 3 {
		return domain.FingerprintAnalysis{}, ErrInvalidAnswers
	}
	outputs := make([]lmfpd.Output, len(answers))
	for i, answer := range answers {
		if answer.ExpectedCount <= 0 {
			return domain.FingerprintAnalysis{}, ErrInvalidAnswers
		}
		outputs[i] = lmfpd.Output{Text: answer.Text, ExpectedCount: answer.ExpectedCount}
	}
	analysis, err := r.detector.Analyze(outputs, false)
	if err != nil {
		return domain.FingerprintAnalysis{}, fmt.Errorf("fingerprint: scoring failed: %v", err)
	}
	return r.conclude(analysis, answers, expected), nil
}

func (r *Reference) conclude(analysis *lmfpd.Analysis, answers []Answer, expected string) domain.FingerprintAnalysis {
	out := domain.FingerprintAnalysis{
		Verdict: domain.FingerprintUndetermined, ValidAnswers: analysis.UsedOutputs, BankBuiltAt: r.BuiltAt,
		Candidates: []domain.FingerprintCandidate{}, Answers: make([]domain.FingerprintAnswer, len(analysis.Diagnostics)),
	}
	for i, diagnostic := range analysis.Diagnostics {
		// The SDK blanks an answer it rejects before counting, so the count shown to people is taken from the answer.
		out.Answers[i] = domain.FingerprintAnswer{Numbers: len(lmfpd.ParseNumbers(answers[i].Text)),
			Minimum: diagnostic.MinimumNumbers, Valid: diagnostic.Accepted}
	}
	if model, ok := r.Model(expected); ok {
		out.Expected = &model
	}
	if len(analysis.Results) == 0 {
		out.Reason = domain.FingerprintNoValidAnswers
		if analysis.Decision == "not_model" {
			out.Reason = domain.FingerprintNotModelLike
		}
		return out
	}
	switch {
	case analysis.Decision == "scored":
		out.Level = domain.FingerprintLevelFull
	case analysis.UsedOutputs == 2:
		out.Level = domain.FingerprintLevelReference
	default:
		out.Level = domain.FingerprintLevelTest
	}
	for i, result := range analysis.Results {
		if i < candidateLimit {
			out.Candidates = append(out.Candidates, domain.FingerprintCandidate{Model: result.Model, DisplayName: result.DisplayName,
				Family: result.Family, FamilyName: result.FamilyName, Score: result.Score, Probability: copyFloat(result.Probability)})
		}
		if out.Expected != nil && result.Model == out.Expected.ID {
			out.ExpectedRank = i + 1
		}
	}
	out.Confidence = copyFloat(analysis.Probability)
	closest := analysis.Results[0]
	switch {
	case out.Expected == nil:
		out.Verdict = domain.FingerprintUnlisted
	case closest.Model == out.Expected.ID:
		out.Verdict = domain.FingerprintMatch
	case closest.Family != "" && closest.Family == out.Expected.Family:
		out.Verdict = domain.FingerprintFamily
	default:
		out.Verdict = domain.FingerprintMismatch
	}
	return out
}

func copyFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
