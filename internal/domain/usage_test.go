package domain

import "testing"

func TestUsageAddSumsTheCountsThatWereReported(t *testing.T) {
	count := func(n int64) *int64 { return &n }
	var total Usage
	total.Add(Usage{})
	if total != (Usage{}) {
		t.Fatalf("total %+v after a response without counts, want every count unknown", total)
	}

	first := Usage{InputTokens: count(3), OutputTokens: count(5), TotalTokens: count(8), CachedInputTokens: count(2)}
	total.Add(first)
	total.Add(Usage{InputTokens: count(10), OutputTokens: count(20), TotalTokens: count(30), CacheCreationInputTokens: count(7)})
	total.Add(Usage{OutputTokens: count(1)})
	for _, tc := range []struct {
		name string
		got  *int64
		want int64
	}{
		{"input", total.InputTokens, 13},
		{"output", total.OutputTokens, 26},
		{"total", total.TotalTokens, 38},
		{"cached input", total.CachedInputTokens, 2},
		{"cache creation input", total.CacheCreationInputTokens, 7},
	} {
		if tc.got == nil || *tc.got != tc.want {
			t.Fatalf("%s tokens %v, want %d", tc.name, tc.got, tc.want)
		}
	}
	if *first.InputTokens != 3 || *first.OutputTokens != 5 || *first.TotalTokens != 8 || *first.CachedInputTokens != 2 {
		t.Fatalf("the added usage changed to %+v", first)
	}
}
