package fingerprint

import (
	"regexp"
	"strings"

	"relaylab/internal/domain"
)

// decorations are name parts that providers and relays append without naming another model.
var decorations = map[string]bool{
	"thinking": true, "think": true, "nothinking": true, "reasoning": true,
	"high": true, "medium": true, "low": true, "xhigh": true, "minimal": true,
	"latest": true, "preview": true, "exp": true, "experimental": true, "beta": true,
	"free": true, "official": true, "search": true, "online": true,
}

var dashedDate = regexp.MustCompile(`[-_]20\d{2}-\d{2}-\d{2}`)

// nameParts splits a model name into lower-case parts. Dots, dashes, underscores and spaces separate parts alike, so
// claude-sonnet-4-6 and Claude Sonnet 4.6 give the same parts. A provider prefix (openai/) and a variant tag (:free,
// @20250929) are dropped.
func nameParts(name string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.IndexAny(name, ":@"); i >= 0 {
		name = name[:i]
	}
	name = dashedDate.ReplaceAllString(name, "")
	return strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '.' || r == '_' || r == ' ' })
}

// isDate reports whether a part is a snapshot date such as 20250929, 250929 or 0813.
func isDate(part string) bool {
	if n := len(part); n != 4 && n != 6 && n != 8 {
		return false
	}
	for _, c := range part {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// coreName joins the parts without the snapshot dates and decorations that follow the first part.
func coreName(parts []string) string {
	core := make([]string, 0, len(parts))
	for i, part := range parts {
		if i > 0 && (decorations[part] || isDate(part)) {
			continue
		}
		core = append(core, part)
	}
	return strings.Join(core, "-")
}

// Match finds the bank model that a declared model name refers to. Beyond the exact name it accepts a provider
// prefix, dashes in place of dots, a snapshot date, and a decoration such as -thinking. A name that could mean
// several bank models matches none, and so does a name the bank does not list.
func (r *Reference) Match(declared string) (Model, bool) {
	parts := nameParts(declared)
	if len(parts) == 0 {
		return Model{}, false
	}
	name, core := strings.Join(parts, "-"), coreName(parts)
	for _, wanted := range []string{name, core} {
		for _, model := range r.models {
			if strings.Join(nameParts(model.ID), "-") == wanted {
				return model, true
			}
		}
	}
	var found []Model
	for _, model := range r.models {
		if coreName(nameParts(model.ID)) == core {
			found = append(found, model)
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return Model{}, false
}

// Expected returns the bank ID of the reference model that a channel model is compared with: the model an
// administrator picked for it, or else the model its upstream ID names. It is empty when an administrator marked the
// model as unlisted or the bank lists no such model.
func (r *Reference) Expected(m domain.Model) string {
	var model Model
	switch m.FingerprintMode {
	case domain.FingerprintModeUnlisted:
	case domain.FingerprintModeManual:
		model, _ = r.Model(m.FingerprintModel)
	default:
		model, _ = r.Match(m.UpstreamID)
	}
	return model.ID
}
