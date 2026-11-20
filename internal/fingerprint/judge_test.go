package fingerprint

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	lmfpd "github.com/Ikaleio/lm-detector/sdk/go"
	"relaylab/internal/domain"
)

// ranking builds the SDK analysis of a round with the given number of valid answers out of three, whose candidates
// are the given bank models, best first.
func ranking(ref *Reference, valid int, models ...string) *lmfpd.Analysis {
	analysis := &lmfpd.Analysis{Decision: "partial", UsedOutputs: valid}
	for i := 0; i < 3; i++ {
		analysis.Diagnostics = append(analysis.Diagnostics, lmfpd.Diagnostic{Index: i, MinimumNumbers: 165, Accepted: i < valid})
	}
	for i, id := range models {
		model, _ := ref.Model(id)
		result := lmfpd.AnalysisResult{Model: id, DisplayName: model.DisplayName, Family: model.Family, FamilyName: model.FamilyName,
			Score: float64(len(models) - i)}
		if valid == 3 {
			probability := 0.9 / float64(i+1)
			result.Probability = &probability
		}
		analysis.Results = append(analysis.Results, result)
	}
	if valid == 3 {
		analysis.Decision, analysis.Probability = "scored", analysis.Results[0].Probability
	}
	return analysis
}

func numbers(count int) string {
	parts := make([]string, count)
	for i := range parts {
		// Neighbours are far apart, as in a model's reply, and stay within 1 to 355.
		parts[i] = strconv.Itoa(1 + (i*97+(i%2)*170)%355)
	}
	return strings.Join(parts, ", ")
}

var threeAnswers = []Answer{{numbers(300), 300}, {numbers(300), 300}, {numbers(40), 300}}

func TestConcludeComparesTheClosestModelWithTheExpectedOne(t *testing.T) {
	ref := publishedReference()
	cases := []struct {
		name     string
		expected string
		closest  []string
		verdict  string
		rank     int
	}{
		{"same model", "claude-opus-4.8", []string{"claude-opus-4.8", "claude-opus-4.7", "gpt-5.5"}, domain.FingerprintMatch, 1},
		{"same family", "claude-opus-4.8", []string{"claude-sonnet-4.6", "gpt-5.5", "claude-opus-4.8"}, domain.FingerprintFamily, 3},
		{"other family", "claude-opus-4.8", []string{"glm-5.3", "claude-opus-4.8", "gpt-5.5"}, domain.FingerprintMismatch, 2},
		{"expected model missing from the ranking", "claude-opus-4.8", []string{"glm-5.3", "gpt-5.5"}, domain.FingerprintMismatch, 0},
		{"no expected model", "", []string{"glm-5.3", "gpt-5.5"}, domain.FingerprintUnlisted, 0},
		{"expected model not in this bank", "claude-opus-9", []string{"glm-5.3", "gpt-5.5"}, domain.FingerprintUnlisted, 0},
	}
	for _, c := range cases {
		out := ref.conclude(ranking(ref, 3, c.closest...), threeAnswers, c.expected)
		if out.Verdict != c.verdict || out.ExpectedRank != c.rank || out.Reason != "" {
			t.Errorf("%s: verdict %q rank %d reason %q, want %q rank %d", c.name, out.Verdict, out.ExpectedRank, out.Reason, c.verdict, c.rank)
		}
		if listed := c.verdict != domain.FingerprintUnlisted; (out.Expected != nil) != listed || (listed && out.Expected.ID != c.expected) {
			t.Errorf("%s: expected model %+v", c.name, out.Expected)
		}
		if len(out.Candidates) != len(c.closest) || out.Candidates[0].Model != c.closest[0] || out.Candidates[0].FamilyName == "" {
			t.Errorf("%s: candidates %+v", c.name, out.Candidates)
		}
	}
}

func TestConcludeGradesByTheNumberOfValidAnswers(t *testing.T) {
	ref := publishedReference()
	for valid, level := range map[int]string{3: domain.FingerprintLevelFull, 2: domain.FingerprintLevelReference, 1: domain.FingerprintLevelTest} {
		out := ref.conclude(ranking(ref, valid, "gpt-5.5", "gpt-5.4"), threeAnswers, "gpt-5.5")
		if out.Verdict != domain.FingerprintMatch || out.Level != level || out.ValidAnswers != valid {
			t.Errorf("%d valid answers: verdict %q level %q valid %d, want level %q", valid, out.Verdict, out.Level, out.ValidAnswers, level)
		}
		full := valid == 3
		if (out.Confidence != nil) != full || (out.Candidates[0].Probability != nil) != full {
			t.Errorf("%d valid answers: confidence %v, closest probability %v", valid, out.Confidence, out.Candidates[0].Probability)
		}
		if full && (*out.Confidence != 0.9 || *out.Candidates[1].Probability != 0.45) {
			t.Errorf("confidence %v, second probability %v", *out.Confidence, *out.Candidates[1].Probability)
		}
		if out.BankBuiltAt != ref.BuiltAt {
			t.Errorf("bank build %q", out.BankBuiltAt)
		}
	}
}

