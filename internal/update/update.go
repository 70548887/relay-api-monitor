// Package update discovers, verifies and stages releases. The launcher owns
// replacement, backup, rollback and health confirmation.
package update

import (
	"context"
	"crypto/rand"
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
	"strings"
	"sync"
	"time"

	"relaylab/internal/config"
)

const (
	stateFileName         = "update-state.json"
	launcherStateFileName = "launcher-update.json"
	maxReleaseBody        = 4 << 20
	maxDownloadSize       = 2 << 30
	LauncherProtocol      = 2
	DatabaseSchema        = 1
)

var repoPartPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var digestPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var commitPattern = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
var urlPattern = regexp.MustCompile(`https?://[^\s<>"']+`)
var bearerPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s,;]+`)
var (
	ErrDisabled            = errors.New("update: updater is disabled")
	ErrNoRelease           = errors.New("update: no compatible release found")
	ErrBusy                = errors.New("update: another operation is active")
	ErrApplyUnavailable    = errors.New("update: launcher protocol 2 is required; manually upgrade a 1.2.1 launcher before applying")
	ErrRollbackUnavailable = errors.New("update: launcher protocol 2 is required to roll back")
	ErrStateDurability     = errors.New("update: cannot durably write update state")
	ErrInvalidManifest     = errors.New("update: invalid or incompatible required manifest")
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
	LocalPath   string `json:"-"`
}
type Release struct {
	TagName           string    `json:"tag_name"`
	Name              string    `json:"name"`
	URL               string    `json:"url"`
	PublishedAt       time.Time `json:"published_at"`
	Body              string    `json:"body"`
	Assets            []Asset   `json:"assets"`
	Commit            string    `json:"commit,omitempty"`
	BuildTime         string    `json:"build_time,omitempty"`
	LauncherProtocol  int       `json:"launcher_protocol"`
	DatabaseSchema    int       `json:"database_schema"`
	MinDatabaseSchema int       `json:"min_database_schema"`
}
type UpdateInfo struct {
	CurrentVersion    string     `json:"current_version"`
	LatestVersion     string     `json:"latest_version"`
	ReleaseURL        string     `json:"release_url"`
	PublishedAt       *time.Time `json:"published_at,omitempty"`
	AssetName         string     `json:"asset_name"`
	AssetSize         int64      `json:"asset_size"`
	SHA256            string     `json:"sha256,omitempty"`
	Compatible        bool       `json:"compatible"`
	Available         bool       `json:"available"`
	Notes             string     `json:"notes,omitempty"`
	OperationID       string     `json:"operation_id,omitempty"`
	Commit            string     `json:"commit"`
	LauncherProtocol  int        `json:"launcher_protocol"`
	DatabaseSchema    int        `json:"database_schema"`
	MinDatabaseSchema int        `json:"min_database_schema"`
}
type Operation struct {
	ID              string    `json:"id"`
	State           string    `json:"state"`
	Version         string    `json:"version,omitempty"`
	Progress        int       `json:"progress"`
	BytesDownloaded int64     `json:"bytes_downloaded"`
	TotalBytes      int64     `json:"total_bytes"`
	BytesPerSecond  float64   `json:"bytes_per_second"`
	ETASeconds      int64     `json:"eta_seconds"`
	ErrorCode       string    `json:"error_code,omitempty"`
	Message         string    `json:"message,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
