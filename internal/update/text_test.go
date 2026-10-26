package update

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSafeTextTruncatesAtUTF8BoundaryAfterRedaction(t *testing.T) {
	_, m := newFixture(t)
	m.cfg.GitHubToken = "secret-text-fixture-token"
	m.cfg.Proxy = "http://user:secret-text-fixture-password@proxy.example"
	secretPrefix := "secret-text-fixture-token secret-text-fixture-password Bearer secret-text-fixture-bearer https://u:p@github.com/path?sig=secret-text-fixture-signature "
	prefix := m.safeText(secretPrefix)
	for _, text := range []string{"数据更新完成", "🙂更新完成"} {
		// The byte cap falls inside the first rune for each character width.
		input := secretPrefix + strings.Repeat("a", 2044-len(prefix)) + text + strings.Repeat("后续发布说明", 400)
		got := m.safeText(input)
		if !utf8.ValidString(got) || len(got) > 2048 || !strings.HasSuffix(got, "…") || strings.ContainsRune(got, utf8.RuneError) {
			t.Fatalf("invalid UTF-8 truncation: length=%d valid=%v", len(got), utf8.ValidString(got))
		}
		for _, secret := range []string{"secret-text-fixture-token", "secret-text-fixture-password", "secret-text-fixture-bearer", "secret-text-fixture-signature", "u:p@"} {
			if strings.Contains(got, secret) {
				t.Fatalf("truncation exposed redacted text: %s", secret)
			}
		}
	}
}
