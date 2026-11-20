package fingerprint

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"relaylab/internal/domain"
)

// storedRounds builds rounds from "verdict" or "verdict/level" words, newest first, with the IDs r1, r2 and so on.
func storedRounds(words ...string) []domain.FingerprintResult {
	rounds := make([]domain.FingerprintResult, len(words))
	for i, word := range words {
		verdict, level, _ := strings.Cut(word, "/")
		rounds[i] = domain.FingerprintResult{ID: fmt.Sprintf("r%d", i+1), Analysis: domain.FingerprintAnalysis{Verdict: verdict, Level: level}}
	}
	return rounds
}

func TestSummarize(t *testing.T) {
	if summary := Summarize(nil); summary != nil {
		t.Fatalf("a model without rounds has no summary, got %+v", summary)
	}
	for name, c := range map[string]struct {
		rounds  []string
		verdict string
		agree   int
		round   string
	}{
		"one round":                                {[]string{"match/full"}, "match", 1, "r1"},
		"three rounds agree":                       {[]string{"mismatch/full", "mismatch/full", "mismatch/full"}, "mismatch", 3, "r1"},
		"two of three":                             {[]string{"mismatch/full", "match/full", "match/full"}, "match", 2, "r2"},
		"older rounds do not count":                {[]string{"match/full", "match/full", "mismatch/full", "mismatch/full", "mismatch/full"}, "match", 2, "r1"},
		"a round without a verdict does not vote":  {[]string{"undetermined", "undetermined", "family/full"}, "family", 1, "r3"},
		"no round reached a verdict":               {[]string{"undetermined", "undetermined"}, "undetermined", 2, "r1"},
		"a model the bank does not list":           {[]string{"unlisted/full", "unlisted/reference", "unlisted/full"}, "unlisted", 3, "r1"},
		"equal votes, the newer round wins":        {[]string{"family/full", "mismatch/full"}, "family", 1, "r1"},
		"equal votes, the fuller round wins":       {[]string{"match/test", "mismatch/full"}, "mismatch", 1, "r2"},
		"three verdicts":                           {[]string{"match/test", "family/reference", "mismatch/reference"}, "family", 1, "r2"},
		"more votes beat a fuller round":           {[]string{"match/test", "match/test", "mismatch/full"}, "match", 2, "r1"},
		"the fullest agreeing round stands for it": {[]string{"match/test", "match/full", "mismatch/full"}, "match", 2, "r2"},
	} {
		t.Run(name, func(t *testing.T) {
			rounds := storedRounds(c.rounds...)
			counted := rounds[:min(len(rounds), SummaryRounds)]
			verdicts := make([]string, len(counted))
			for i, round := range counted {
				verdicts[i] = round.Analysis.Verdict
			}
			want := &domain.FingerprintSummary{Verdict: c.verdict, Agree: c.agree, Verdicts: verdicts, RoundID: c.round}
			// The outcome must not depend on the order in which the verdicts are compared.
			for range 20 {
				if got := Summarize(rounds); !reflect.DeepEqual(got, want) {
					t.Fatalf("Summarize(%v) = %+v, want %+v", c.rounds, got, want)
				}
			}
		})
	}
}