type HistoryEntry struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Version   string    `json:"version,omitempty"`
	Message   string    `json:"message,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
type Snapshot struct {
	Enabled    bool        `json:"enabled"`
	Repository string      `json:"repository"`
	Current    BuildInfo   `json:"current"`
	Latest     *UpdateInfo `json:"latest,omitempty"`
	Release    *Release    `json:"release,omitempty"`
	State      string      `json:"state"`
	Operation  *Operation  `json:"operation,omitempty"`
	// Kept for Go callers; local filesystem paths are never serialized.
	StagedPath string         `json:"-"`
	History    []HistoryEntry `json:"history"`
	CheckedAt  *time.Time     `json:"checked_at,omitempty"`
}
type stateJournal struct {
	Latest              *UpdateInfo    `json:"latest,omitempty"`
	Release             *Release       `json:"release,omitempty"`
	State               string         `json:"state"`
	Operation           *Operation     `json:"operation,omitempty"`
	StagedPath          string         `json:"staged_path,omitempty"`
	StagedBinding       string         `json:"staged_binding,omitempty"`
	LauncherOperationID string         `json:"launcher_operation_id,omitempty"`
	History             []HistoryEntry `json:"history"`
	CheckedAt           *time.Time     `json:"checked_at,omitempty"`
}

// LauncherStatus is an observation of the launcher's authoritative journal.
// The manager never writes launcher-update.json or infers success from its build.
type LauncherStatus struct {
	OperationID string    `json:"operation_id"`
	Kind        string    `json:"kind,omitempty"`
	State       string    `json:"state"`
	Version     string    `json:"version,omitempty"`
	Progress    int       `json:"progress"`
	ErrorCode   string    `json:"error_code,omitempty"`
	Message     string    `json:"message,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type Manager struct {
	mu      sync.Mutex
	cfg     config.UpdateConfig
	dataDir string
	build   BuildInfo
	client  *http.Client
	// apiBase is package-private and used only by in-process HTTP tests.
	apiBase        string
	state          stateJournal
	busy           bool
	launcher       *LauncherStatus
	applier        func(context.Context, string, UpdateInfo) error
	rollback       func(context.Context) error
	rollbackWithID func(context.Context, string) error
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
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, errors.New("update: invalid data directory")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(cfg.Proxy) != "" {
		u, e := url.Parse(cfg.Proxy)
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("update: invalid proxy URL")
		}
		transport.Proxy = http.ProxyURL(u)
	}
	m := &Manager{cfg: cfg, dataDir: abs, build: build, client: &http.Client{Transport: transport, Timeout: 2 * time.Minute}, state: stateJournal{State: "idle", History: []HistoryEntry{}}}
	m.client.CheckRedirect = m.checkRedirect
	if err = m.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
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
func (m *Manager) SetRollbackWithID(fn func(context.Context, string) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollbackWithID = fn
}
func (m *Manager) Config() config.UpdateConfig { m.mu.Lock(); defer m.mu.Unlock(); return m.cfg }
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readLauncherLocked()
	return m.snapshotLocked()
}

