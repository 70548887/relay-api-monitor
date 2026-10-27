// Package artifact extracts complete generated HTML/SVG and prepares iframe previews.
package artifact

import (
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

var fenceStart = regexp.MustCompile("(?m)^[ \\t]*```([^\\r\\n`]*)\\r?\\n")
var svgStart = regexp.MustCompile(`(?i)<svg(?:\s|>)`)

type candidate struct{ code, kind string }

func fenced(text string) []candidate {
	var result []candidate
	position := 0
	for position < len(text) {
		loc := fenceStart.FindStringSubmatchIndex(text[position:])
		if loc == nil {
			break
		}
		start := position + loc[1]
		language := strings.ToLower(strings.TrimSpace(text[position+loc[2] : position+loc[3]]))
		if fields := strings.Fields(language); len(fields) > 0 {
			language = fields[0]
		}
		// A closing fence is accepted at end of input as well as before a newline.
		end := -1
		next := len(text)
		cursor := start
		for cursor <= len(text) {
			lineEnd := strings.IndexByte(text[cursor:], '\n')
			last := lineEnd < 0
			if last {
				lineEnd = len(text) - cursor
			}
			line := strings.TrimSpace(text[cursor : cursor+lineEnd])
			if line == "```" {
				end = cursor
				next = cursor + lineEnd
				if next < len(text) {
					next++
				}
				break
			}
			if last {
				break
			}
			cursor += lineEnd + 1
		}
		if end < 0 {
			break
		}
		result = append(result, candidate{strings.TrimSpace(text[start:end]), language})
		position = next
	}
	return result
}

// Extract selects the first complete matching fenced block, then tries the whole text.
// HTML and SVG are accepted as actual kinds independently of the preferred kind.
func Extract(text, kind string) (code, actualKind string, err error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = "html"
	}
	if kind != "html" && kind != "svg" {
		return "", "", &Error{Code: "invalid_kind", Message: "Artifact kind must be html or svg"}
	}
	blocks := fenced(text)
	for _, block := range blocks {
		if block.kind == kind || (kind == "html" && (block.kind == "htm" || block.kind == "html5")) {
			if code, actualKind, err = complete(block.code, kind); err == nil {
				return code, actualKind, nil
			}
		}
	}
	for _, block := range blocks {
		if block.kind == "" || block.kind == "xml" || block.kind == "html" || block.kind == "svg" || block.kind == "htm" || block.kind == "html5" {
			if code, actualKind, err = complete(block.code, kind); err == nil {
				return code, actualKind, nil
			}
		}
	}
	if code, actualKind, err = complete(strings.TrimSpace(text), kind); err == nil {
		return code, actualKind, nil
	}
	if strings.Contains(strings.ToLower(text), "<html") || strings.Contains(strings.ToLower(text), "<svg") || strings.Contains(text, "```") {
		return "", "", &Error{Code: "truncated_artifact", Message: "Generated code is incomplete or malformed"}
	}
	return "", "", &Error{Code: "missing_artifact", Message: "Response does not contain a complete HTML or SVG document"}
}

func complete(text, preferred string) (string, string, error) {
	if code, ok := htmlDocument(text); ok {
		return code, "html", nil
	}
	if loc := svgStart.FindStringIndex(text); loc != nil {
		code := strings.TrimSpace(text[loc[0]:])
		if validSVG(code) {
			return code, "svg", nil
		}
	}
	return "", "", errors.New("missing complete document")
}

// Locate actual tokenizer boundaries so HTML-like strings in comments or
// script text cannot select the document start/end or merge two documents.
func htmlDocument(text string) (string, bool) {
	tokenizer := html.NewTokenizer(strings.NewReader(text))
	position, start, doctype, doctypeEnd := 0, -1, -1, -1
	for {
		typ := tokenizer.Next()
		begin := position
		position += len(tokenizer.Raw())
		switch typ {
		case html.ErrorToken:
			return "", false
		case html.DoctypeToken:
			if strings.EqualFold(tokenizer.Token().Data, "html") {
				doctype, doctypeEnd = begin, position
			}
		case html.StartTagToken:
			name, _ := tokenizer.TagName()
			if string(name) == "html" {
				// A second real root before the first closes starts a new
				// candidate. Providers can abandon a partial document and
				// restart it; merging the prefix into the complete document
				// can trap all of its SVG under unfinished defs/gradients.
				start = begin
				if doctype >= 0 && strings.TrimSpace(text[doctypeEnd:begin]) == "" {
					start = doctype
				}
			}
		case html.EndTagToken:
			name, _ := tokenizer.TagName()
			if string(name) == "html" && start >= 0 {
				return text[start:position], true
			}
		}
	}
}

func validSVG(code string) bool {
	decoder := xml.NewDecoder(strings.NewReader(code))
	depth, roots := 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return depth == 0 && roots == 1
		}
		if err != nil {
			return false
		}
		switch value := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if value.Name.Local != "svg" {
					return false
				}
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(value)) != "" {
				return false
			}
		case xml.Directive:
			return false
		}
	}
}

// Preview returns an isolated srcdoc document. The caller must also apply iframe
// sandbox="allow-scripts" without allow-same-origin. CSP is defense in depth;
// browsers may still permit some navigation or resource behavior.
func Preview(code, kind string) string {
	const csp = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:; connect-src 'none'; frame-src 'none'; child-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"
	if kind == "svg" {
		code = `<!doctype html><html><head><style>html,body{margin:0;min-height:100%;background:white}svg{display:block;max-width:100%;height:auto;margin:auto}</style></head><body>` + code + `</body></html>`
	}
	document, err := html.Parse(strings.NewReader(code))
	if err != nil {
		return `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="` + csp + `"></head><body>Preview could not be prepared.</body></html>`
	}
	scrub(document)
	var root, head, body *html.Node
	for child := document.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == "html" {
			root = child
			break
		}
	}
	if root != nil {
		for child := root.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode {
				switch child.Data {
				case "head":
					head = child
				case "body":
					body = child
				}
			}
		}
	}
	var out strings.Builder
	out.WriteString(`<!doctype html>`)
	opening := html.Token{Type: html.StartTagToken, Data: "html"}
	if root != nil {
		opening.Attr = root.Attr
	}
	out.WriteString(opening.String())
	out.WriteString(`<head><meta http-equiv="Content-Security-Policy" content="` + csp + `"><meta charset="utf-8"><meta name="referrer" content="no-referrer">`)
	if head != nil {
		for child := head.FirstChild; child != nil; child = child.NextSibling {
			_ = html.Render(&out, child)
		}
	}
	out.WriteString(`</head>`)
	if body != nil {
		_ = html.Render(&out, body)
	} else {
		out.WriteString(`<body></body>`)
	}
	out.WriteString(`</html>`)
	return out.String()
}

func scrub(parent *html.Node) {
	for child := parent.FirstChild; child != nil; {
		next := child.NextSibling
		drop := child.Type == html.ElementNode && strings.EqualFold(child.Data, "base")
		if child.Type == html.ElementNode && strings.EqualFold(child.Data, "meta") {
			for _, attribute := range child.Attr {
				if strings.EqualFold(attribute.Key, "http-equiv") {
					value := strings.TrimSpace(attribute.Val)
					if strings.EqualFold(value, "refresh") || strings.EqualFold(value, "content-security-policy") {
						drop = true
					}
				}
			}
		}
		if drop {
			parent.RemoveChild(child)
		} else {
			scrub(child)
		}
		child = next
	}
}
