package domain

import "time"

// Fingerprint verdicts compare the model a channel declares with the reference model its answers resemble most.
const (
	FingerprintMatch    = "match"
	FingerprintFamily   = "family"
	FingerprintMismatch = "mismatch"
	// FingerprintUnlisted means the declared model is not in the reference bank, so there is nothing to compare with.
	FingerprintUnlisted = "unlisted"
	// FingerprintUndetermined means the answers produced no ranking.
	FingerprintUndetermined = "undetermined"
)

// Fingerprint levels say how many valid answers a verdict rests on: three, two, or one. Only a full verdict carries
// a confidence.
const (
	FingerprintLevelFull      = "full"
	FingerprintLevelReference = "reference"
	FingerprintLevelTest      = "test"
)

// Reasons for an undetermined fingerprint verdict.
const (
	FingerprintNoValidAnswers = "no_valid_answers"
	FingerprintNotModelLike   = "not_model_like"
)

// FingerprintModel is one model of the reference bank.
type FingerprintModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Family      string `json:"family"`
	FamilyName  string `json:"family_name"`
}

// FingerprintBank describes the reference bank that answers are scored against.
type FingerprintBank struct {
	Ready bool `json:"ready"`
	// Source is online or local.
	Source  string `json:"source"`
	BuiltAt string `json:"built_at"`
	Models  int    `json:"models"`
	// UpdatedAt is when the files of this build were written.
	UpdatedAt *time.Time `json:"updated_at"`
	Error     string     `json:"error"`
}

// FingerprintOverview is the data of the public fingerprint page.
type FingerprintOverview struct {
	Bank   FingerprintBank    `json:"bank"`
	Models []FingerprintModel `json:"models"`
}

// FingerprintChallenge is one question of a round. A model answers it with ExpectedCount numbers.
type FingerprintChallenge struct {
	ID            string `json:"id"`
	Prompt        string `json:"prompt"`
	ExpectedCount int    `json:"expected_count"`
}

// FingerprintChallengeSet is one round of questions.
type FingerprintChallengeSet struct {
	Challenges []FingerprintChallenge `json:"challenges"`
}

// FingerprintAnswerInput is one pasted answer with the number count its question asked for.
type FingerprintAnswerInput struct {
	Text          string `json:"text"`
	ExpectedCount int    `json:"expected_count"`
}

// FingerprintAnalyzeRequest asks to score one round of pasted answers. ExpectedModel is the ID of the reference
// model the answers are claimed to come from, or empty when the bank does not list that model.
type FingerprintAnalyzeRequest struct {
	Answers       []FingerprintAnswerInput `json:"answers"`
	ExpectedModel string                   `json:"expected_model"`
}

// FingerprintCandidate is one reference model in a ranking. Probability is set only for a full verdict.
type FingerprintCandidate struct {
	Model       string   `json:"model"`
	DisplayName string   `json:"display_name"`
	Family      string   `json:"family"`
	FamilyName  string   `json:"family_name"`
	Score       float64  `json:"score"`
	Probability *float64 `json:"probability"`
}

// FingerprintAnswer reports how one answer was read. An answer is valid when it holds at least Minimum numbers.
type FingerprintAnswer struct {
	Numbers int  `json:"numbers"`
	Minimum int  `json:"minimum"`
	Valid   bool `json:"valid"`
}

// FingerprintAnalysis is the outcome of scoring one round of answers.
type FingerprintAnalysis struct {
	Verdict string `json:"verdict"`
	// Level is empty for an undetermined verdict.
	Level string `json:"level"`
	// Reason is set only for an undetermined verdict.
	Reason       string `json:"reason"`
	ValidAnswers int    `json:"valid_answers"`
	// Expected is the reference model the declared model was compared with, or nil when the bank does not list it.
	Expected *FingerprintModel `json:"expected"`
	// ExpectedRank is the 1-based position of Expected in the ranking, or 0 without a ranking or an expected model.
	ExpectedRank int `json:"expected_rank"`
	// Candidates are the closest reference models, best first.
	Candidates []FingerprintCandidate `json:"candidates"`
	// Confidence is the probability of the closest model among the models of the reference bank.
	Confidence  *float64            `json:"confidence"`
	Answers     []FingerprintAnswer `json:"answers"`
	BankBuiltAt string              `json:"bank_built_at"`
}