func (m *Manager) beginLocked(state, version string) error {
	m.readLauncherLocked()
	if !m.cfg.Enabled {
		return ErrDisabled
	}
	if m.busy || m.launcherActiveLocked() || isActiveState(m.effectiveStateLocked()) {
		return ErrBusy
	}
	if l := m.launcher; l != nil && l.OperationID == m.state.LauncherOperationID && !isActiveState(l.State) {
		found := false
		for _, h := range m.state.History {
			if h.ID == l.OperationID && h.State == l.State {
				found = true
				break
			}
		}
		if !found {
			m.state.History = append([]HistoryEntry{{ID: l.OperationID, State: l.State, Version: l.Version, Message: l.Message, CreatedAt: l.UpdatedAt}}, m.state.History...)
			if len(m.state.History) > 20 {
				m.state.History = m.state.History[:20]
			}
		}
	}
	now := time.Now().UTC()
	m.state.Operation = &Operation{ID: randomID(), State: state, Version: version, StartedAt: now, UpdatedAt: now}
	m.state.State = state
	m.state.LauncherOperationID = ""
	m.launcher = nil
	if err := m.persistLocked(); err != nil {
		// Even asynchronous callers that only poll must see this failure. No
		// network request or launcher handoff starts without durable acceptance.
		m.durabilityFailureLocked()
		return err
	}
	m.busy = true
	return nil
}
func (m *Manager) Check(ctx context.Context) (Snapshot, error) {
	m.mu.Lock()
	if err := m.beginLocked("checking", ""); err != nil {
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, err
	}
	m.mu.Unlock()
	rel, err := m.fetchLatest(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	defer func() { m.busy = false }()
	if err != nil {
		return m.finishErrorLocked(err)
	}
	now := time.Now().UTC()
	m.state.CheckedAt = &now
	asset, ok := selectAsset(rel.Assets, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return m.finishErrorLocked(ErrNoRelease)
	}
	info := makeInfo(m.build, rel, asset)
	m.state.Operation.Version = rel.TagName
	oldBinding := ""
	if m.state.Latest != nil {
		oldBinding = targetBinding(*m.state.Latest)
	}
	newBinding := targetBinding(info)
	if oldBinding != newBinding || m.state.StagedBinding != newBinding {
		m.clearStagingLocked()
	}
	m.state.Latest = &info
	m.state.Release = &rel
	state := "idle"
	if info.Available {
		state = "available"
	}
	if m.state.StagedPath != "" && m.state.StagedBinding == newBinding {
		state = "ready"
		m.markVerifiedLocked(asset.Name, m.state.StagedPath)
	}
	if err = m.completeLocked(state, "检查完成"); err != nil {
		return m.snapshotLocked(), err
	}
	return m.snapshotLocked(), nil
}
func (m *Manager) Download(ctx context.Context) (Snapshot, error) {
	m.mu.Lock()
	if err := m.beginLocked("downloading", ""); err != nil {
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, err
	}
	if m.state.Latest == nil || m.state.Release == nil || !m.state.Latest.Available || !validInfo(*m.state.Latest) {
		out, err := m.finishErrorLocked(ErrNoRelease)
		m.busy = false
		m.mu.Unlock()
		return out, err
	}
	info := *m.state.Latest
	asset, ok := selectAsset(m.state.Release.Assets, runtime.GOOS, runtime.GOARCH)
	if !ok || asset.DownloadURL == "" || targetBinding(makeInfo(m.build, *m.state.Release, asset)) != targetBinding(info) {
		out, err := m.finishErrorLocked(ErrNoRelease)
		m.busy = false
		m.mu.Unlock()
		return out, err
	}
	opID := m.state.Operation.ID
	m.state.Operation.Version = info.LatestVersion
	m.state.Operation.TotalBytes = asset.Size
	m.clearStagingLocked()
	if err := m.persistLocked(); err != nil {
		m.busy = false
		m.durabilityFailureLocked()
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, err
	}
	m.mu.Unlock()
	path, err := m.download(ctx, asset, opID)
	m.mu.Lock()
	defer m.mu.Unlock()
	defer func() { m.busy = false }()
	if err != nil {
		return m.finishErrorLocked(err)
	}
	// This binding cannot change while busy. Persist it together with ready.
	m.state.StagedPath = path
	m.state.StagedBinding = targetBinding(info)
	m.markVerifiedLocked(asset.Name, path)
	m.state.Operation.BytesDownloaded = asset.Size
	m.state.Operation.TotalBytes = asset.Size
	m.state.Operation.ETASeconds = 0
	if err = m.completeLocked("ready", "下载并校验完成"); err != nil {
		m.clearStagingLocked()
		m.state.State = "failed"
		return m.snapshotLocked(), err
	}
	return m.snapshotLocked(), nil
}
func (m *Manager) download(ctx context.Context, asset Asset, opID string) (path string, err error) {
	dir := filepath.Join(m.dataDir, "updates", opID)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return "", errors.New("update: cannot create staging directory")
	}
	tmp, e := os.CreateTemp(dir, ".download-*")
	if e != nil {
		return "", errors.New("update: cannot create staged artifact")
	}
	tempPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tempPath)
		}
	}()
	resp, e := m.request(ctx, asset.DownloadURL, "application/octet-stream")
	if e != nil {
		return "", e
	}
	defer resp.Body.Close()
	if resp.ContentLength > 0 && resp.ContentLength != asset.Size {
		return "", errors.New("update: download size mismatch")
	}
	h := sha256.New()
	progress := &downloadProgress{target: io.MultiWriter(tmp, h), manager: m, total: asset.Size, operationID: opID, started: time.Now()}
	n, e := io.Copy(progress, io.LimitReader(resp.Body, asset.Size+1))
	if e != nil {
		return "", m.safeError(e)
	}
	if n != asset.Size {
		return "", errors.New("update: download size mismatch or truncated artifact")
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), asset.SHA256) {
		return "", errors.New("update: SHA-256 mismatch")
	}
	if tmp.Sync() != nil {
		return "", errors.New("update: cannot sync staged artifact")
	}
	if tmp.Close() != nil {
		return "", errors.New("update: cannot close staged artifact")
	}
	final := filepath.Join(dir, asset.Name)
	if os.Rename(tempPath, final) != nil {
		return "", errors.New("update: cannot commit staged artifact")
	}
	return final, nil
}

