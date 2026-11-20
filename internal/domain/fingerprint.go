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

// Fingerprint modes say which reference model a detection compares a channel model with.
const (
	// FingerprintModeAuto derives the reference model from the upstream model ID. An empty mode means the same.
	FingerprintModeAuto = "auto"
	// FingerprintModeManual uses the reference model an administrator picked.
	FingerprintModeManual = "manual"
	// FingerprintModeUnlisted compares with no reference model, because the bank does not list the model.
	FingerprintModeUnlisted = "unlisted"
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

// FingerprintResult is one stored detection round of a channel model.
type FingerprintResult struct {
	ID      string `json:"id"`
	JobID   string `json:"job_id"`
	ModelID string `json:"model_id"`
	// Questions is how many questions the round asked.
	Questions int                 `json:"questions"`
	Analysis  FingerprintAnalysis `json:"analysis"`
	// Errors lists the questions whose request failed. The round is scored on the replies that arrived, which may
	// include the part of a reply that came before its request failed.
	Errors []FingerprintRequestError `json:"errors"`
	// ReturnedModel is the model name the upstream reported in its responses.
	ReturnedModel string `json:"returned_model"`
	// Usage adds up the token usage the upstream reported. A request that failed or was stopped because its reply
	// already held more numbers than asked reports none.
	Usage      Usage     `json:"usage"`
	DurationMS int64     `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
}

// FingerprintRequestError is the failure of the request for one question of a round.
type FingerprintRequestError struct {
	// Question is the 1-based position of the question in the round.
	Question int    `json:"question"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// FingerprintModelSettings is how the detection of one channel model is set up.
type FingerprintModelSettings struct {
	// Mode is auto, manual or unlisted. See the FingerprintMode constants.
	Mode string `json:"mode"`
	// Model is the ID of the reference model picked for the manual mode, and empty in the other modes.
	Model string `json:"model"`
	// Questions is how many questions one round asks, 1 to 3.
	Questions int `json:"questions"`
	// Scheduled turns scheduled detection on.
	Scheduled bool `json:"scheduled"`
}

// AdminFingerprintModel is one channel model on the admin fingerprint page.
type AdminFingerprintModel struct {
	ID           string `json:"id"`
	UpstreamID   string `json:"upstream_id"`
	DisplayName  string `json:"display_name"`
	ChannelID    string `json:"channel_id"`
	ChannelName  string `json:"channel_name"`
	PlatformID   string `json:"platform_id"`
	PlatformName string `json:"platform_name"`
	// Runnable is false when a detection cannot be started: the model is missing upstream or its channel is disabled.
	Runnable bool                     `json:"runnable"`
	Settings FingerprintModelSettings `json:"settings"`
	// Expected is the ID of the reference model the next round compares with. It is empty when the settings select
	// none, or while the bank is not ready.
	Expected string `json:"expected"`
	// Latest is the newest stored round.
	Latest *FingerprintResult `json:"latest"`
	// Job is the newest detection job. It may still be running, or may have failed without storing a round.
	Job *Job `json:"job"`
}

// AdminFingerprintOverview is the data of the admin fingerprint page.
type AdminFingerprintOverview struct {
	Bank FingerprintBank `json:"bank"`
	// References are the models of the reference bank.
	References []FingerprintModel      `json:"references"`
	Models     []AdminFingerprintModel `json:"models"`
}

// FingerprintBankRefresh is the outcome of downloading the online reference bank again.
type FingerprintBankRefresh struct {
	Bank FingerprintBank `json:"bank"`
	// Changed reports whether the downloaded build differs from the one used before.
	Changed bool `json:"changed"`
}
