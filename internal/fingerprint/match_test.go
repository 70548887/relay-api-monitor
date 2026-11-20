package fingerprint

import (
	"strings"
	"testing"
	"time"

	"relaylab/internal/domain"
)

// publishedModels are the model IDs of the reference bank built on 2026-11-19, by family.
var publishedModels = map[string]string{
	"Claude":    "claude-haiku-4.5 claude-sonnet-4.6 claude-sonnet-5 claude-opus-4.6 claude-opus-4.7 claude-opus-4.8 claude-opus-5 claude-fable-5.1 claude-fable-5 claude-opus-5.5 claude-sonnet-5.5 claude-haiku-5.5 claude-fable-5.2",
	"Gemini":    "gemini-2.5-pro gemini-3.1-pro-preview gemini-3.5-flash gemini-3.6-flash gemini-3.7-flash gemini-3.8-flash gemini-3.5-flash-lite gemini-4-carbon gemini-4-argon gemini-4-hydrogen gemini-4-neon",
	"Grok":      "grok-4.5 grok-4.6 grok-4.7",
	"GPT":       "gpt-4o gpt-6-luna gpt-6-sol gpt-6-astra gpt-5.4 gpt-5.5 gpt-5.6-sol gpt-5.6-terra gpt-5.6-luna gpt-6.1-terra gpt-6.2-sol gpt-6.2-astra",
	"GLM":       "glm-5.2 glm-5.3 glm-5.3-flashx glm-5.3-flash",
	"Qwen":      "qwen3.8-max-0902 qwen3.8-27b qwen3.8-omni-flash qwen3.8-flash",
	"Kimi":      "kimi-k3 kimi-k2.8-preview kimi-k3.1-pro",
	"Hunyuan":   "hy4-preview hy4 hy4-flash",
	"Muse":      "muse-spark-1.2 muse-spark-1.3 muse-wonder muse-ember",
	"MiMo":      "mimo-v2.5 mimo-v2.5-pro mimo-v2.6-flash mimo-v2.6-pro",
	"Step":      "step-5-preview",
	"DeepSeek":  "deepseek-v4.1-flash deepseek-v4-flash deepseek-v4-pro-0813 deepseek-v4-pro deepseek-v4-flash-0731 deepseek-v3.2 deepseek-v4.1-flash-1017",
	"Seed":      "seed-2.0-mini seed-2.0-lite seed-2.1-turbo",
	"MiniMax":   "minimax-m3.1-flash",
	"openPangu": "openpangu-2.0-flash openpangu-2.0-pro",
}

func referenceOf(builtAt string, families map[string]string) *Reference {
	head := &bankHeader{BuiltAt: builtAt}
	for family, ids := range families {
		for _, id := range strings.Fields(ids) {
			head.Models = append(head.Models, Model{ID: id, DisplayName: id, Family: strings.ToLower(family), FamilyName: family})
		}
	}
	return newReference(nil, head, time.Now())
}

func publishedReference() *Reference {
	return referenceOf("2026-11-19T05:26:09.347356+00:00", publishedModels)
}

func TestMatchFindsTheDeclaredModel(t *testing.T) {
	ref := publishedReference()
	if len(ref.models) != 75 {
		t.Fatalf("%d published models, want 75", len(ref.models))
	}
	cases := map[string]string{
		// The exact name, with any letter case.
		"claude-sonnet-4.6":  "claude-sonnet-4.6",
		"GLM-5.3":            "glm-5.3",
		"MiniMax-M3.1-Flash": "minimax-m3.1-flash",
		"gpt-6-astra":        "gpt-6-astra",
		"gpt-4o":             "gpt-4o",
		// Dashes, underscores or spaces in place of dots.
		"claude-sonnet-4-6": "claude-sonnet-4.6",
		"claude-sonnet-5-5": "claude-sonnet-5.5",
		"gemini-3-5-flash":  "gemini-3.5-flash",
		"glm-5-3-flashx":    "glm-5.3-flashx",
		"Claude Opus 4.8":   "claude-opus-4.8",
		"gpt_5_5":           "gpt-5.5",
		// A provider prefix or a variant tag.
		"anthropic/claude-opus-4.8": "claude-opus-4.8",
		"openai/gpt-5.5:free":       "gpt-5.5",
		"google/gemini-3.5-flash":   "gemini-3.5-flash",
		"claude-haiku-4-5@20251001": "claude-haiku-4.5",
		// A snapshot date.
		"claude-sonnet-4-6-20260115": "claude-sonnet-4.6",
		"gpt-5.5-2026-04-01":         "gpt-5.5",
		"gpt-4o-2024-08-06":          "gpt-4o",
		"deepseek-v4-flash-0901":     "deepseek-v4-flash",
		"deepseek-v4.1-flash-0101":   "deepseek-v4.1-flash",
		// A decoration.
		"claude-opus-4-8-thinking": "claude-opus-4.8",
		"gpt-5.5-high":             "gpt-5.5",
		"deepseek-v4-pro-thinking": "deepseek-v4-pro",
		// The bank lists a dated or preview name and a plain one: the exact name wins.
		"deepseek-v4-pro":          "deepseek-v4-pro",
		"deepseek-v4-pro-0813":     "deepseek-v4-pro-0813",
		"deepseek-v4.1-flash-1017": "deepseek-v4.1-flash-1017",
		"hy4":                      "hy4",
		"hy4-preview":              "hy4-preview",
		"gemini-3.5-flash-lite":    "gemini-3.5-flash-lite",
		// The bank lists only a dated or preview name.
		"qwen3.8-max":                 "qwen3.8-max-0902",
		"qwen3.8-max-latest":          "qwen3.8-max-0902",
		"gemini-3.1-pro":              "gemini-3.1-pro-preview",
		"gemini-3.1-pro-preview-0605": "gemini-3.1-pro-preview",
		"kimi-k2.8":                   "kimi-k2.8-preview",
		"step-5":                      "step-5-preview",
	}
	for declared, want := range cases {
		if model, ok := ref.Match(declared); !ok || model.ID != want {
			t.Errorf("%q matched %q (%v), want %q", declared, model.ID, ok, want)
		}
	}
}

