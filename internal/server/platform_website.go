package server

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// Website links are display-only; validating one never contacts its host.
func normalizePlatformWebsite(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	invalid := errors.New("官网地址须为完整的 HTTP 或 HTTPS 链接，且不能包含账号密码")
	if len(value) > 2048 || strings.ContainsRune(value, '\\') || strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return "", invalid
	}
	website, err := url.Parse(value)
	if err != nil || website.Hostname() == "" || website.User != nil || website.Opaque != "" {
		return "", invalid
	}
	website.Scheme = strings.ToLower(website.Scheme)
	if website.Scheme != "https" && website.Scheme != "http" {
		return "", invalid
	}
	if port := website.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", invalid
		}
	}
	return website.String(), nil
}
