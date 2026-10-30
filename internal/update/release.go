package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

type githubAsset struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	BrowserURL string `json:"browser_download_url"`
	APIURL     string `json:"url"`
}
type githubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	HTMLURL     string        `json:"html_url"`
	PublishedAt string        `json:"published_at"`
	Body        string        `json:"body"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	Assets      []githubAsset `json:"assets"`
}
type manifest struct {
	Version           string          `json:"version"`
	Commit            string          `json:"commit"`
	BuildTime         string          `json:"build_time"`
	LauncherProtocol  int             `json:"launcher_protocol"`
	UsageGuard        int             `json:"usage_guard"`
	DatabaseSchema    int             `json:"database_schema"`
	MinDatabaseSchema int             `json:"min_database_schema"`
	Assets            []manifestAsset `json:"assets"`
}
type manifestAsset struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func (m *Manager) fetchLatest(ctx context.Context) (Release, error) {
	return m.fetchRelease(ctx, "")
}

// A recovered download resolves its saved tag rather than whichever release is
// latest now. URLs are deliberately transient; identity is checked separately.
func (m *Manager) fetchRelease(ctx context.Context, tag string) (Release, error) {
	base := m.apiBase
	if base == "" {
		base = "https://api.github.com"
	}
	releasePath := "latest"
	if tag != "" {
		releasePath = "tags/" + url.PathEscape(tag)
	}
	endpoint := strings.TrimRight(base, "/") + "/repos/" + url.PathEscape(m.cfg.GitHubOwner) + "/" + url.PathEscape(m.cfg.GitHubRepo) + "/releases/" + releasePath
	resp, err := m.request(ctx, endpoint, "application/vnd.github+json")
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	var raw githubRelease
	if decodeBounded(resp.Body, &raw, false) != nil {
		return Release{}, errors.New("update: invalid GitHub release response")
	}
	if tag != "" && raw.TagName != tag {
		return Release{}, ErrReleaseChanged
	}
	v, valid := parseVersion(raw.TagName)
	if !valid {
		return Release{}, errors.New("update: release tag is not a semantic version")
	}
	if raw.Draft || raw.Prerelease || len(v.pre) > 0 {
		return Release{}, ErrNoRelease
	}
	if !validReleaseURL(raw.HTMLURL) {
		return Release{}, errors.New("update: invalid GitHub release page URL")
	}
	published, err := time.Parse(time.RFC3339, raw.PublishedAt)
	if err != nil {
		return Release{}, errors.New("update: invalid release publication time")
	}
	assets := make(map[string]githubAsset, len(raw.Assets))
	for _, a := range raw.Assets {
		if a.Name == "" || assets[a.Name].Name != "" {
			return Release{}, fmt.Errorf("%w: duplicate or empty release asset", ErrInvalidManifest)
		}
		assets[a.Name] = a
	}
	manifestAsset, ok := assets["manifest.json"]
	if !ok {
		return Release{}, fmt.Errorf("%w: manifest.json is missing", ErrInvalidManifest)
	}
	manifestURL, err := m.assetURL(manifestAsset)
	if err != nil {
		return Release{}, err
	}
	meta, err := m.fetchManifest(ctx, manifestURL, raw.TagName)
	if err != nil {
		return Release{}, err
	}
	rel := Release{TagName: raw.TagName, Name: m.safeText(raw.Name), URL: raw.HTMLURL, PublishedAt: published, Body: m.safeText(raw.Body), Assets: []Asset{}, Commit: meta.Commit, BuildTime: meta.BuildTime, LauncherProtocol: meta.LauncherProtocol, UsageGuard: meta.UsageGuard, DatabaseSchema: meta.DatabaseSchema, MinDatabaseSchema: meta.MinDatabaseSchema}
	for _, item := range meta.Assets {
		a, exists := assets[item.Name]
		if !exists || a.Size != item.Size {
			return Release{}, fmt.Errorf("%w: release asset and manifest size differ", ErrInvalidManifest)
		}
		downloadURL, err := m.assetURL(a)
		if err != nil {
			return Release{}, err
		}
		rel.Assets = append(rel.Assets, Asset{Name: item.Name, OS: item.OS, Arch: item.Arch, Size: item.Size, SHA256: strings.ToLower(item.SHA256), DownloadURL: downloadURL})
	}
	return rel, nil
}

func (m *Manager) assetURL(a githubAsset) (string, error) {
	raw := a.BrowserURL
	if m.cfg.GitHubToken != "" {
		raw = a.APIURL
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() != "api.github.com" || !strings.HasPrefix(u.Path, "/repos/") || !strings.Contains(u.Path, "/releases/assets/") {
			// An explicit package-private test endpoint still exercises transport.
			if !m.isTestURL(u) {
				return "", errors.New("update: private release asset API URL is required")
			}
		}
	}
	u, err := url.Parse(raw)
	if err != nil || !m.allowedURL(u) {
		return "", errors.New("update: asset URL is outside allowed HTTPS GitHub hosts")
	}
	return raw, nil
}

func (m *Manager) fetchManifest(ctx context.Context, rawURL, tag string) (manifest, error) {
	resp, err := m.request(ctx, rawURL, "application/octet-stream")
	if err != nil {
		return manifest{}, err
	}
	defer resp.Body.Close()
	var meta manifest
	if decodeBounded(resp.Body, &meta, true) != nil {
		return manifest{}, fmt.Errorf("%w: malformed JSON", ErrInvalidManifest)
	}
	if !isSameVersion(meta.Version, tag) || !commitPattern.MatchString(meta.Commit) {
		return manifest{}, fmt.Errorf("%w: version or commit mismatch", ErrInvalidManifest)
	}
	// Release version is represented without a leading v in the manifest.
	if strings.TrimPrefix(tag, "v") != meta.Version {
		return manifest{}, fmt.Errorf("%w: version does not exactly match tag", ErrInvalidManifest)
	}
	if _, err = time.Parse(time.RFC3339, meta.BuildTime); err != nil {
		return manifest{}, fmt.Errorf("%w: invalid build_time", ErrInvalidManifest)
	}
	// Discovery still validates the manifest's shape and target identity. A
	// container installs a complete image, so changes to the binary updater's
	// protocol or schema contract must not hide an otherwise valid release.
	if meta.LauncherProtocol <= 0 || meta.DatabaseSchema <= 0 || meta.MinDatabaseSchema <= 0 || meta.MinDatabaseSchema > meta.DatabaseSchema {
		return manifest{}, fmt.Errorf("%w: invalid launcher protocol or database schema", ErrInvalidManifest)
	}
	if meta.UsageGuard < 0 {
		return manifest{}, fmt.Errorf("%w: invalid usage guard", ErrInvalidManifest)
	}
	if m.cfg.InstallMode != "container" && (meta.LauncherProtocol != LauncherProtocol || (isNewer(tag, m.build.Version) && (meta.DatabaseSchema < DatabaseSchema || meta.MinDatabaseSchema > DatabaseSchema))) {
		return manifest{}, fmt.Errorf("%w: launcher protocol or database schema is incompatible", ErrInvalidManifest)
	}
	if m.cfg.InstallMode != "container" && (meta.UsageGuard > RequiredUsageGuard || (isNewer(tag, m.build.Version) && meta.UsageGuard != RequiredUsageGuard)) {
		return manifest{}, fmt.Errorf("%w: usage guard is incompatible; use a full installation package", ErrInvalidManifest)
	}
	if len(meta.Assets) == 0 {
		return manifest{}, fmt.Errorf("%w: assets are empty", ErrInvalidManifest)
	}
	seen := make(map[string]bool, len(meta.Assets))
	for _, a := range meta.Assets {
		if seen[a.Name] || !supportedPlatform(a.OS, a.Arch) || !validManifestAssetName(a, meta.Version) || a.Size <= 0 || a.Size > maxDownloadSize || !digestPattern.MatchString(a.SHA256) {
			return manifest{}, fmt.Errorf("%w: invalid, duplicate or unsafe asset", ErrInvalidManifest)
		}
		seen[a.Name] = true
	}
	return meta, nil
}

func decodeBounded(reader io.Reader, dst any, strict bool) error {
	b, err := io.ReadAll(io.LimitReader(reader, maxReleaseBody+1))
	if err != nil {
		return err
	}
	if len(b) > maxReleaseBody {
		return errors.New("response exceeds size limit")
	}
	if strict {
		if err = rejectDuplicateJSONKeys(b); err != nil {
			return err
		}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err = dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var visit func() error
	visit = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate JSON object field")
				}
				seen[name] = true
				if err = visit(); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err = visit(); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, err = dec.Token()
		return err
	}
	if err := visit(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func (m *Manager) request(ctx context.Context, rawURL, accept string) (*http.Response, error) {
	return m.requestUsing(ctx, rawURL, accept, m.client)
}

func (m *Manager) requestUsing(ctx context.Context, rawURL, accept string, client *http.Client) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !m.allowedURL(u) {
		return nil, errors.New("update: URL is outside allowed HTTPS GitHub hosts")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("update: invalid HTTP request")
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "RelayLab-Updater/2")
	if m.cfg.GitHubToken != "" && (u.Hostname() == "api.github.com" || m.isTestURL(u)) {
		req.Header.Set("Authorization", "Bearer "+m.cfg.GitHubToken)
	}
	// Only read-only GitHub GET requests are retried, before a response is
	// accepted. This path is never shared with model generation requests.
	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		resp, err = client.Do(req.Clone(ctx))
		if err == nil || ctx.Err() != nil || !retryableGitHubTransport(err) || attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, m.safeError(ctx.Err())
		case <-timer.C:
		}
	}
	if err != nil {
		return nil, m.safeError(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, fmt.Errorf("update: GitHub returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func retryableGitHubTransport(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}
func (m *Manager) checkRedirect(req *http.Request, via []*http.Request) error {
	if !m.allowedURL(req.URL) {
		return errors.New("update: redirect outside allowed HTTPS GitHub hosts is blocked")
	}
	if len(via) > 6 {
		return errors.New("update: too many redirects")
	}
	if req.URL.Hostname() != "api.github.com" {
		req.Header.Del("Authorization")
	}
	return nil
}
func (m *Manager) isTestURL(u *url.URL) bool {
	if u == nil || m.apiBase == "" {
		return false
	}
	base, err := url.Parse(m.apiBase)
	return err == nil && u.Scheme == base.Scheme && u.Host == base.Host && u.User == nil
}
func (m *Manager) allowedURL(u *url.URL) bool {
	if u == nil || u.User != nil || u.Fragment != "" {
		return false
	}
	if m.isTestURL(u) {
		return true
	}
	return u.Scheme == "https" && u.Port() == "" && allowedGitHubHost(u.Hostname())
}
func allowedGitHubHost(host string) bool {
	switch host {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}
func validReleaseURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && strings.Contains(u.Path, "/releases/")
}
func platformAssetName(goos, goarch string) string {
	name := "relaylab-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}
func supportedPlatform(goos, goarch string) bool {
	switch goos {
	case "windows":
		return goarch == "amd64" || goarch == "arm64" || goarch == "386"
	case "linux":
		return goarch == "amd64" || goarch == "arm64" || goarch == "386" || goarch == "arm"
	case "darwin":
		return goarch == "amd64" || goarch == "arm64"
	}
	return false
}
func validManifestAssetName(a manifestAsset, version string) bool {
	core := platformAssetName(a.OS, a.Arch)
	launcher := strings.Replace(core, "relaylab-", "relaylab-launcher-", 1)
	archive := "relaylab-" + a.OS + "-" + a.Arch + "-" + version
	if a.OS == "windows" {
		archive += ".zip"
	} else {
		archive += ".tar.gz"
	}
	return a.Name == core || a.Name == launcher || a.Name == archive
}
func selectAsset(assets []Asset, goos, goarch string) (Asset, bool) {
	name := platformAssetName(goos, goarch)
	for _, a := range assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}
