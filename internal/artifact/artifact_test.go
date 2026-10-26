package artifact

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestExtractCompleteBlocks(t *testing.T) {
	for _, test := range []struct{ name, text, kind, wantKind, want string }{
		{"html unfenced", "Here is the document\n<!DOCTYPE html><html><head><title>x</title></head><body>鹈鹕</body></html>\nDone.", "html", "html", "鹈鹕"},
		{"svg unfenced", `<svg xmlns="http://www.w3.org/2000/svg"><circle r="5"/></svg>`, "svg", "svg", "circle"},
		{"preferred fence", "```svg\n<svg/>\n```\n```html\n<html><body>preferred</body></html>\n```", "html", "html", "preferred"},
		{"multiple matching blocks", "```html\n<html><body>first</body></html>\n```\n```html\n<html><body>second</body></html>\n```", "html", "html", "first"},
		{"skip incomplete block", "```html\n<html><body>bad\n```\n```html\n<html><body>good</body></html>\n```", "html", "html", "good"},
		{"svg fallback", "```xml\n<svg><text>fallback</text></svg>\n```", "html", "svg", "fallback"},
		{"script closing html string", `<html><body><script>let x="</html>";</script>actual</body></html>`, "html", "html", "actual"},
		{"comment fake root", `<!-- <html>fake</html> --><!doctype html><html><body>actual</body></html>`, "html", "html", "actual"},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, kind, err := Extract(test.text, test.kind)
			if err != nil || kind != test.wantKind || !strings.Contains(code, test.want) {
				t.Fatalf("%q %q %v", code, kind, err)
			}
		})
	}
}

func TestExtractStopsAtFirstActualHTMLDocument(t *testing.T) {
	input := `<!-- <html>fake</html> --><!doctype html><html><body><script>const end="</html>";</script>first</body></html><html><body>second</body></html>`
	code, kind, err := Extract(input, "html")
	if err != nil || kind != "html" || !strings.HasPrefix(strings.ToLower(code), "<!doctype html>") || strings.Contains(code, "fake") || strings.Contains(code, "second") || !strings.Contains(code, "first") {
		t.Fatalf("incorrect document boundaries: %q %q %v", code, kind, err)
	}
}

func TestExtractRejectsTruncated(t *testing.T) {
	for _, text := range []string{`<html><body>unfinished`, `<svg><g></svg>`, `<svg><script>broken`, `<html><script>var x="</html>";`, "```html\n<html>unfinished", "plain response"} {
		if _, _, err := Extract(text, "html"); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
}

func TestPreviewCSPAndSanitization(t *testing.T) {
	code := `<!doctype html><html><head><base href="https://bad.example"><meta HTTP-EQUIV=" refresh " content="0;url=https://bad.example"><meta http-equiv="Content-Security-Policy" content="default-src *"><style>.x{animation:a 1s infinite}@keyframes a{to{opacity:0}}</style></head><body><svg><circle r="4"/></svg><script>document.querySelector('circle').setAttribute('r','8')</script></body></html>`
	preview := Preview(code, "html")
	if !strings.HasPrefix(preview, `<!doctype html><html><head><meta http-equiv="Content-Security-Policy"`) {
		t.Fatal("CSP is not first")
	}
	for _, value := range []string{"connect-src 'none'", "frame-src 'none'", "base-uri 'none'", "form-action 'none'", "@keyframes a", "document.querySelector"} {
		if !strings.Contains(preview, value) {
			t.Errorf("missing %q", value)
		}
	}
	tokenizer := html.NewTokenizer(strings.NewReader(preview))
	policies := 0
	for {
		typ := tokenizer.Next()
		if typ == html.ErrorToken {
			break
		}
		if typ != html.StartTagToken && typ != html.SelfClosingTagToken {
			continue
		}
		token := tokenizer.Token()
		if token.Data == "base" {
			t.Error("base preserved")
		}
		if token.Data == "meta" {
			for _, a := range token.Attr {
				if strings.EqualFold(a.Key, "http-equiv") {
					if strings.EqualFold(strings.TrimSpace(a.Val), "refresh") {
						t.Error("refresh preserved")
					}
					if strings.EqualFold(a.Val, "Content-Security-Policy") {
						policies++
					}
				}
			}
		}
	}
	if policies != 1 {
		t.Fatalf("policies=%d", policies)
	}
	if !strings.Contains(code, "bad.example") {
		t.Fatal("original input changed")
	}
}

func TestPreviewSVGAnimation(t *testing.T) {
	code := `<svg xmlns="http://www.w3.org/2000/svg"><style>circle{fill:red}</style><circle r="5"><animate attributeName="r" values="5;10;5" dur="1s" repeatCount="indefinite"/></circle><script>console.log('animation')</script></svg>`
	preview := Preview(code, "svg")
	if !strings.Contains(preview, "<animate") || !strings.Contains(preview, "<script>") || !strings.Contains(preview, "circle{fill:red}") {
		t.Fatal("animation removed")
	}
}

func TestPreviewPreservesBodyAnimationAttributes(t *testing.T) {
	preview := Preview(`<html lang="en"><head></head><body style="background:black" onload="document.body.dataset.started='yes'"><div>animation</div></body></html>`, "html")
	if !strings.Contains(preview, `style="background:black"`) || !strings.Contains(preview, "onload=") || !strings.Contains(preview, `lang="en"`) {
		t.Fatal("body or document animation attributes lost")
	}
}
