// Package restorestate stores recovery pointers outside the database being restored.
package restorestate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"relaylab/internal/backup"
	"relaylab/internal/durablejson"
	"relaylab/internal/operationlock"
)

var validID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Preflight struct {
	OperationID string          `json:"operation_id,omitempty"`
	DataDir     string          `json:"data_dir"`
	ID          string          `json:"id"`
	BackupID    string          `json:"backup_id"`
	Dir         string          `json:"dir"`
	DBHash      string          `json:"db_hash"`
	KeyHash     string          `json:"key_hash"`
	Manifest    json.RawMessage `json:"manifest"`
	ExpiresAt   time.Time       `json:"expires_at"`
}
type Operation struct {
	DataDir        string    `json:"data_dir"`
	ID             string    `json:"id"`
	PreflightID    string    `json:"preflight_id"`
	State          string    `json:"state"`
	Message        string    `json:"message"`
	ErrorCode      string    `json:"error_code,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	TokenHash      string    `json:"token_hash"`
	TokenExpiresAt time.Time `json:"token_expires_at"`
	SafetyDir      string    `json:"safety_dir,omitempty"`
	SafetyDBHash   string    `json:"safety_db_hash,omitempty"`
	SafetyKeyHash  string    `json:"safety_key_hash,omitempty"`
	DataChanged    bool      `json:"data_changed"`
}

func Root(dataDir string) string      { return filepath.Join(operationlock.BackupRoot(dataDir), ".restore") }
func Stage(dataDir, id string) string { return filepath.Join(Root(dataDir), "preflights", id) }
func PreflightPath(dataDir, id string) string {
	return filepath.Join(Stage(dataDir, id), "preflight.json")
}
func OperationPath(dataDir, id string) string {
	return filepath.Join(Root(dataDir), "operations", id+".json")
}
func ValidID(id string) bool { return validID.MatchString(id) }
func Save(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := backup.ProtectDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if err := durablejson.Write(path, v); err != nil {
		return err
	}
	return backup.ProtectFile(path)
}

type Current struct {
	ID      string `json:"id"`
	DataDir string `json:"data_dir"`
}

func owner(dataDir string) (string, error) { return operationlock.CanonicalDataDir(dataDir) }
func sameOwner(dataDir, actual string) error {
	want, e := owner(dataDir)
	if e != nil {
		return e
	}
	if actual == "" || actual != want {
		return errors.New("恢复记录不属于当前数据目录或缺少归属信息")
	}
	return nil
}
func SavePreflight(dataDir string, p Preflight) error {
	if !ValidID(p.ID) {
		return errors.New("无效预检标识")
	}
	canonical, e := owner(dataDir)
	if e != nil {
		return e
	}
	if p.DataDir != "" && p.DataDir != canonical {
		return errors.New("预检记录归属不匹配")
	}
	p.DataDir = canonical
	return Save(PreflightPath(dataDir, p.ID), p)
}
func SaveOperation(dataDir string, op Operation) error {
	if !ValidID(op.ID) {
		return errors.New("无效恢复操作标识")
	}
	canonical, e := owner(dataDir)
	if e != nil {
		return e
	}
	if op.DataDir != "" && op.DataDir != canonical {
		return errors.New("恢复操作归属不匹配")
	}
	op.DataDir = canonical
	return Save(OperationPath(dataDir, op.ID), op)
}
func SaveCurrent(dataDir, id string) error {
	if !ValidID(id) {
		return errors.New("无效恢复指针")
	}
	canonical, e := owner(dataDir)
	if e != nil {
		return e
	}
	return Save(filepath.Join(Root(dataDir), "current.json"), Current{ID: id, DataDir: canonical})
}
func ReadPreflight(dataDir, id string, out *Preflight) error {
	if !ValidID(id) {
		return errors.New("无效预检标识")
	}
	if e := Read(PreflightPath(dataDir, id), out); e != nil {
		return e
	}
	if out.ID != id {
		return errors.New("预检标识不匹配")
	}
	return sameOwner(dataDir, out.DataDir)
}
func ReadOperation(dataDir, id string, out *Operation) error {
	if !ValidID(id) {
		return errors.New("无效恢复操作标识")
	}
	if e := Read(OperationPath(dataDir, id), out); e != nil {
		return e
	}
	if out.ID != id {
		return errors.New("恢复操作标识不匹配")
	}
	return sameOwner(dataDir, out.DataDir)
}
func ReadCurrent(dataDir string, out *Current) error {
	if e := Read(filepath.Join(Root(dataDir), "current.json"), out); e != nil {
		return e
	}
	if !ValidID(out.ID) {
		return errors.New("无效恢复指针")
	}
	return sameOwner(dataDir, out.DataDir)
}
func Read(path string, out any) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() || st.Size() > 128<<10 {
		return errors.New("invalid recovery metadata")
	}
	return json.NewDecoder(f).Decode(out)
}
func Contained(root, path string) bool {
	r, e := filepath.EvalSymlinks(root)
	if e != nil {
		return false
	}
	p, e := filepath.EvalSymlinks(path)
	if e != nil {
		return false
	}
	rel, e := filepath.Rel(r, p)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}
func Public(op Operation) map[string]any {
	return map[string]any{"id": op.ID, "operation_id": op.ID, "state": op.State, "message": op.Message, "error_code": op.ErrorCode, "started_at": op.StartedAt, "updated_at": op.UpdatedAt}
}

// Cleanup only removes expired preflights belonging to this module. Active
// operations and all safety snapshots remain protected outside the database.
func Cleanup(dataDir string, now time.Time) {
	busy := map[string]bool{}
	operations, _ := os.ReadDir(filepath.Join(Root(dataDir), "operations"))
	for _, entry := range operations {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if strings.HasSuffix(entry.Name(), ".submission.json") {
			cleanupSubmission(dataDir, entry, now)
			continue
		}
		// Only a formally named operation can guard recovery staging. Other
		// metadata belongs to a separate lifecycle and cannot block all cleanup.
		if !ValidID(id) {
			continue
		}
		var op Operation
		if ReadOperation(dataDir, id, &op) != nil {
			// Unknown ownership or damaged journals may still refer to plaintext
			// staging. Preserve it for explicit recovery rather than guessing.
			return
		}
		switch op.State {
		case "succeeded", "failed", "rolled_back":
		default:
			busy[op.PreflightID] = true
		}
	}
	entries, _ := os.ReadDir(filepath.Join(Root(dataDir), "preflights"))
	for _, entry := range entries {
		if !entry.IsDir() || !ValidID(entry.Name()) || busy[entry.Name()] {
			continue
		}
		p := Stage(dataDir, entry.Name())
		if !Contained(filepath.Join(Root(dataDir), "preflights"), p) {
			continue
		}
		var preflight Preflight
		preflightErr := ReadPreflight(dataDir, entry.Name(), &preflight)
		if preflightErr == nil {
			if !now.After(preflight.ExpiresAt) {
				continue
			}
		} else {
			if !os.IsNotExist(preflightErr) {
				continue
			}
			info, e := entry.Info()
			if e != nil || now.Sub(info.ModTime()) < time.Hour {
				continue
			}
		}
		_ = os.RemoveAll(p)
	}
}

func cleanupSubmission(dataDir string, entry os.DirEntry, now time.Time) {
	id := strings.TrimSuffix(entry.Name(), ".submission.json")
	if !ValidID(id) {
		return
	}
	info, e := entry.Info()
	if e != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	path := filepath.Join(Root(dataDir), "operations", entry.Name())
	var submission struct {
		OperationID string    `json:"operation_id"`
		Unknown     bool      `json:"unknown"`
		At          time.Time `json:"at"`
	}
	if Read(path, &submission) != nil || submission.OperationID != id || submission.At.IsZero() || now.Sub(submission.At) < 2*time.Hour {
		return
	}
	var op Operation
	e = ReadOperation(dataDir, id, &op)
	if e == nil {
		switch op.State {
		case "succeeded", "failed", "rolled_back":
		default:
			return
		}
	} else if !os.IsNotExist(e) {
		return
	}
	_ = os.Remove(path)
}