func TestMatchLeavesUnlistedModelsUnmatched(t *testing.T) {
	ref := publishedReference()
	for _, declared := range []string{
		"", "   ", "/", "gpt-5", "gpt-5.5-turbo", "gpt-4o-mini", "o3", "llama-4", "claude-opus-4-20250514",
		"claude-opus-4-1-20250805", "claude-sonnet", "sonnet-4.6", "gemini-3.5", "deepseek-chat", "thinking",
	} {
		if model, ok := ref.Match(declared); ok {
			t.Errorf("%q matched %q, want no match", declared, model.ID)
		}
	}
}

func TestMatchRejectsAnAmbiguousName(t *testing.T) {
	ref := referenceOf("build", map[string]string{"X": "x-pro-0101 x-pro-0202 x-lite-preview"})
	if model, ok := ref.Match("x-pro"); ok {
		t.Fatalf("x-pro matched %q although two snapshots are listed", model.ID)
	}
	if model, ok := ref.Match("x-pro-0303"); ok {
		t.Fatalf("x-pro-0303 matched %q although two other snapshots are listed", model.ID)
	}
	if model, ok := ref.Match("x-pro-0202"); !ok || model.ID != "x-pro-0202" {
		t.Fatalf("x-pro-0202 matched %q (%v)", model.ID, ok)
	}
	if model, ok := ref.Match("x-lite"); !ok || model.ID != "x-lite-preview" {
		t.Fatalf("x-lite matched %q (%v)", model.ID, ok)
	}
}

func TestExpectedFollowsTheModeOfTheChannelModel(t *testing.T) {
	ref := publishedReference()
	cases := []struct {
		name  string
		model domain.Model
		want  string
	}{
		{"no mode matches the upstream ID", domain.Model{UpstreamID: "claude-sonnet-4-6"}, "claude-sonnet-4.6"},
		{"auto matches the upstream ID", domain.Model{UpstreamID: "GPT-5.5", FingerprintMode: domain.FingerprintModeAuto}, "gpt-5.5"},
		{"auto ignores an earlier pick", domain.Model{UpstreamID: "gpt-5.5", FingerprintModel: "glm-5.3"}, "gpt-5.5"},
		{"auto without a listed model", domain.Model{UpstreamID: "llama-4"}, ""},
		{"manual uses the pick", domain.Model{UpstreamID: "my-alias", FingerprintMode: domain.FingerprintModeManual, FingerprintModel: "glm-5.3"}, "glm-5.3"},
		{"manual wins over the upstream ID", domain.Model{UpstreamID: "gpt-5.5", FingerprintMode: domain.FingerprintModeManual, FingerprintModel: "glm-5.3"}, "glm-5.3"},
		{"manual pick that left the bank", domain.Model{UpstreamID: "gpt-5.5", FingerprintMode: domain.FingerprintModeManual, FingerprintModel: "gpt-3"}, ""},
		{"manual without a pick", domain.Model{UpstreamID: "gpt-5.5", FingerprintMode: domain.FingerprintModeManual}, ""},
		{"unlisted", domain.Model{UpstreamID: "gpt-5.5", FingerprintMode: domain.FingerprintModeUnlisted, FingerprintModel: "gpt-5.5"}, ""},
	}
	for _, c := range cases {
		if got := ref.Expected(c.model); got != c.want {
			t.Errorf("%s: expected model %q, want %q", c.name, got, c.want)
		}
	}
}
