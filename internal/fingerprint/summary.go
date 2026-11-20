package fingerprint

import "relaylab/internal/domain"

// SummaryRounds is how many of the newest rounds of a model its summary counts.
const SummaryRounds = 3

// completeness orders the levels of a verdict by the number of valid answers behind them.
var completeness = map[string]int{domain.FingerprintLevelTest: 1, domain.FingerprintLevelReference: 2, domain.FingerprintLevelFull: 3}

// Summarize draws one conclusion from the stored rounds of a model, given newest first. Only the first SummaryRounds
// count. It returns nil for a model without rounds. See domain.FingerprintSummary for the rule.
func Summarize(rounds []domain.FingerprintResult) *domain.FingerprintSummary {
	if len(rounds) == 0 {
		return nil
	}
	if len(rounds) > SummaryRounds {
		rounds = rounds[:SummaryRounds]
	}
	out := &domain.FingerprintSummary{Verdict: domain.FingerprintUndetermined, Agree: len(rounds), Verdicts: make([]string, len(rounds)), RoundID: rounds[0].ID}
	votes := map[string]int{}
	// lead holds, for every verdict, the index of the round that stands for it.
	lead := map[string]int{}
	for i, round := range rounds {
		verdict := round.Analysis.Verdict
		out.Verdicts[i] = verdict
		if verdict == domain.FingerprintUndetermined {
			continue
		}
		votes[verdict]++
		if j, seen := lead[verdict]; !seen || completeness[round.Analysis.Level] > completeness[rounds[j].Analysis.Level] {
			lead[verdict] = i
		}
	}
	// ahead reports whether the verdict that round i stands for beats the one that round j stands for.
	ahead := func(i, j int) bool {
		a, b := rounds[i].Analysis, rounds[j].Analysis
		if votes[a.Verdict] != votes[b.Verdict] {
			return votes[a.Verdict] > votes[b.Verdict]
		}
		if completeness[a.Level] != completeness[b.Level] {
			return completeness[a.Level] > completeness[b.Level]
		}
		return i < j
	}
	winner := -1
	for _, i := range lead {
		if winner < 0 || ahead(i, winner) {
			winner = i
		}
	}
	if winner >= 0 {
		out.Verdict, out.Agree, out.RoundID = rounds[winner].Analysis.Verdict, votes[rounds[winner].Analysis.Verdict], rounds[winner].ID
	}
	return out
}