func TestConcludeKeepsTheClosestFiveAndReportsEveryAnswer(t *testing.T) {
	ref := publishedReference()
	closest := []string{"gpt-5.5", "gpt-5.4", "gpt-4o", "glm-5.3", "glm-5.2", "grok-4.5", "claude-opus-4.8"}
	out := ref.conclude(ranking(ref, 2, closest...), threeAnswers, "claude-opus-4.8")
	if len(out.Candidates) != 5 || out.Candidates[4].Model != "glm-5.2" || out.ExpectedRank != 7 || out.Verdict != domain.FingerprintMismatch {
		t.Fatalf("candidates %+v, rank %d, verdict %q", out.Candidates, out.ExpectedRank, out.Verdict)
	}
	want := []domain.FingerprintAnswer{{Numbers: 300, Minimum: 165, Valid: true}, {Numbers: 300, Minimum: 165, Valid: true}, {Numbers: 40, Minimum: 165}}
	if len(out.Answers) != len(want) {
		t.Fatalf("answers %+v", out.Answers)
	}
	for i, answer := range want {
		if out.Answers[i] != answer {
			t.Errorf("answer %d is %+v, want %+v", i, out.Answers[i], answer)
		}
	}
}

func TestConcludeIsUndeterminedWithoutARanking(t *testing.T) {
	ref := publishedReference()
	for decision, reason := range map[string]string{"unscorable": domain.FingerprintNoValidAnswers, "not_model": domain.FingerprintNotModelLike} {
		analysis := ranking(ref, 0)
		analysis.Decision = decision
		out := ref.conclude(analysis, threeAnswers, "gpt-5.5")
		if out.Verdict != domain.FingerprintUndetermined || out.Reason != reason || out.Level != "" || out.Confidence != nil || out.ExpectedRank != 0 {
			t.Errorf("%s: %+v", decision, out)
		}
		if out.Candidates == nil || len(out.Candidates) != 0 || out.Expected == nil || len(out.Answers) != 3 {
			t.Errorf("%s: candidates %v, expected %v, answers %v", decision, out.Candidates, out.Expected, out.Answers)
		}
	}
}

func TestJudgeRejectsAnInvalidRound(t *testing.T) {
	ref := publishedReference()
	one := Answer{numbers(300), 300}
	for name, answers := range map[string][]Answer{
		"no answers": nil, "four answers": {one, one, one, one}, "no expected count": {{numbers(300), 0}}, "negative expected count": {one, {numbers(300), -1}},
	} {
		if _, err := ref.Judge(answers, "gpt-5.5"); !errors.Is(err, ErrInvalidAnswers) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestRealReferenceBankJudge scores made-up answers against the published reference bank. It shares the switch and
// the cache directory of TestRealReferenceBankDownload.
func TestRealReferenceBankJudge(t *testing.T) {
	if os.Getenv("RELAYLAB_REAL_FINGERPRINT_DOWNLOAD_TEST") != "1" {
		t.Skip("set RELAYLAB_REAL_FINGERPRINT_DOWNLOAD_TEST=1 to score against the published reference bank")
	}
	dir := os.Getenv("RELAYLAB_FINGERPRINT_TEST_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	ref, err := NewBank(dir).Reference(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	expected := ref.Models()[0].ID
	answer, short := Answer{numbers(300), 300}, Answer{numbers(40), 300}
	for valid, level := range map[int]string{3: domain.FingerprintLevelFull, 2: domain.FingerprintLevelReference, 1: domain.FingerprintLevelTest} {
		answers := []Answer{short, short, short}
		for i := 0; i < valid; i++ {
			answers[i] = answer
		}
		out, err := ref.Judge(answers, expected)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%d valid: verdict %s, level %s, expected rank %d, confidence %v, closest %+v", valid, out.Verdict, out.Level, out.ExpectedRank, out.Confidence, out.Candidates[0])
		if out.Level != level || out.ValidAnswers != valid || len(out.Candidates) != candidateLimit || out.ExpectedRank < 1 || out.ExpectedRank > len(ref.Models()) {
			t.Fatalf("%d valid answers: %+v", valid, out)
		}
		if (out.Confidence != nil) != (valid == 3) {
			t.Fatalf("%d valid answers: confidence %v", valid, out.Confidence)
		}
		for i, reported := range out.Answers {
			if wantValid := i < valid; reported.Valid != wantValid || reported.Minimum != 165 || (wantValid && reported.Numbers != 300) || (!wantValid && reported.Numbers != 40) {
				t.Fatalf("%d valid answers: answer %d is %+v", valid, i, reported)
			}
		}
	}
	single, err := ref.Judge([]Answer{answer}, "")
	if err != nil || single.Verdict != domain.FingerprintUnlisted || single.Level != domain.FingerprintLevelTest || single.Expected != nil {
		t.Fatalf("one answer without an expected model: %+v, %v", single, err)
	}
	none, err := ref.Judge([]Answer{short, short, short}, expected)
	if err != nil || none.Verdict != domain.FingerprintUndetermined || none.Reason != domain.FingerprintNoValidAnswers {
		t.Fatalf("no valid answers: %+v, %v", none, err)
	}
	sorted := make([]string, 300)
	for i := range sorted {
		sorted[i] = strconv.Itoa(i + 1)
	}
	counting := Answer{strings.Join(sorted, ", "), 300}
	program, err := ref.Judge([]Answer{counting, counting, counting}, expected)
	if err != nil || program.Verdict != domain.FingerprintUndetermined || program.Reason != domain.FingerprintNotModelLike || program.ValidAnswers != 3 {
		t.Fatalf("counting upwards: %+v, %v", program, err)
	}
}