type downloadProgress struct {
	target        io.Writer
	manager       *Manager
	total, read   int64
	operationID   string
	started, last time.Time
}

func (p *downloadProgress) Write(b []byte) (int, error) {
	n, err := p.target.Write(b)
	p.read += int64(n)
	now := time.Now()
	if p.last.IsZero() || now.Sub(p.last) >= 500*time.Millisecond || err != nil {
		p.manager.mu.Lock()
		op := p.manager.state.Operation
		if op != nil && op.ID == p.operationID && p.manager.state.State == "downloading" {
			op.BytesDownloaded = p.read
			op.TotalBytes = p.total
			if p.total > 0 {
				op.Progress = int(p.read * 100 / p.total)
				if op.Progress > 99 {
					op.Progress = 99
				}
			}
			if elapsed := now.Sub(p.started).Seconds(); elapsed > 0 {
				op.BytesPerSecond = float64(p.read) / elapsed
				if op.BytesPerSecond > 0 && p.total > p.read {
					op.ETASeconds = int64(float64(p.total-p.read) / op.BytesPerSecond)
				}
			}
			op.UpdatedAt = now.UTC()
			if e := p.manager.persistLocked(); e != nil && err == nil {
				err = e
			}
		}
		p.manager.mu.Unlock()
		p.last = now
	}
	return n, err
}
func (m *Manager) Apply(ctx context.Context) (Snapshot, error) {
	m.mu.Lock()
	if !m.cfg.Enabled {
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, ErrDisabled
	}
	m.readLauncherLocked()
	if m.busy || m.launcherActiveLocked() || isActiveState(m.effectiveStateLocked()) {
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, ErrBusy
	}
	if m.state.State != "ready" || m.state.Latest == nil || !validInfo(*m.state.Latest) || m.state.StagedPath == "" || m.state.StagedBinding != targetBinding(*m.state.Latest) {
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, ErrNoRelease
	}
	if m.applier == nil {
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, ErrApplyUnavailable
	}
	path, info, fn := m.state.StagedPath, *m.state.Latest, m.applier
	if err := m.beginLocked("validating", info.LatestVersion); err != nil {
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, err
	}
	info.OperationID = m.state.Operation.ID
	m.state.Latest.OperationID = info.OperationID
	m.state.LauncherOperationID = info.OperationID
	if err := m.persistLocked(); err != nil {
		m.busy = false
		m.durabilityFailureLocked()
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, err
	}
	m.mu.Unlock()
	err := m.verifyStaged(path, info)
	if err == nil {
		err = fn(ctx, path, info)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	defer func() { m.busy = false }()
	if err != nil {
		return m.finishErrorLocked(err)
	}
	// An accepted request is still pending. Only launcher journal health results
	// can report succeeded or rolled_back, including after a process restart.
	m.state.Operation.Message = "启动器已接受升级，等待健康验证"
	if err = m.persistLocked(); err != nil {
		m.durabilityFailureLocked()
		m.readLauncherLocked()
		return m.snapshotLocked(), err
	}
	m.readLauncherLocked()
	return m.snapshotLocked(), nil
}
func (m *Manager) Rollback(ctx context.Context) (Snapshot, error) {
	m.mu.Lock()
	if !m.cfg.Enabled {
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, ErrDisabled
	}
	m.readLauncherLocked()
	if m.busy || m.launcherActiveLocked() || isActiveState(m.effectiveStateLocked()) {
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, ErrBusy
	}
	if m.rollback == nil && m.rollbackWithID == nil {
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, ErrRollbackUnavailable
	}
	if err := m.beginLocked("validating", ""); err != nil {
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, err
	}
	id, fn, fnID := m.state.Operation.ID, m.rollback, m.rollbackWithID
	m.state.LauncherOperationID = id
	if err := m.persistLocked(); err != nil {
		m.busy = false
		m.durabilityFailureLocked()
		out := m.snapshotLocked()
		m.mu.Unlock()
		return out, err
	}
	m.mu.Unlock()
	var err error
	if fnID != nil {
		err = fnID(ctx, id)
	} else {
		err = fn(ctx)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	defer func() { m.busy = false }()
	if err != nil {
		return m.finishErrorLocked(err)
	}
	m.state.Operation.Message = "启动器已接受回滚，等待健康验证"
	if err = m.persistLocked(); err != nil {
		m.durabilityFailureLocked()
		m.readLauncherLocked()
		return m.snapshotLocked(), err
	}
	m.readLauncherLocked()
	return m.snapshotLocked(), nil
}
func (m *Manager) verifyStaged(path string, info UpdateInfo) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return errors.New("update: invalid staged artifact")
	}
	stagingRelative, stagingErr := filepath.Rel(filepath.Join(m.dataDir, "updates"), abs)
	if stagingErr != nil || stagingRelative == ".." || strings.HasPrefix(stagingRelative, ".."+string(os.PathSeparator)) || filepath.IsAbs(stagingRelative) || filepath.Base(abs) != info.AssetName {
		return errors.New("update: staged artifact is outside staging directory")
	}
	real, err := filepath.EvalSymlinks(abs)
	realRoot, rootErr := filepath.EvalSymlinks(m.dataDir)
	originalRelative, relativeErr := filepath.Rel(m.dataDir, abs)
	realRelative, realRelativeErr := filepath.Rel(realRoot, real)
	sameRelative := originalRelative == realRelative
	if runtime.GOOS == "windows" {
		sameRelative = strings.EqualFold(originalRelative, realRelative)
	}
	if err != nil || rootErr != nil || relativeErr != nil || realRelativeErr != nil || !sameRelative {
		return errors.New("update: staged artifact path changed")
	}
	st, err := os.Lstat(abs)
	if err != nil || !st.Mode().IsRegular() || st.Size() != info.AssetSize {
		return errors.New("update: staged artifact size mismatch")
	}
	f, err := os.Open(abs)
	if err != nil {
		return errors.New("update: cannot open staged artifact")
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, info.AssetSize+1))
	if err != nil || n != info.AssetSize {
		return errors.New("update: staged artifact size mismatch")
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), info.SHA256) {
		return errors.New("update: staged SHA-256 mismatch")
	}
	return nil
}
func (m *Manager) clearStagingLocked() {
	m.state.StagedPath = ""
	m.state.StagedBinding = ""
	if m.state.Release != nil {
		for i := range m.state.Release.Assets {
			m.state.Release.Assets[i].Verified = false
			m.state.Release.Assets[i].LocalPath = ""
		}
	}
}
func (m *Manager) markVerifiedLocked(name, path string) {
	if m.state.Release != nil {
		for i := range m.state.Release.Assets {
			if m.state.Release.Assets[i].Name == name {
				m.state.Release.Assets[i].Verified = true
				m.state.Release.Assets[i].LocalPath = path
			}
		}
	}
}
func validInfo(info UpdateInfo) bool {
	_, ok := parseVersion(info.LatestVersion)
	return ok && info.Available && info.Compatible && info.AssetName == platformAssetName(runtime.GOOS, runtime.GOARCH) && info.AssetSize > 0 && info.AssetSize <= maxDownloadSize && digestPattern.MatchString(info.SHA256) && commitPattern.MatchString(info.Commit) && info.LauncherProtocol == LauncherProtocol && info.DatabaseSchema >= DatabaseSchema && info.MinDatabaseSchema > 0 && info.MinDatabaseSchema <= DatabaseSchema && info.MinDatabaseSchema <= info.DatabaseSchema
}
func targetBinding(info UpdateInfo) string {
	b, _ := json.Marshal([]any{info.LatestVersion, info.Commit, info.AssetName, info.AssetSize, strings.ToLower(info.SHA256), info.LauncherProtocol, info.DatabaseSchema, info.MinDatabaseSchema})
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}
func makeInfo(build BuildInfo, rel Release, asset Asset) UpdateInfo {
	var published *time.Time
	if !rel.PublishedAt.IsZero() {
		v := rel.PublishedAt
		published = &v
	}
	return UpdateInfo{CurrentVersion: build.Version, LatestVersion: rel.TagName, ReleaseURL: rel.URL, PublishedAt: published, AssetName: asset.Name, AssetSize: asset.Size, SHA256: asset.SHA256, Compatible: true, Available: isNewer(rel.TagName, build.Version), Notes: rel.Body, Commit: rel.Commit, LauncherProtocol: rel.LauncherProtocol, DatabaseSchema: rel.DatabaseSchema, MinDatabaseSchema: rel.MinDatabaseSchema}
}
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
func (m *Manager) safeText(s string) string {
	if m.cfg.GitHubToken != "" {
		s = strings.ReplaceAll(s, m.cfg.GitHubToken, "[redacted]")
	}
	if m.cfg.Proxy != "" {
		if u, e := url.Parse(m.cfg.Proxy); e == nil && u.User != nil {
			if p, ok := u.User.Password(); ok && p != "" {
				s = strings.ReplaceAll(s, p, "[redacted]")
			}
		}
	}
	return safeMessage(errors.New(s))
}
func (m *Manager) safeError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrDisabled, ErrNoRelease, ErrBusy, ErrApplyUnavailable, ErrRollbackUnavailable, ErrStateDurability} {
		if errors.Is(err, known) {
			return known
		}
	}
	return errors.New(m.safeText(err.Error()))
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
	s = urlPattern.ReplaceAllStringFunc(s, func(raw string) string {
		u, e := url.Parse(raw)
		if e != nil {
			return "[redacted URL]"
		}
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	})
	s = bearerPattern.ReplaceAllString(s, "${1}[redacted]")
	if len(s) > 2048 {
		s = s[:2048]
	}
	return s
}
func classifyError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrBusy):
		return "operation_busy"
	case errors.Is(err, ErrStateDurability):
		return "state_write_failed"
	case errors.Is(err, ErrInvalidManifest):
		return "invalid_manifest"
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "sha-256"):
		return "checksum_mismatch"
	case strings.Contains(s, "size mismatch") || strings.Contains(s, "truncat"):
		return "size_mismatch"
	case strings.Contains(s, "http"):
		return "github_http_error"
	case strings.Contains(s, "deadline") || strings.Contains(s, "timeout"):
		return "timeout"
	case strings.Contains(s, "proxy"):
		return "proxy_error"
	default:
		return "update_failed"
	}
}
