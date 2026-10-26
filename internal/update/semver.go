package update

import "strings"

// SemVer 2.0 comparison uses decimal strings, avoiding integer overflow even
// for syntactically valid arbitrarily large version components.
type semanticVersion struct {
	core [3]string
	pre  []string
}

func parseVersion(value string) (semanticVersion, bool) {
	var out semanticVersion
	if strings.HasPrefix(value, "v") {
		value = value[1:]
	}
	if value == "" {
		return out, false
	}
	if i := strings.IndexByte(value, '+'); i >= 0 {
		if !validIdentifiers(value[i+1:], false) {
			return out, false
		}
		value = value[:i]
	}
	if i := strings.IndexByte(value, '-'); i >= 0 {
		if !validIdentifiers(value[i+1:], true) {
			return out, false
		}
		out.pre = strings.Split(value[i+1:], ".")
		value = value[:i]
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		if !numeric(part) || (len(part) > 1 && part[0] == '0') {
			return out, false
		}
		out.core[i] = part
	}
	return out, true
}

func validIdentifiers(value string, prerelease bool) bool {
	for _, part := range strings.Split(value, ".") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
		if prerelease && numeric(part) && len(part) > 1 && part[0] == '0' {
			return false
		}
	}
	return true
}

func numeric(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func decimalCompare(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

func compareVersions(a, b semanticVersion) int {
	for i := range a.core {
		if n := decimalCompare(a.core[i], b.core[i]); n != 0 {
			return n
		}
	}
	if len(a.pre) == 0 && len(b.pre) == 0 {
		return 0
	}
	if len(a.pre) == 0 {
		return 1
	}
	if len(b.pre) == 0 {
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		nx, ny := numeric(x), numeric(y)
		var n int
		switch {
		case nx && ny:
			n = decimalCompare(x, y)
		case nx:
			n = -1
		case ny:
			n = 1
		default:
			n = strings.Compare(x, y)
		}
		if n != 0 {
			return n
		}
	}
	if len(a.pre) < len(b.pre) {
		return -1
	}
	if len(a.pre) > len(b.pre) {
		return 1
	}
	return 0
}

func isNewer(latest, current string) bool {
	a, ok := parseVersion(latest)
	if !ok {
		return false
	}
	b, ok := parseVersion(current)
	if !ok {
		return true
	}
	return compareVersions(a, b) > 0
}

// IsNewerVersion compares two explicit SemVer versions. Unlike discovery from
// a development build, invalid versions never establish an ordering.
func IsNewerVersion(latest, current string) bool {
	a, ok := parseVersion(latest)
	if !ok {
		return false
	}
	b, ok := parseVersion(current)
	return ok && compareVersions(a, b) > 0
}

func isSameVersion(a, b string) bool {
	x, ok := parseVersion(a)
	if !ok {
		return false
	}
	y, ok := parseVersion(b)
	return ok && compareVersions(x, y) == 0
}
