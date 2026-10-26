package launcher

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"relaylab/internal/update"
)

type InstallRequest struct {
	OperationID       string `json:"operation_id"`
	StagedPath        string `json:"staged_path"`
	SHA256            string `json:"sha256"`
	Size              int64  `json:"size"`
	Version           string `json:"version"`
	Commit            string `json:"commit"`
	BackupDir         string `json:"backup_dir,omitempty"` // compatibility only; never trusted
	LauncherProtocol  int    `json:"launcher_protocol"`
	DatabaseSchema    int    `json:"database_schema"`
	MinDatabaseSchema int    `json:"min_database_schema"`
	DeferRestart      bool   `json:"defer_restart"`
}
type TransactionEvent struct {
	State   string    `json:"state"`
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
}
type Transaction struct {
	ID                 string             `json:"id"`
	OperationID        string             `json:"operation_id"`
	Kind               string             `json:"kind"`
	State              string             `json:"state"`
	Version            string             `json:"version"`
	Commit             string             `json:"commit"`
	Progress           int                `json:"progress"`
	ErrorCode          string             `json:"error_code,omitempty"`
	Message            string             `json:"message"`
	StartedAt          time.Time          `json:"started_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
	Request            InstallRequest     `json:"request"`
	OldMetadata        BinaryMetadata     `json:"old_metadata"`
	OldBinary          string             `json:"old_binary,omitempty"`
	OldSHA256          string             `json:"old_sha256,omitempty"`
	Snapshot           string             `json:"snapshot,omitempty"`
	SnapshotSHA256     string             `json:"snapshot_sha256,omitempty"`
	BinaryChanged      bool               `json:"binary_changed"`
	DataMayHaveChanged bool               `json:"data_may_have_changed"`
	History            []TransactionEvent `json:"history"`
}
type binaryBackup struct {
	Metadata  BinaryMetadata `json:"metadata"`
	SHA256    string         `json:"sha256"`
	Size      int64          `json:"size"`
	CreatedAt time.Time      `json:"created_at"`
}

var operationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

func (s *Supervisor) journalPath() string {
	return filepath.Join(s.opts.DataDir, "launcher-update.json")
}
func (s *Supervisor) transactionCopy() *Transaction {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.txn == nil {
		return nil
	}
	t := *s.txn
	t.History = append([]TransactionEvent(nil), s.txn.History...)
	return &t
}
func (s *Supervisor) saveTransaction(t *Transaction) error {
	t.UpdatedAt = time.Now().UTC()
	b, e := json.MarshalIndent(t, "", "  ")
	if e != nil {
		return e
	}
	if e = atomicWrite(s.journalPath(), append(b, '\n'), 0600); e != nil {
		return e
	}
	clone := *t
	clone.History = append([]TransactionEvent(nil), t.History...)
	s.mu.Lock()
	s.txn = &clone
	s.mu.Unlock()
	// The journal is authoritative. Failure refreshing advisory launcher.json
	// after a durable commit must not retroactively turn success into rollback.
	_ = s.WriteStateFile()
	return nil
}
func (s *Supervisor) phase(t *Transaction, state string, progress int, message, code string) error {
	t.State = state
	t.Progress = progress
	t.Message = message
	t.ErrorCode = code
	t.History = append(t.History, TransactionEvent{State: state, Message: message, At: time.Now().UTC()})
	if len(t.History) > 64 {
		t.History = append([]TransactionEvent(nil), t.History[len(t.History)-64:]...)
	}
	return s.saveTransaction(t)
}
func (s *Supervisor) newTransaction(req InstallRequest, kind string) (*Transaction, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed || s.lifetime.Err() != nil {
		return nil, errors.New("launcher: shutting down")
	}
	if !operationPattern.MatchString(req.OperationID) {
		return nil, errors.New("launcher: operation_id must contain 1-80 safe alphanumeric/underscore/hyphen characters")
	}
	if old := s.transactionCopy(); old != nil && old.OperationID == req.OperationID {
		return nil, errors.New("launcher: operation_id already exists; inspect the authoritative journal")
	}
	t := &Transaction{ID: req.OperationID, OperationID: req.OperationID, Kind: kind, Version: req.Version, Commit: req.Commit, Request: req, StartedAt: time.Now().UTC()}
	if e := s.phase(t, "accepted", 0, "Operation accepted", ""); e != nil {
		return nil, e
	}
	return t, nil
}
func (s *Supervisor) validateInstall(ctx context.Context, req InstallRequest) (InstallRequest, BinaryMetadata, error) {
	var m BinaryMetadata
	if !operationPattern.MatchString(req.OperationID) || req.Size <= 0 || req.Version == "" || req.Commit == "" || req.LauncherProtocol != Protocol || req.DatabaseSchema < 1 || req.MinDatabaseSchema < 1 || req.MinDatabaseSchema > req.DatabaseSchema {
		return req, m, errors.New("launcher: required operation/hash/size/version/commit/protocol/schema metadata missing or incompatible")
	}
	digest, e := hex.DecodeString(req.SHA256)
	if e != nil || len(digest) != 32 {
		return req, m, errors.New("launcher: SHA-256 is required")
	}
	stage, e := filepath.Abs(req.StagedPath)
	if e != nil {
		return req, m, e
	}
	root := filepath.Join(s.opts.DataDir, "updates")
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return req, m, fmt.Errorf("launcher: updates root: %w", e)
	}
	// The updates directory itself must not redirect staging outside DataDir.
	if !strings.EqualFold(filepath.Clean(root), filepath.Clean(filepath.Join(s.opts.DataDir, "updates"))) {
		return req, m, errors.New("launcher: updates directory must not be a symlink or junction")
	}
	resolved, e := filepath.EvalSymlinks(stage)
	if e != nil {
		return req, m, e
	}
	rel, e := filepath.Rel(root, resolved)
	if e != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return req, m, errors.New("launcher: staged path escapes updates directory")
	}
	info, e := os.Lstat(stage)
	if e != nil {
		return req, m, e
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return req, m, errors.New("launcher: staged executable must be a regular non-symlink file")
	}
	got, size, e := SHA256File(resolved)
	if e != nil {
		return req, m, e
	}
	if size != req.Size || !strings.EqualFold(got, req.SHA256) {
		return req, m, errors.New("launcher: staged executable size/SHA-256 mismatch")
	}
	m, e = readBinaryMetadata(ctx, resolved)
	if e != nil {
		return req, m, e
	}
	if m.Version != req.Version || m.Commit != req.Commit || m.LauncherProtocol != req.LauncherProtocol || m.DatabaseSchema != req.DatabaseSchema || m.MinDatabaseSchema != req.MinDatabaseSchema {
		return req, m, errors.New("launcher: staged executable metadata does not match release manifest")
	}
	req.StagedPath = resolved
	req.BackupDir = s.opts.BackupDir
	return req, m, nil
}

// Install is synchronous for local tools and tests. HTTP uses the same operation
// after durable acceptance and detaches it from the caller's request context.
func (s *Supervisor) Install(ctx context.Context, req InstallRequest) error {
	s.transactionMu.Lock()
	defer s.transactionMu.Unlock()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	t, e := s.newTransaction(req, "update")
	if e != nil {
		return e
	}
	return s.installLocked(ctx, t, false)
}
func (s *Supervisor) installLocked(ctx context.Context, t *Transaction, fromRestart bool) error {
	req, target, e := s.validateInstall(ctx, t.Request)
	if e != nil {
		_ = s.phase(t, "failed", 0, e.Error(), "validation_failed")
		return e
	}
	t.Request = req
	t.Version = req.Version
	t.Commit = req.Commit
	current, e := readBinaryMetadata(ctx, s.opts.RelayLabPath)
	if e != nil {
		_ = s.phase(t, "failed", 0, e.Error(), "current_metadata_failed")
		return e
	}
	if current.DatabaseSchema < target.MinDatabaseSchema || current.DatabaseSchema > target.DatabaseSchema {
		e = errors.New("launcher: target binary cannot read the current database schema")
		_ = s.phase(t, "failed", 0, e.Error(), "incompatible_schema")
		return e
	}
	if req.DeferRestart && !fromRestart {
		return s.phase(t, "pending_restart", 10, "Validated update will be installed on the next launcher start", "")
	}
	t.OldMetadata = current
	if e = s.phase(t, "draining", 15, "Waiting for both children to acknowledge maintenance and finish active work", ""); e != nil {
		return e
	}
	if e = s.drainLocked(); e != nil {
		return s.failBeforeReplacement(ctx, t, e)
	}
	if e = s.quiesceOrphans(ctx); e != nil {
		return s.failBeforeReplacement(ctx, t, e)
	}
	if e = s.stopLocked(ctx); e != nil {
		return s.failBeforeReplacement(ctx, t, e)
	}
	if e = s.phase(t, "stopped", 30, "Both children have closed their resources", ""); e != nil {
		return e
	}
	opdir := filepath.Join(s.opts.BackupDir, "update", t.ID)
	if e = os.MkdirAll(opdir, 0700); e != nil {
		return s.failBeforeReplacement(ctx, t, e)
	}
	if e = protectDirectory(opdir); e != nil {
		return s.failBeforeReplacement(ctx, t, e)
	}
	t.OldBinary = filepath.Join(opdir, "relaylab"+filepath.Ext(s.opts.RelayLabPath))
	t.Snapshot = filepath.Join(opdir, "snapshot.zip")
	if e = copyFile(s.opts.RelayLabPath, t.OldBinary); e != nil {
		return s.failBeforeReplacement(ctx, t, e)
	}
	if t.OldSHA256, _, e = SHA256File(t.OldBinary); e != nil {
		return s.failBeforeReplacement(ctx, t, e)
	}
	oldSize := int64(0)
	_, oldSize, e = SHA256File(t.OldBinary)
	if e != nil {
		return s.failBeforeReplacement(ctx, t, e)
	}
	b, _ := json.MarshalIndent(binaryBackup{Metadata: current, SHA256: t.OldSHA256, Size: oldSize, CreatedAt: time.Now().UTC()}, "", "  ")
	if e = atomicWrite(filepath.Join(opdir, "binary.json"), append(b, '\n'), 0600); e != nil {
		return s.failBeforeReplacement(ctx, t, e)
	}
	if e = s.runDataCommand(ctx, t.OldBinary, "backup", "--data-dir", s.opts.DataDir, "--output", t.Snapshot); e != nil {
		return s.failBeforeReplacement(ctx, t, e)
	}
	if e = protectFile(t.Snapshot); e != nil {
		return s.failBeforeReplacement(ctx, t, e)
	}
	if t.SnapshotSHA256, _, e = SHA256File(t.Snapshot); e != nil {
		return s.failBeforeReplacement(ctx, t, e)
	}
	if e = s.phase(t, "backed_up", 45, "Consistent database/key snapshot and previous executable saved", ""); e != nil {
		return e
	}
	// Verify immediately before copying. All writes target a private temp file;
	// hash and metadata are checked again on that exact replacement artifact.
	prepared, e := s.prepareExecutable(req)
	if e != nil {
		return s.failBeforeReplacement(ctx, t, e)
	}
	defer os.Remove(prepared)
	t.BinaryChanged = true
	if e = s.phase(t, "replacing", 55, "Replacing executable atomically", ""); e != nil {
		return e
	}
	if e = atomicRename(prepared, s.opts.RelayLabPath); e != nil {
		return s.rollbackFailure(t, e)
	}
	if e = syncDirectory(filepath.Dir(s.opts.RelayLabPath)); e != nil {
		return s.rollbackFailure(t, e)
	}
	t.DataMayHaveChanged = true
	if e = s.phase(t, "starting", 70, "Starting the target binary behind the maintenance fence", ""); e != nil {
		return s.rollbackFailure(t, e)
	}
	if e = s.startLocked(ctx); e != nil {
		return s.rollbackFailure(t, e)
	}
	if e = s.phase(t, "validating", 90, "Both target child identities, versions, schemas and readiness verified", ""); e != nil {
		return s.rollbackFailure(t, e)
	}
	// Commit must be durable before permitting a single new business write.
	if e = s.phase(t, "succeeded", 100, "Update installed and both children verified", ""); e != nil {
		return s.rollbackFailure(t, e)
	}
	if e = s.resumeLocked(); e != nil {
		return fmt.Errorf("launcher: committed update could not remove maintenance fence: %w", e)
	}
	return nil
}
func (s *Supervisor) prepareExecutable(req InstallRequest) (string, error) {
	f, e := os.CreateTemp(filepath.Dir(s.opts.RelayLabPath), ".relaylab-replacement-*")
	if e != nil {
		return "", e
	}
	path := f.Name()
	if e = f.Close(); e != nil {
		os.Remove(path)
		return "", e
	}
	if e = copyFile(req.StagedPath, path); e != nil {
		os.Remove(path)
		return "", e
	}
	hash, size, e := SHA256File(path)
	if e != nil || hash != strings.ToLower(req.SHA256) || size != req.Size {
		os.Remove(path)
		return "", errors.New("launcher: replacement changed after validation")
	}
	return path, nil
}
func (s *Supervisor) failBeforeReplacement(ctx context.Context, t *Transaction, cause error) error {
	// No executable or database state has changed. Never restore data here.
	if e := s.phase(t, "failed", t.Progress, cause.Error(), "pre_replace_failed"); e != nil {
		return errors.Join(cause, e)
	}
	children := s.children()
	if (children[0] == nil) != (children[1] == nil) {
		recovery, cancel := context.WithTimeout(context.Background(), s.opts.DrainTimeout+2*s.opts.GracePeriod)
		err := s.stopLocked(recovery)
		cancel()
		if err != nil {
			return errors.Join(cause, err)
		}
		children = s.children()
	}
	if children[0] == nil && children[1] == nil {
		restart, cancel := context.WithTimeout(context.Background(), s.opts.StartupTimeout)
		e := s.startLocked(restart)
		cancel()
		if e != nil {
			return errors.Join(cause, e)
		}
	}
	// A drain timeout must not cancel still-active work; resume the old version.
	if e := s.resumeLocked(); e != nil {
		return errors.Join(cause, e)
	}
	return cause
}
func (s *Supervisor) rollbackFailure(t *Transaction, cause error) error {
	recovery, cancel := context.WithTimeout(context.Background(), s.opts.DrainTimeout+2*s.opts.GracePeriod+2*s.opts.StartupTimeout+time.Minute)
	defer cancel()
	if e := s.restoreTransaction(recovery, t, cause); e != nil {
		_ = s.phase(t, "recovery_required", t.Progress, errors.Join(cause, e).Error(), "rollback_failed")
		s.setState(StateFailed, e)
		return errors.Join(cause, e)
	}
	return fmt.Errorf("launcher: update failed and previous state was restored: %w", cause)
}
func (s *Supervisor) restoreTransaction(ctx context.Context, t *Transaction, cause error) error {
	if e := s.drainLocked(); e != nil {
		return e
	}
	if e := s.phase(t, "rolling_back", t.Progress, cause.Error(), "new_version_failed"); e != nil {
		return e
	}
	if e := s.quiesceOrphans(ctx); e != nil {
		return e
	}
	if e := s.stopLocked(ctx); e != nil {
		return e
	}
	hash, _, e := SHA256File(t.OldBinary)
	if e != nil {
		return e
	}
	if hash != t.OldSHA256 {
		return errors.New("launcher: previous executable backup checksum mismatch")
	}
	metadata, e := readBinaryMetadata(ctx, t.OldBinary)
	if e != nil {
		return e
	}
	if metadata != t.OldMetadata {
		return errors.New("launcher: previous executable metadata mismatch")
	}
	if t.DataMayHaveChanged {
		snapshotHash, _, e := SHA256File(t.Snapshot)
		if e != nil {
			return e
		}
		if snapshotHash != t.SnapshotSHA256 {
			return errors.New("launcher: database snapshot checksum mismatch")
		}
		temp, e := os.MkdirTemp(filepath.Dir(t.Snapshot), ".launcher-restore-*")
		if e != nil {
			return e
		}
		defer os.RemoveAll(temp)
		if e = protectDirectory(temp); e != nil {
			return e
		}
		// Restore validates the archive/key/database in a fresh directory. It does
		// not replace updates, backups, control state or the journal itself.
		if e = s.runDataCommand(ctx, t.OldBinary, "restore", "--input", t.Snapshot, "--data-dir", temp); e != nil {
			return e
		}
		for _, name := range []string{"relaylab.db-wal", "relaylab.db-shm"} {
			if e = removeIfExists(filepath.Join(s.opts.DataDir, name)); e != nil {
				return e
			}
		}
		for _, name := range []string{"relaylab.db", "master.key"} {
			if e = replaceFile(filepath.Join(temp, name), filepath.Join(s.opts.DataDir, name)); e != nil {
				return e
			}
		}
	}
	if e = replaceFile(t.OldBinary, s.opts.RelayLabPath); e != nil {
		return e
	}
	if e = s.startLocked(ctx); e != nil {
		return e
	}
	if e = s.phase(t, "rolled_back", 100, "Previous executable and consistent database/key state restored", ""); e != nil {
		return e
	}
	return s.resumeLocked()
}
func (s *Supervisor) runDataCommand(ctx context.Context, binary string, args ...string) error {
	commandTemp, e := os.MkdirTemp(filepath.Dir(binary), ".launcher-command-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(commandTemp)
	if e = protectDirectory(commandTemp); e != nil {
		return e
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = s.opts.WorkingDir
	env := make([]string, 0, len(os.Environ()))
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if !strings.HasPrefix(key, "RELAYLAB_LAUNCHER") && key != "RELAYLAB_DATA_DIR" && key != "RELAYLAB_CONFIG" && !strings.EqualFold(key, "TMP") && !strings.EqualFold(key, "TEMP") && key != "TMPDIR" {
			env = append(env, v)
		}
	}
	cmd.Env = append(env, "TMP="+commandTemp, "TEMP="+commandTemp, "TMPDIR="+commandTemp)
	log, e := os.OpenFile(filepath.Join(s.opts.DataDir, "launcher-update.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return e
	}
	defer log.Close()
	if e = protectFile(log.Name()); e != nil {
		return e
	}
	cmd.Stdout = log
	cmd.Stderr = log
	if e = cmd.Run(); e != nil {
		return fmt.Errorf("launcher: %s command failed: %w", args[0], e)
	}
	return nil
}

// Recover runs before Start. Any interrupted mutation restores a conservative
// pre-update snapshot before a child can perform a new write.
func (s *Supervisor) Recover(ctx context.Context) error {
	s.transactionMu.Lock()
	defer s.transactionMu.Unlock()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	t := s.transactionCopy()
	if t == nil {
		if e := s.quiesceOrphans(ctx); e != nil {
			return e
		}
		return s.resumeLocked()
	}
	switch t.State {
	case "succeeded", "rolled_back":
		if e := s.quiesceOrphans(ctx); e != nil {
			return e
		}
		return s.resumeLocked()
	case "failed":
		if t.BinaryChanged {
			return s.restoreTransaction(ctx, t, errors.New("launcher: retrying interrupted rollback"))
		}
		if e := s.quiesceOrphans(ctx); e != nil {
			return e
		}
		return s.resumeLocked()
	case "pending_restart":
		if err := s.installLocked(ctx, t, true); err != nil {
			s.mu.Lock()
			s.lastError = err
			s.mu.Unlock()
			// A rejected deferred update or successful rollback must leave the
			// verified old service available, retaining the failed journal result.
			current := s.transactionCopy()
			if current != nil && current.State == "rolled_back" {
				return nil
			}
			if !t.BinaryChanged {
				children := s.children()
				if children[0] == nil && children[1] == nil {
					if startErr := s.startLocked(ctx); startErr != nil {
						return errors.Join(err, startErr)
					}
				}
				return s.resumeLocked()
			}
			return err
		}
		return nil
	default:
		if e := s.drainLocked(); e != nil {
			return e
		}
		if e := s.quiesceOrphans(ctx); e != nil {
			return e
		}
		if t.BinaryChanged {
			return s.restoreTransaction(ctx, t, errors.New("launcher: interrupted update recovered before startup"))
		}
		if e := s.phase(t, "failed", t.Progress, "Interrupted before executable replacement; current data preserved", "interrupted"); e != nil {
			return e
		}
		return s.resumeLocked()
	}
}

// Manual rollback preserves current data when the older binary's declared
// schema range accepts it. The regular transaction takes a safety snapshot,
// allowing failed rollback startup to restore the newer version and its data.
func (s *Supervisor) RollbackLatest(ctx context.Context, _ string) error {
	s.transactionMu.Lock()
	defer s.transactionMu.Unlock()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	req, e := s.latestRollback(ctx)
	if e != nil {
		return e
	}
	t, e := s.newTransaction(req, "rollback")
	if e != nil {
		return e
	}
	return s.installLocked(ctx, t, false)
}
func (s *Supervisor) latestRollback(ctx context.Context) (InstallRequest, error) {
	var req InstallRequest
	current, e := readBinaryMetadata(ctx, s.opts.RelayLabPath)
	if e != nil {
		return req, e
	}
	entries, e := os.ReadDir(filepath.Join(s.opts.BackupDir, "update"))
	if e != nil {
		return req, e
	}
	var newest time.Time
	var source string
	var chosen binaryBackup
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(s.opts.BackupDir, "update", entry.Name())
		b, e := os.ReadFile(filepath.Join(dir, "binary.json"))
		if e != nil {
			continue
		}
		var candidate binaryBackup
		if json.Unmarshal(b, &candidate) != nil || candidate.Metadata.Version == current.Version && candidate.Metadata.Commit == current.Commit || candidate.CreatedAt.Before(newest) {
			continue
		}
		if !update.IsNewerVersion(current.Version, candidate.Metadata.Version) {
			continue
		}
		if current.DatabaseSchema < candidate.Metadata.MinDatabaseSchema || current.DatabaseSchema > candidate.Metadata.DatabaseSchema {
			continue
		}
		path := filepath.Join(dir, "relaylab"+filepath.Ext(s.opts.RelayLabPath))
		got, size, e := SHA256File(path)
		if e != nil || got != candidate.SHA256 || size != candidate.Size {
			continue
		}
		actual, e := readBinaryMetadata(ctx, path)
		if e != nil || actual != candidate.Metadata {
			continue
		}
		newest = candidate.CreatedAt
		source = path
		chosen = candidate
	}
	if source == "" {
		return req, errors.New("launcher: no verified schema-compatible executable backup available")
	}
	id, e := randomToken()
	if e != nil {
		return req, e
	}
	id = "rollback-" + id[:24]
	stage := filepath.Join(s.opts.DataDir, "updates", id, "relaylab"+filepath.Ext(s.opts.RelayLabPath))
	if e = copyFile(source, stage); e != nil {
		return req, e
	}
	m := chosen.Metadata
	return InstallRequest{OperationID: id, StagedPath: stage, SHA256: chosen.SHA256, Size: chosen.Size, Version: m.Version, Commit: m.Commit, LauncherProtocol: m.LauncherProtocol, DatabaseSchema: m.DatabaseSchema, MinDatabaseSchema: m.MinDatabaseSchema}, nil
}

// Legacy in-process API now derives every required property from the staged
// executable. It does not trust caller-selected backup directories.
func (s *Supervisor) ReplaceBinary(ctx context.Context, path, sha, _ string) error {
	m, e := readBinaryMetadata(ctx, path)
	if e != nil {
		return e
	}
	got, size, e := SHA256File(path)
	if e != nil {
		return e
	}
	if sha == "" || !strings.EqualFold(sha, got) {
		return errors.New("launcher: SHA-256 required and must match")
	}
	id, e := randomToken()
	if e != nil {
		return e
	}
	return s.Install(ctx, InstallRequest{OperationID: "update-" + id[:24], StagedPath: path, SHA256: sha, Size: size, Version: m.Version, Commit: m.Commit, LauncherProtocol: m.LauncherProtocol, DatabaseSchema: m.DatabaseSchema, MinDatabaseSchema: m.MinDatabaseSchema})
}
