package update

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

func (m *Manager) load() error {
	f, err := os.Open(filepath.Join(m.dataDir, stateFileName))
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(f, maxReleaseBody+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(b) > maxReleaseBody {
		return errors.New("update: cannot read update journal")
	}
	var st stateJournal
	if json.Unmarshal(b, &st) != nil {
		return errors.New("update: invalid update journal")
	}
	if st.State == "" {
		return errors.New("update: invalid update journal state")
	}
	if st.History == nil {
		st.History = []HistoryEntry{}
	}
	if len(st.History) > 20 {
		st.History = st.History[:20]
	}
	m.state = st
	m.sanitizeStateLocked()
	if m.state.Latest != nil {
		m.state.Latest.CurrentVersion = m.build.Version
		m.state.Latest.Available = isNewer(m.state.Latest.LatestVersion, m.build.Version)
	}
	m.readLauncherLocked()
	if isActiveState(st.State) {
		if m.launcher == nil || m.launcher.OperationID != st.LauncherOperationID {
			if m.state.Operation == nil {
				now := time.Now().UTC()
				m.state.Operation = &Operation{ID: randomID(), StartedAt: now, UpdatedAt: now}
			}
			m.state.Operation.ErrorCode = "operation_interrupted"
			if err = m.completeLocked("failed", "上次操作中断，启动器尚未确认成功"); err != nil {
				return err
			}
		}
	}
	if m.state.StagedPath != "" {
		if m.state.Latest == nil || !validInfo(*m.state.Latest) || m.state.StagedBinding != targetBinding(*m.state.Latest) || m.verifyStaged(m.state.StagedPath, *m.state.Latest) != nil {
			m.clearStagingLocked()
			if m.state.State == "ready" {
				m.state.State = "failed"
				if m.state.Operation != nil {
					m.state.Operation.State = "failed"
					m.state.Operation.ErrorCode = "staging_invalid"
					m.state.Operation.Message = "暂存文件不再匹配已检查的发布"
				}
			}
			if err = m.persistLocked(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) persistLocked() error {
	if os.MkdirAll(m.dataDir, 0700) != nil {
		return ErrStateDurability
	}
	b, err := json.Marshal(m.state)
	if err != nil {
		return ErrStateDurability
	}
	tmp, err := os.CreateTemp(m.dataDir, ".update-state-*")
	if err != nil {
		return ErrStateDurability
	}
	name := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(name) }()
	if tmp.Chmod(0600) != nil {
		return ErrStateDurability
	}
	if _, err = tmp.Write(b); err != nil {
		return ErrStateDurability
	}
	if tmp.Sync() != nil || tmp.Close() != nil {
		return ErrStateDurability
	}
	if os.Rename(name, filepath.Join(m.dataDir, stateFileName)) != nil {
		return ErrStateDurability
	}
	if runtime.GOOS != "windows" {
		dir, err := os.Open(m.dataDir)
		if err != nil {
			return ErrStateDurability
		}
		defer dir.Close()
		if dir.Sync() != nil {
			return ErrStateDurability
		}
	}
	return nil
}

func (m *Manager) completeLocked(state, message string) error {
	now := time.Now().UTC()
	m.state.State = state
	if m.state.Operation != nil {
		op := m.state.Operation
		op.State = state
		op.UpdatedAt = now
		op.Message = m.safeText(message)
		if state == "ready" || state == "succeeded" || state == "rolled_back" {
			op.Progress = 100
		}
		m.state.History = append([]HistoryEntry{{ID: op.ID, State: state, Version: op.Version, Message: op.Message, CreatedAt: now}}, m.state.History...)
		if len(m.state.History) > 20 {
			m.state.History = m.state.History[:20]
		}
	}
	if err := m.persistLocked(); err != nil {
		m.durabilityFailureLocked()
		return err
	}
	return nil
}

func (m *Manager) durabilityFailureLocked() {
	m.state.State = "failed"
	if op := m.state.Operation; op != nil {
		op.State = "failed"
		op.ErrorCode = "state_write_failed"
		op.Message = ErrStateDurability.Error()
		op.UpdatedAt = time.Now().UTC()
		if len(m.state.History) > 0 && m.state.History[0].ID == op.ID {
			m.state.History[0].State = "failed"
			m.state.History[0].Message = op.Message
		}
	}
}

func (m *Manager) finishErrorLocked(err error) (Snapshot, error) {
	code := classifyError(err)
	err = m.safeError(err)
	if m.state.Operation == nil {
		now := time.Now().UTC()
		m.state.Operation = &Operation{ID: randomID(), StartedAt: now, UpdatedAt: now}
	}
	m.state.Operation.ErrorCode = code
	if persistErr := m.completeLocked("failed", err.Error()); persistErr != nil {
		return m.snapshotLocked(), persistErr
	}
	return m.snapshotLocked(), err
}

func (m *Manager) SyncLauncher(status LauncherStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !validLauncherStatus(status) {
		return errors.New("update: invalid launcher status")
	}
	if status.OperationID != m.state.LauncherOperationID {
		return errors.New("update: launcher operation does not match the active request")
	}
	status.Message = m.safeText(status.Message)
	status.ErrorCode = m.safeText(status.ErrorCode)
	m.launcher = &status
	return nil
}

func (m *Manager) readLauncherLocked() {
	f, err := os.Open(filepath.Join(m.dataDir, launcherStateFileName))
	if err != nil {
		return
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxReleaseBody+1))
	if err != nil || len(b) > maxReleaseBody {
		return
	}
	var status LauncherStatus
	if json.Unmarshal(b, &status) != nil || !validLauncherStatus(status) || status.OperationID != m.state.LauncherOperationID {
		return
	}
	if m.launcher != nil && m.launcher.OperationID == status.OperationID && status.UpdatedAt.Before(m.launcher.UpdatedAt) {
		return
	}
	status.Message = m.safeText(status.Message)
	status.ErrorCode = m.safeText(status.ErrorCode)
	m.launcher = &status
}

func validLauncherStatus(status LauncherStatus) bool {
	if status.OperationID == "" || status.Progress < 0 || status.Progress > 100 {
		return false
	}
	switch status.State {
	case "accepted", "pending_restart", "draining", "stopping", "stopped", "backing_up", "backed_up", "replacing", "starting", "validating", "rolling_back", "recovering", "recovery_required", "succeeded", "failed", "rolled_back":
		return true
	}
	return false
}

func (m *Manager) launcherActiveLocked() bool {
	return m.launcher != nil && m.launcher.OperationID == m.state.LauncherOperationID && isActiveState(m.launcher.State)
}

func isActiveState(state string) bool {
	switch state {
	case "checking", "downloading", "accepted", "pending_restart", "draining", "stopping", "stopped", "backing_up", "backed_up", "replacing", "starting", "validating", "rolling_back", "recovering", "recovery_required":
		return true
	}
	return false
}

func (m *Manager) sanitizeStateLocked() {
	if v := m.state.Latest; v != nil {
		v.ReleaseURL = m.safeText(v.ReleaseURL)
		v.Notes = m.safeText(v.Notes)
	}
	if v := m.state.Release; v != nil {
		v.Name = m.safeText(v.Name)
		v.URL = m.safeText(v.URL)
		v.Body = m.safeText(v.Body)
	}
	if v := m.state.Operation; v != nil {
		v.Message = m.safeText(v.Message)
		v.ErrorCode = m.safeText(v.ErrorCode)
	}
	for i := range m.state.History {
		m.state.History[i].Message = m.safeText(m.state.History[i].Message)
	}
}

func (m *Manager) effectiveStateLocked() string {
	if m.launcher != nil && m.launcher.OperationID == m.state.LauncherOperationID {
		return m.launcher.State
	}
	return m.state.State
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
		v.Assets = append([]Asset(nil), v.Assets...)
		for i := range v.Assets {
			v.Assets[i].LocalPath = ""
			v.Assets[i].DownloadURL = ""
		}
		release = &v
	}
	var op *Operation
	if m.state.Operation != nil {
		v := *m.state.Operation
		op = &v
	}
	var checkedAt *time.Time
	if m.state.CheckedAt != nil {
		v := *m.state.CheckedAt
		checkedAt = &v
	}
	history := append([]HistoryEntry(nil), m.state.History...)
	if history == nil {
		history = []HistoryEntry{}
	}
	state := m.state.State
	// Preserve the launcher's operation ID and its final error for API clients.
	if l := m.launcher; l != nil && l.OperationID == m.state.LauncherOperationID {
		state = l.State
		op = &Operation{ID: l.OperationID, Kind: l.Kind, State: l.State, Version: l.Version, Progress: l.Progress, ErrorCode: l.ErrorCode, Message: l.Message, StartedAt: l.StartedAt, UpdatedAt: l.UpdatedAt}
		if !isActiveState(l.State) {
			entry := HistoryEntry{ID: l.OperationID, State: l.State, Version: l.Version, Message: l.Message, CreatedAt: l.UpdatedAt}
			found := false
			for _, h := range history {
				if h.ID == entry.ID && h.State == entry.State {
					found = true
					break
				}
			}
			if !found {
				history = append([]HistoryEntry{entry}, history...)
				if len(history) > 20 {
					history = history[:20]
				}
			}
		}
	}
	return Snapshot{Enabled: m.cfg.Enabled, InstallEnabled: m.cfg.AllowsInstall(), InstallMode: m.cfg.InstallMode, Repository: m.cfg.GitHubOwner + "/" + m.cfg.GitHubRepo, Current: m.build, Latest: latest, Release: release, State: state, Operation: op, History: history, CheckedAt: checkedAt}
}
