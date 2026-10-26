// Package update implements the GitHub Release side of Relay Lab updates.
// Process replacement is intentionally delegated to the launcher; this package
// only discovers, downloads, verifies and journals update artifacts.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"relaylab/internal/config"
)

const (
	stateFileName   = "update-state.json"
	maxReleaseBody  = 4 << 20
	maxDownloadSize = 2 << 30
)

var semverPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:[-+][0-9A-Za-z.-]+)?$`)
var repoPartPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var credentialURLPattern = regexp.MustCompile(`(?i)(https?://)([^/@\s]+)@`)
var bearerPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s,;]+`)

var (
	ErrDisabled            = errors.New("update: updater is disabled")
	ErrNoRelease           = errors.New("update: no compatible release found")
	ErrApplyUnavailable    = errors.New("update: launcher is required to apply an update")
	ErrRollbackUnavailable = errors.New("update: launcher is required to roll back an update")
)

type BuildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

type Asset struct {
	Name        string `json:"name"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256,omitempty"`
	DownloadURL string `json:"-"`
	Verified    bool   `json:"verified"`
	LocalPath   string `json:"local_path,omitempty"`
}

type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"published_at"`
	Body        string    `json:"body"`
	Assets      []Asset   `json:"assets"`
}

type UpdateInfo struct {
	CurrentVersion string     `json:"current_version"`
	LatestVersion  string     `json:"latest_version"`
	ReleaseURL     string     `json:"release_url"`
	PublishedAt    *time.Time `json:"published_at,omitempty"`
	AssetName      string     `json:"asset_name"`
	AssetSize      int64      `json:"asset_size"`
	SHA256         string     `json:"sha256,omitempty"`
	Compatible     bool       `json:"compatible"`
	Available      bool       `json:"available"`
	Notes          string     `json:"notes,omitempty"`
}

type Operation struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Version   string    `json:"version,omitempty"`
	Progress  int       `json:"progress"`
	ErrorCode string    `json:"error_code,omitempty"`
	Message   string    `json:"message,omitempty"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type HistoryEntry struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Version   string    `json:"version,omitempty"`
	Message   string    `json:"message,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Snapshot struct {
	Enabled    bool           `json:"enabled"`
	Repository string         `json:"repository"`
	Current    BuildInfo      `json:"current"`
	Latest     *UpdateInfo    `json:"latest,omitempty"`
	Release    *Release       `json:"release,omitempty"`
	State      string         `json:"state"`
	Operation  *Operation     `json:"operation,omitempty"`
	StagedPath string         `json:"staged_path,omitempty"`
	History    []HistoryEntry `json:"history"`
	CheckedAt  *time.Time     `json:"checked_at,omitempty"`
}

type stateJournal struct {
	Latest     *UpdateInfo    `json:"latest,omitempty"`
	Release    *Release       `json:"release,omitempty"`
	State      string         `json:"state"`
	Operation  *Operation     `json:"operation,omitempty"`
	StagedPath string         `json:"staged_path,omitempty"`
	History    []HistoryEntry `json:"history"`
	CheckedAt  *time.Time     `json:"checked_at,omitempty"`
}

type Manager struct {
	mu       sync.Mutex
	cfg      config.UpdateConfig
	dataDir  string
	build    BuildInfo
	client   *http.Client
	apiBase  string
	state    stateJournal
	applier  func(context.Context, string, UpdateInfo) error
	rollback func(context.Context) error
}

func New(cfg config.UpdateConfig, dataDir string, build BuildInfo) (*Manager, error) {
	if dataDir == "" {
		return nil, errors.New("update: data directory is required")
	}
	if cfg.GitHubOwner == "" {
		cfg.GitHubOwner = "70548887"
	}
	if cfg.GitHubRepo == "" {
		cfg.GitHubRepo = "relay-api-monitor"
	}
	if !repoPartPattern.MatchString(cfg.GitHubOwner) || !repoPartPattern.MatchString(cfg.GitHubRepo) {
		return nil, errors.New("update: invalid GitHub repository")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(cfg.Proxy) != "" {
		u, err := url.Parse(cfg.Proxy)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return nil, errors.New("update: invalid proxy URL")
		}
		transport.Proxy = http.ProxyURL(u)
	}
	m := &Manager{cfg: cfg, dataDir: dataDir, build: build, client: &http.Client{Transport: transport, Timeout: 2 * time.Minute}}
	m.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !allowedGitHubHost(req.URL.Hostname()) {
			return errors.New("update: redirect outside GitHub is blocked")
		}
		if len(via) > 6 {
			return errors.New("update: too many redirects")
		}
		return nil
	}
	m.state = stateJournal{State: "idle", History: []HistoryEntry{}}
	_ = m.load()
	return m, nil
}

func (m *Manager) SetApplier(fn func(context.Context, string, UpdateInfo) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applier = fn
}
func (m *Manager) SetRollback(fn func(context.Context) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollback = fn
}
func (m *Manager) Config() config.UpdateConfig { m.mu.Lock(); defer m.mu.Unlock(); return m.cfg }

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *UpdateInfo
	if m.state.Latest != nil {
		v := *m.state.Latest
		latest = &v
	}
	var release *Release
	if m.state.Release != nil {
		v := *m.state.Release
		v.Assets = append([]Asset(nil), m.state.Release.Assets...)
		release = &v
	}
	var op *Operation
	if m.state.Operation != nil {
		v := *m.state.Operation
		op = &v
	}
	history := append([]HistoryEntry(nil), m.state.History...)
	var checkedAt *time.Time
	if m.state.CheckedAt != nil {
		v := *m.state.CheckedAt
		checkedAt = &v
	}
	return Snapshot{Enabled: m.cfg.Enabled, Repository: m.cfg.GitHubOwner + "/" + m.cfg.GitHubRepo, Current: m.build, Latest: latest, Release: release, State: m.state.State, Operation: op, StagedPath: m.state.StagedPath, History: history, CheckedAt: checkedAt}
}

func (m *Manager) Check(ctx context.Context) (Snapshot, error) {
	m.mu.Lock()
	if !m.cfg.Enabled {
		m.mu.Unlock()
		return m.Snapshot(), ErrDisabled
	}
	if m.state.Operation != nil && (m.state.State == "checking" || m.state.State == "downloading") {
		m.mu.Unlock()
		return m.Snapshot(), errors.New("update: another operation is active")
	}
	op := Operation{ID: randomID(), State: "checking", Progress: 0, StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	m.state.Operation = &op
	m.state.State = "checking"
	m.persistLocked()
	m.mu.Unlock()
	rel, err := m.fetchLatest(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	m.state.CheckedAt = &now
	if err != nil {
		m.failLocked(err)
		return m.snapshotLocked(), err
	}
	asset, ok := selectAsset(rel.Assets, runtime.GOOS, runtime.GOARCH)
	if !ok {
		m.state.Latest = nil
		m.state.Release = &rel
		m.state.State = "idle"
		m.completeLocked("没有匹配当前系统和架构的安装包")
		return m.snapshotLocked(), ErrNoRelease
	}
	info := makeInfo(m.build, rel, asset)
	info.Available = isNewer(rel.TagName, m.build.Version)
	info.Compatible = true
	m.state.Release = &rel
	m.state.Latest = &info
	if info.Available {
		m.state.State = "available"
	} else {
		m.state.State = "idle"
	}
	m.completeLocked("检查完成")
	return m.snapshotLocked(), nil
}

func (m *Manager) Download(ctx context.Context) (Snapshot, error) {
	m.mu.Lock()
	if !m.cfg.Enabled {
		m.mu.Unlock()
		return m.Snapshot(), ErrDisabled
	}
	if m.state.Release == nil || m.state.Latest == nil {
		m.mu.Unlock()
		if _, err := m.Check(ctx); err != nil {
			return m.Snapshot(), err
		}
		m.mu.Lock()
	}
	if m.state.Operation != nil && m.state.State == "downloading" {
		m.mu.Unlock()
		return m.Snapshot(), errors.New("update: another operation is active")
	}
	rel := *m.state.Release
	info := *m.state.Latest
	asset, ok := selectAsset(rel.Assets, runtime.GOOS, runtime.GOARCH)
	if !ok {
		m.mu.Unlock()
		return m.Snapshot(), ErrNoRelease
	}
	op := Operation{ID: randomID(), State: "downloading", Version: rel.TagName, StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	m.state.Operation = &op
	m.state.State = "downloading"
	m.state.StagedPath = ""
	m.persistLocked()
	m.mu.Unlock()
	if !info.Available {
		m.mu.Lock()
		m.state.State = "idle"
		m.completeLocked("当前已是最新版本")
		m.persistLocked()
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, ErrNoRelease
	}
	versionDir := filepath.Join(m.dataDir, "updates", safeVersion(rel.TagName))
	if err := os.MkdirAll(versionDir, 0700); err != nil {
		m.mu.Lock()
		m.failLocked(err)
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, err
	}
	tmp, err := os.CreateTemp(versionDir, ".download-*")
	if err != nil {
		m.mu.Lock()
		m.failLocked(err)
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	resp, err := m.request(ctx, asset.DownloadURL, "application/octet-stream")
	if resp != nil {
		defer resp.Body.Close()
	}
	if err == nil {
		if resp.ContentLength > maxDownloadSize {
			err = errors.New("update: download exceeds size limit")
		}
	}
	var n int64
	var sum string
	if err == nil {
		h := sha256.New()
		writer := io.Writer(io.MultiWriter(tmp, h))
		if resp.ContentLength > 0 {
			writer = &downloadProgress{target: writer, manager: m, total: resp.ContentLength}
		}
		n, err = io.Copy(writer, io.LimitReader(resp.Body, maxDownloadSize+1))
		if err == nil && n > maxDownloadSize {
			err = errors.New("update: download exceeds size limit")
		}
		sum = hex.EncodeToString(h.Sum(nil))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil && asset.Size > 0 && n != asset.Size {
		err = fmt.Errorf("update: size mismatch: got %d want %d", n, asset.Size)
	}
	if err == nil && asset.SHA256 != "" && !strings.EqualFold(asset.SHA256, sum) {
		err = errors.New("update: SHA-256 mismatch")
	}
	if err != nil {
		m.mu.Lock()
		m.failLocked(err)
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, err
	}
	finalPath := filepath.Join(versionDir, filepath.Base(asset.Name))
	if err = os.Rename(tmpPath, finalPath); err != nil {
		m.mu.Lock()
		m.failLocked(err)
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, err
	}
	m.mu.Lock()
	info.SHA256 = sum
	info.AssetSize = n
	info.AssetName = asset.Name
	info.Available = true
	info.Compatible = true
	if m.state.Release != nil {
		for i := range m.state.Release.Assets {
			if m.state.Release.Assets[i].Name == asset.Name {
				m.state.Release.Assets[i].SHA256 = sum
				m.state.Release.Assets[i].Verified = true
				m.state.Release.Assets[i].LocalPath = finalPath
			}
		}
	}
	m.state.Latest = &info
	m.state.StagedPath = finalPath
	m.state.State = "ready"
	m.completeLocked("下载并校验完成")
	out := m.snapshotLocked()
	m.mu.Unlock()
	return out, nil
}

type downloadProgress struct {
	target  io.Writer
	manager *Manager
	total   int64
	read    int64
	last    time.Time
}

func (p *downloadProgress) Write(b []byte) (int, error) {
	n, err := p.target.Write(b)
	p.read += int64(n)
	now := time.Now()
	if p.last.IsZero() || now.Sub(p.last) >= 500*time.Millisecond || err != nil {
		p.manager.mu.Lock()
		if p.manager.state.Operation != nil && p.manager.state.State == "downloading" && p.total > 0 {
			p.manager.state.Operation.Progress = int((p.read * 100) / p.total)
			if p.manager.state.Operation.Progress > 99 {
				p.manager.state.Operation.Progress = 99
			}
			p.manager.state.Operation.UpdatedAt = now.UTC()
			p.manager.persistLocked()
		}
		p.manager.mu.Unlock()
		p.last = now
	}
	return n, err
}

func (m *Manager) Apply(ctx context.Context) (Snapshot, error) {
	m.mu.Lock()
	if m.state.StagedPath == "" || m.state.Latest == nil {
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, ErrNoRelease
	}
	path, info, fn := m.state.StagedPath, *m.state.Latest, m.applier
	if fn != nil {
		if m.state.Operation == nil {
			now := time.Now().UTC()
			m.state.Operation = &Operation{ID: randomID(), Version: info.LatestVersion, StartedAt: now, UpdatedAt: now}
		}
		m.state.State = "validating"
		m.state.Operation.State = "validating"
		m.state.Operation.Version = info.LatestVersion
		m.state.Operation.Message = "正在交给启动器排空并替换"
		m.state.Operation.UpdatedAt = time.Now().UTC()
		m.persistLocked()
	}
	m.mu.Unlock()
	if fn == nil {
		m.mu.Lock()
		m.state.State = "pending_restart"
		m.completeLocked("升级包已就绪，需由 relaylab-launcher 执行替换和重启")
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, ErrApplyUnavailable
	}
	if err := fn(ctx, path, info); err != nil {
		m.mu.Lock()
		m.failLocked(err)
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, err
	}
	// The launcher acknowledges the handoff before stopping this web process.
	// Keep the journal in validating state; the next process confirms the
	// target version and converts it to succeeded or failed during load().
	m.mu.Lock()
	out := m.snapshotLocked()
	m.mu.Unlock()
	return out, nil
}

func (m *Manager) Rollback(ctx context.Context) (Snapshot, error) {
	m.mu.Lock()
	fn := m.rollback
	m.mu.Unlock()
	if fn == nil {
		m.mu.Lock()
		m.state.State = "failed"
		m.completeLocked("回滚需由 relaylab-launcher 执行")
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, ErrRollbackUnavailable
	}
	if err := fn(ctx); err != nil {
		m.mu.Lock()
		m.failLocked(err)
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, err
	}
	m.mu.Lock()
	m.state.State = "rolled_back"
	m.completeLocked("回滚完成")
	out := m.snapshotLocked()
	m.mu.Unlock()
	return out, nil
}

func (m *Manager) fetchLatest(ctx context.Context) (Release, error) {
	if !repoPartPattern.MatchString(m.cfg.GitHubOwner) || !repoPartPattern.MatchString(m.cfg.GitHubRepo) {
		return Release{}, errors.New("update: invalid GitHub repository")
	}
	base := m.apiBase
	if base == "" {
		base = "https://api.github.com"
	}
	endpoint := strings.TrimRight(base, "/") + "/repos/" + url.PathEscape(m.cfg.GitHubOwner) + "/" + url.PathEscape(m.cfg.GitHubRepo) + "/releases/latest"
	resp, err := m.request(ctx, endpoint, "application/vnd.github+json")
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	var raw struct {
		TagName     string `json:"tag_name"`
		Name        string `json:"name"`
		HTMLURL     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
		Body        string `json:"body"`
		Assets      []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, maxReleaseBody)).Decode(&raw); err != nil {
		return Release{}, fmt.Errorf("update: invalid GitHub release: %w", err)
	}
	if !semverPattern.MatchString(raw.TagName) {
		return Release{}, errors.New("update: release tag is not semantic version")
	}
	var published time.Time
	if raw.PublishedAt != "" {
		published, _ = time.Parse(time.RFC3339, raw.PublishedAt)
	}
	rel := Release{TagName: raw.TagName, Name: raw.Name, URL: raw.HTMLURL, PublishedAt: published, Body: raw.Body, Assets: []Asset{}}
	for _, a := range raw.Assets {
		if a.Name == "" || a.URL == "" || a.Size < 0 {
			continue
		}
		osName, arch := assetPlatform(a.Name)
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, Size: a.Size, DownloadURL: a.URL, OS: osName, Arch: arch})
	}
	// Releases produced by the project include manifest.json. It is optional
	// for older releases, but when present its per-asset hashes are copied into
	// the public snapshot and enforced during download.
	for _, manifestAsset := range rel.Assets {
		if strings.EqualFold(manifestAsset.Name, "manifest.json") {
			if manifest, fetchErr := m.fetchManifest(ctx, manifestAsset.DownloadURL); fetchErr == nil {
				for i := range rel.Assets {
					if item, ok := manifest[rel.Assets[i].Name]; ok {
						rel.Assets[i].SHA256 = item.SHA256
						rel.Assets[i].Verified = item.SHA256 != ""
					}
				}
			}
			break
		}
	}
	if !releaseHasChecksums(rel.Assets) {
		for _, sumsAsset := range rel.Assets {
			name := strings.ToLower(sumsAsset.Name)
			if name != "sha256sums" && name != "sha256sums.txt" && !strings.HasSuffix(name, ".sha256") {
				continue
			}
			if sums, sumsErr := m.fetchChecksums(ctx, sumsAsset.DownloadURL); sumsErr == nil {
				for i := range rel.Assets {
					if hash, ok := sums[rel.Assets[i].Name]; ok {
						rel.Assets[i].SHA256 = hash
						rel.Assets[i].Verified = true
					}
				}
			}
			break
		}
	}
	return rel, nil
}

type manifestAsset struct {
	SHA256 string `json:"sha256"`
}

func (m *Manager) fetchManifest(ctx context.Context, rawURL string) (map[string]manifestAsset, error) {
	resp, err := m.request(ctx, rawURL, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw struct {
		Assets []struct {
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseBody)).Decode(&raw); err != nil {
		return nil, err
	}
	out := make(map[string]manifestAsset, len(raw.Assets))
	for _, item := range raw.Assets {
		if item.Name != "" && len(item.SHA256) == 64 {
			out[item.Name] = manifestAsset{SHA256: strings.ToLower(item.SHA256)}
		}
	}
	return out, nil
}

func releaseHasChecksums(assets []Asset) bool {
	for _, asset := range assets {
		if asset.SHA256 != "" {
			return true
		}
	}
	return false
}

func (m *Manager) fetchChecksums(ctx context.Context, rawURL string) (map[string]string, error) {
	resp, err := m.request(ctx, rawURL, "text/plain")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || len(fields[0]) != sha256.Size*2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name != "" {
			out[name] = strings.ToLower(fields[0])
		}
	}
	return out, nil
}

func (m *Manager) request(ctx context.Context, rawURL, accept string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	allowed := err == nil && allowedGitHubHost(u.Hostname())
	if !allowed && m.apiBase != "" {
		if base, baseErr := url.Parse(m.apiBase); baseErr == nil && strings.EqualFold(base.Host, u.Host) {
			allowed = true
		}
	}
	if err != nil || !allowed {
		return nil, errors.New("update: URL is outside GitHub")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "RelayLab-Updater/1")
	if m.cfg.GitHubToken != "" {
		req.Header.Set("Authorization", "Bearer "+m.cfg.GitHubToken)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, fmt.Errorf("update: GitHub returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func selectAsset(assets []Asset, goos, goarch string) (Asset, bool) {
	for _, a := range assets {
		if a.Name == "relaylab-"+goos+"-"+goarch || a.Name == "relaylab-"+goos+"-"+goarch+".exe" {
			return a, true
		}
	}
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if strings.Contains(n, "relaylab") && strings.Contains(n, goos) && strings.Contains(n, goarch) && !strings.Contains(n, "launcher") {
			return a, true
		}
	}
	return Asset{}, false
}
func assetPlatform(name string) (string, string) {
	n := strings.ToLower(name)
	goos, arch := "", ""
	for _, v := range []string{"windows", "linux", "darwin"} {
		if strings.Contains(n, v) {
			goos = v
			break
		}
	}
	for _, v := range []string{"amd64", "arm64", "386", "arm"} {
		if strings.Contains(n, v) {
			arch = v
			break
		}
	}
	return goos, arch
}
func allowedGitHubHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "api.github.com" || host == "github.com" || strings.HasSuffix(host, ".github.com") || strings.HasSuffix(host, ".githubusercontent.com") || strings.HasSuffix(host, ".githubassets.com")
}
func safeVersion(v string) string {
	v = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '+' {
			return r
		}
		return '_'
	}, v)
	return strings.Trim(v, "._")
}
func isNewer(latest, current string) bool {
	if !semverPattern.MatchString(latest) {
		return false
	}
	if !semverPattern.MatchString(current) {
		return true
	}
	parse := func(v string) [3]int64 {
		v = strings.TrimPrefix(v, "v")
		v = strings.FieldsFunc(v, func(r rune) bool { return r == '-' || r == '+' })[0]
		p := strings.Split(v, ".")
		var out [3]int64
		for i := range out {
			out[i], _ = strconv.ParseInt(p[i], 10, 64)
		}
		return out
	}
	a, b := parse(latest), parse(current)
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
func makeInfo(build BuildInfo, rel Release, asset Asset) UpdateInfo {
	var published *time.Time
	if !rel.PublishedAt.IsZero() {
		v := rel.PublishedAt
		published = &v
	}
	return UpdateInfo{CurrentVersion: build.Version, LatestVersion: rel.TagName, ReleaseURL: rel.URL, PublishedAt: published, AssetName: asset.Name, AssetSize: asset.Size, Compatible: true, Notes: rel.Body, Available: isNewer(rel.TagName, build.Version)}
}
func randomID() string { return fmt.Sprintf("%d", time.Now().UnixNano()) }

func (m *Manager) load() error {
	b, err := os.ReadFile(filepath.Join(m.dataDir, stateFileName))
	if err != nil {
		return err
	}
	var st stateJournal
	if err = json.Unmarshal(b, &st); err != nil {
		return err
	}
	if st.History == nil {
		st.History = []HistoryEntry{}
	}
	recovered := isActiveState(st.State)
	if recovered {
		state := "failed"
		message := "上次升级进程中断，已恢复为失败状态"
		if st.State != "checking" && st.State != "downloading" && st.Latest != nil && isSameVersion(m.build.Version, st.Latest.LatestVersion) {
			state = "succeeded"
			message = "升级后服务已重新启动并通过版本确认"
		}
		st.State = state
		st.Operation = nil
		st.History = append([]HistoryEntry{{ID: randomID(), State: state, Version: func() string {
			if st.Latest != nil {
				return st.Latest.LatestVersion
			}
			return ""
		}(), Message: message, CreatedAt: time.Now().UTC()}}, st.History...)
		if len(st.History) > 20 {
			st.History = st.History[:20]
		}
	}
	m.state = st
	if recovered {
		m.persistLocked()
	}
	return nil
}

func isSameVersion(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

func isActiveState(state string) bool {
	switch state {
	case "checking", "downloading", "draining", "stopping", "backing_up", "starting", "validating":
		return true
	default:
		return false
	}
}
func (m *Manager) persistLocked() {
	dir := m.dataDir
	_ = os.MkdirAll(dir, 0700)
	b, err := json.Marshal(m.state)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".update-state-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(name) }()
	_ = tmp.Chmod(0600)
	if _, err = tmp.Write(b); err != nil {
		return
	}
	if err = tmp.Sync(); err != nil {
		return
	}
	_ = tmp.Close()
	_ = os.Rename(name, filepath.Join(dir, stateFileName))
}
func (m *Manager) failLocked(err error) {
	m.state.State = "failed"
	if m.state.Operation == nil {
		m.state.Operation = &Operation{ID: randomID(), StartedAt: time.Now().UTC()}
	}
	m.state.Operation.ErrorCode = classifyError(err)
	m.state.Operation.Message = safeMessage(err)
	m.completeLocked(m.state.Operation.Message)
	m.persistLocked()
}
func (m *Manager) completeLocked(message string) {
	now := time.Now().UTC()
	if m.state.Operation != nil {
		m.state.Operation.UpdatedAt = now
		if m.state.Operation.Progress < 100 && (m.state.State == "ready" || m.state.State == "succeeded") {
			m.state.Operation.Progress = 100
		}
		m.state.Operation.Message = safeMessage(errors.New(message))
		m.state.History = append([]HistoryEntry{{ID: m.state.Operation.ID, State: m.state.State, Version: m.state.Operation.Version, Message: m.state.Operation.Message, CreatedAt: now}}, m.state.History...)
		if len(m.state.History) > 20 {
			m.state.History = m.state.History[:20]
		}
		m.state.Operation = nil
	}
	m.persistLocked()
}
func (m *Manager) snapshotLocked() Snapshot {
	var latest *UpdateInfo
	if m.state.Latest != nil {
		v := *m.state.Latest
		latest = &v
	}
	var release *Release
	if m.state.Release != nil {
		v := *m.state.Release
		v.Assets = append([]Asset(nil), m.state.Release.Assets...)
		release = &v
	}
	var operation *Operation
	if m.state.Operation != nil {
		v := *m.state.Operation
		operation = &v
	}
	var checkedAt *time.Time
	if m.state.CheckedAt != nil {
		v := *m.state.CheckedAt
		checkedAt = &v
	}
	return Snapshot{Enabled: m.cfg.Enabled, Repository: m.cfg.GitHubOwner + "/" + m.cfg.GitHubRepo, Current: m.build, Latest: latest, Release: release, State: m.state.State, Operation: operation, StagedPath: m.state.StagedPath, History: append([]HistoryEntry(nil), m.state.History...), CheckedAt: checkedAt}
}
func classifyError(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "sha-256"):
		return "checksum_mismatch"
	case strings.Contains(s, "http"):
		return "github_http_error"
	case strings.Contains(s, "timeout"):
		return "timeout"
	case strings.Contains(s, "proxy"):
		return "proxy_error"
	default:
		return "update_failed"
	}
}
func safeMessage(err error) string {
	if err == nil {
		return ""
	}
	s := strings.Map(func(r rune) rune {
		if r < ' ' {
			return ' '
		}
		return r
	}, err.Error())
	s = credentialURLPattern.ReplaceAllString(s, "$1[redacted]@")
	s = bearerPattern.ReplaceAllString(s, "$1[redacted]")
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}
