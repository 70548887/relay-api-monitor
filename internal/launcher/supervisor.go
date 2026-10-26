// Package launcher manages the Relay Lab web and worker child processes.
//
// The launcher is deliberately independent from the web server.  It owns the
// process lifecycle and exposes a small, loopback-only, token-authenticated
// control API that an update manager can use while replacing binaries.
package launcher

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	StateStopped  = "stopped"
	StateStarting = "starting"
	StateRunning  = "running"
	StateDraining = "draining"
	StateStopping = "stopping"
	StateFailed   = "failed"
)

var (
	ErrAlreadyRunning = errors.New("launcher: already running")
	ErrNotRunning     = errors.New("launcher: not running")
	ErrUnauthorized   = errors.New("launcher: unauthorized")
	ErrNonLoopback    = errors.New("launcher: control endpoint requires loopback")
)

type Options struct {
	RelayLabPath   string
	DataDir        string
	Address        string
	ControlAddress string
	ControlToken   string
	ConfigPath     string
	WorkingDir     string
	GracePeriod    time.Duration
}

func (o Options) withDefaults() (Options, error) {
	if o.RelayLabPath == "" {
		exe, err := os.Executable()
		if err != nil {
			return o, err
		}
		name := "relaylab"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		o.RelayLabPath = filepath.Join(filepath.Dir(exe), name)
	}
	if o.WorkingDir == "" {
		o.WorkingDir = filepath.Dir(o.RelayLabPath)
	}
	if o.DataDir == "" {
		o.DataDir = filepath.Join(o.WorkingDir, "data")
	}
	if o.Address == "" {
		o.Address = "127.0.0.1:42783"
	}
	if o.ControlAddress == "" {
		o.ControlAddress = "127.0.0.1:42784"
	}
	if o.GracePeriod <= 0 {
		o.GracePeriod = 20 * time.Second
	}
	for k, v := range map[string]string{"relaylab": o.RelayLabPath, "data": o.DataDir, "working": o.WorkingDir} {
		if v == "" {
			return o, fmt.Errorf("launcher: %s path is empty", k)
		}
	}
	host, _, err := net.SplitHostPort(o.ControlAddress)
	if err != nil {
		return o, fmt.Errorf("launcher: invalid control address: %w", err)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return o, ErrNonLoopback
		}
	}
	return o, nil
}

type ChildStatus struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Running   bool      `json:"running"`
}

type Status struct {
	State          string      `json:"state"`
	ControlAddress string      `json:"control_address"`
	DataDir        string      `json:"data_dir"`
	RelayLabPath   string      `json:"relaylab_path"`
	Address        string      `json:"address"`
	StartedAt      time.Time   `json:"started_at,omitempty"`
	UpdatedAt      time.Time   `json:"updated_at"`
	LastError      string      `json:"last_error,omitempty"`
	Serve          ChildStatus `json:"serve"`
	Worker         ChildStatus `json:"worker"`
	Maintenance    bool        `json:"maintenance"`
}

type processExit struct {
	name string
	err  error
}

type Supervisor struct {
	opts Options

	mu          sync.Mutex
	state       string
	lastError   error
	startedAt   time.Time
	maintenance bool
	serve       *exec.Cmd
	worker      *exec.Cmd
	exits       chan processExit
	server      *http.Server
	serverErr   chan error
	closed      chan struct{}
	closeOnce   sync.Once
	replaceMu   sync.Mutex
}

func New(opts Options) (*Supervisor, error) {
	var err error
	if opts, err = opts.withDefaults(); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(opts.DataDir, 0700); err != nil {
		return nil, err
	}
	if _, err = os.Stat(opts.RelayLabPath); err != nil {
		return nil, fmt.Errorf("launcher: relaylab binary: %w", err)
	}
	if opts.ControlToken == "" {
		opts.ControlToken, err = randomToken()
		if err != nil {
			return nil, err
		}
	}
	return &Supervisor{opts: opts, state: StateStopped, exits: make(chan processExit, 4), serverErr: make(chan error, 1), closed: make(chan struct{})}, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Supervisor) Options() Options { return s.opts }
func (s *Supervisor) Token() string    { return s.opts.ControlToken }

func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := Status{State: s.state, ControlAddress: s.opts.ControlAddress, DataDir: s.opts.DataDir, RelayLabPath: s.opts.RelayLabPath, Address: s.opts.Address, UpdatedAt: time.Now().UTC(), StartedAt: s.startedAt, Maintenance: s.maintenance}
	if s.lastError != nil {
		status.LastError = s.lastError.Error()
	}
	if s.serve != nil {
		status.Serve = ChildStatus{PID: s.serve.Process.Pid, StartedAt: s.startedAt, Running: s.serve.ProcessState == nil}
	}
	if s.worker != nil {
		status.Worker = ChildStatus{PID: s.worker.Process.Pid, StartedAt: s.startedAt, Running: s.worker.ProcessState == nil}
	}
	return status
}

// Serve starts the authenticated control endpoint and child processes. It
// returns when the context is cancelled or a child exits unexpectedly.
func (s *Supervisor) Serve(ctx context.Context) error {
	if err := s.startControl(); err != nil {
		return err
	}
	defer s.closeControl()
	if err := s.Start(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.Background(), s.opts.GracePeriod)
			err := s.Stop(stopCtx)
			cancel()
			if err != nil {
				return err
			}
			return nil
		case e := <-s.exits:
			s.mu.Lock()
			stillRunning := (e.name == "serve" && s.serve != nil) || (e.name == "worker" && s.worker != nil)
			s.mu.Unlock()
			if stillRunning {
				s.mu.Lock()
				s.state, s.lastError = StateFailed, e.err
				s.mu.Unlock()
				_ = s.Stop(context.Background())
				if e.err != nil {
					return fmt.Errorf("launcher: %s exited: %w", e.name, e.err)
				}
				return fmt.Errorf("launcher: %s exited", e.name)
			}
		case err := <-s.serverErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
		}
	}
}

func (s *Supervisor) startControl() error {
	ln, err := net.Listen("tcp", s.opts.ControlAddress)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: s.controlHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Minute, IdleTimeout: 30 * time.Second}
	s.mu.Lock()
	s.server = server
	s.mu.Unlock()
	go func() { s.serverErr <- server.Serve(ln) }()
	return nil
}

func (s *Supervisor) closeControl() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		server := s.server
		s.mu.Unlock()
		if server != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = server.Shutdown(ctx)
			cancel()
		}
		close(s.closed)
	})
}

func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.serve != nil || s.worker != nil {
		s.mu.Unlock()
		return ErrAlreadyRunning
	}
	s.state, s.lastError = StateStarting, nil
	s.startedAt = time.Now().UTC()
	s.mu.Unlock()
	if err := os.Remove(filepath.Join(s.opts.DataDir, "stop.request")); err != nil && !os.IsNotExist(err) {
		return s.failStart(err)
	}
	if !s.isMaintenance() {
		_ = os.Remove(filepath.Join(s.opts.DataDir, "maintenance.request"))
	}
	serve, err := s.startChild("serve")
	if err != nil {
		return s.failStart(err)
	}
	s.mu.Lock()
	s.serve = serve
	s.mu.Unlock()
	if err := waitHealth(ctx, s.opts.Address, 30*time.Second); err != nil {
		_ = s.stopProcess(serve)
		s.mu.Lock()
		s.serve = nil
		s.mu.Unlock()
		return s.failStart(err)
	}
	worker, err := s.startChild("worker")
	if err != nil {
		_ = s.stopProcess(serve)
		s.mu.Lock()
		s.serve = nil
		s.mu.Unlock()
		return s.failStart(err)
	}
	s.mu.Lock()
	s.worker = worker
	s.state = StateRunning
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) failStart(err error) error {
	s.mu.Lock()
	s.state, s.lastError = StateFailed, err
	s.mu.Unlock()
	return err
}

func (s *Supervisor) startChild(name string) (*exec.Cmd, error) {
	args := []string{name, "--addr", s.opts.Address, "--data-dir", s.opts.DataDir}
	cmd := exec.Command(s.opts.RelayLabPath, args...)
	cmd.Dir = s.opts.WorkingDir
	cmd.Env = append(os.Environ(), "RELAYLAB_LAUNCHER=1")
	cmd.Env = append(cmd.Env, "RELAYLAB_LAUNCHER_CONTROL_ADDR="+s.opts.ControlAddress)
	if s.opts.ConfigPath != "" {
		cmd.Env = append(cmd.Env, "RELAYLAB_CONFIG="+s.opts.ConfigPath)
	}
	if err := os.MkdirAll(s.opts.DataDir, 0700); err != nil {
		return nil, err
	}
	logPath := filepath.Join(s.opts.DataDir, name+".log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = f, f
	if err = cmd.Start(); err != nil {
		_ = f.Close()
		return nil, err
	}
	go func() {
		err := cmd.Wait()
		_ = f.Close()
		s.exits <- processExit{name: name, err: err}
	}()
	return cmd, nil
}

func waitHealth(ctx context.Context, address string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp, err := client.Get("http://" + address + "/healthz")
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(string(body), `"status":"ok"`) {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("launcher: website health check timed out at %s", address)
}

func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	serve, worker := s.serve, s.worker
	if serve == nil && worker == nil {
		s.state = StateStopped
		s.mu.Unlock()
		return nil
	}
	s.state = StateStopping
	s.mu.Unlock()
	_ = os.WriteFile(filepath.Join(s.opts.DataDir, "stop.request"), []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0600)
	for _, p := range []*exec.Cmd{worker, serve} {
		if p == nil {
			continue
		}
		if err := s.waitProcess(ctx, p); err != nil {
			_ = s.stopProcess(p)
		}
	}
	s.mu.Lock()
	s.serve, s.worker = nil, nil
	s.state = StateStopped
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) waitProcess(ctx context.Context, cmd *exec.Cmd) error {
	if cmd.ProcessState != nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case e := <-s.exits:
		if e.err != nil && !errors.Is(e.err, os.ErrProcessDone) {
			return e.err
		}
		return nil
	case <-time.After(s.opts.GracePeriod):
		return context.DeadlineExceeded
	}
}

func (s *Supervisor) stopProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return nil
	}
	return cmd.Process.Kill()
}

func (s *Supervisor) Drain() error {
	s.mu.Lock()
	if s.state == StateStopped {
		s.mu.Unlock()
		return ErrNotRunning
	}
	s.maintenance, s.state = true, StateDraining
	s.mu.Unlock()
	return os.WriteFile(filepath.Join(s.opts.DataDir, "maintenance.request"), []byte("draining\n"), 0600)
}

func (s *Supervisor) Resume() error {
	s.mu.Lock()
	s.maintenance = false
	if s.state == StateDraining {
		s.state = StateRunning
	}
	s.mu.Unlock()
	return os.Remove(filepath.Join(s.opts.DataDir, "maintenance.request"))
}

func (s *Supervisor) isMaintenance() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maintenance
}

func (s *Supervisor) Restart(ctx context.Context) error {
	if err := s.Drain(); err != nil && !errors.Is(err, ErrNotRunning) {
		return err
	}
	if err := s.Stop(ctx); err != nil {
		return err
	}
	return s.Start(ctx)
}

// ReplaceBinary performs the launcher handoff for an already downloaded and
// verified Relay Lab executable. The old executable is copied to backupDir,
// then the replacement is atomically moved into place while both children are
// stopped. If the new process does not become healthy, the backup is restored
// and the old version is started again.
func (s *Supervisor) ReplaceBinary(ctx context.Context, stagedPath, expectedSHA, backupDir string) error {
	if stagedPath == "" {
		return errors.New("launcher: staged binary is required")
	}
	if _, err := os.Stat(stagedPath); err != nil {
		return err
	}
	if expectedSHA != "" {
		got, _, err := SHA256File(stagedPath)
		if err != nil {
			return err
		}
		if !strings.EqualFold(got, expectedSHA) {
			return errors.New("launcher: staged binary SHA-256 mismatch")
		}
	}
	if backupDir == "" {
		backupDir = filepath.Join(s.opts.DataDir, "backups", "update")
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return err
	}
	if err := s.Drain(); err != nil && !errors.Is(err, ErrNotRunning) {
		return err
	}
	if err := s.Stop(ctx); err != nil {
		return err
	}
	oldPath := s.opts.RelayLabPath
	backupPath := filepath.Join(backupDir, "relaylab-"+time.Now().UTC().Format("20060102-150405.000000000")+filepath.Ext(oldPath))
	if err := copyFile(oldPath, backupPath); err != nil {
		return err
	}
	if err := replaceFile(stagedPath, oldPath); err != nil {
		return fmt.Errorf("launcher: replace binary: %w", err)
	}
	if err := s.Start(ctx); err == nil {
		_ = s.Resume()
		return nil
	} else {
		_ = s.Stop(context.Background())
		if restoreErr := replaceFile(backupPath, oldPath); restoreErr != nil {
			return fmt.Errorf("launcher: new binary failed (%v), restore failed: %w", err, restoreErr)
		}
		if startErr := s.Start(ctx); startErr != nil {
			return fmt.Errorf("launcher: new binary failed (%v), old binary restart failed: %w", err, startErr)
		}
		_ = s.Resume()
		return fmt.Errorf("launcher: new binary failed and old version restored: %w", err)
	}
}

// RollbackLatest restores the newest executable backup created by a previous
// replacement. It keeps the current data directory intact; callers must only
// expose this operation when the target binary accepts the current schema.
func (s *Supervisor) RollbackLatest(ctx context.Context, backupDir string) error {
	if backupDir == "" {
		backupDir = filepath.Join(s.opts.DataDir, "backups", "update")
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return err
	}
	var chosen string
	var chosenTime time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "relaylab-") {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil || (chosen != "" && !info.ModTime().After(chosenTime)) {
			continue
		}
		chosen = filepath.Join(backupDir, entry.Name())
		chosenTime = info.ModTime()
	}
	if chosen == "" {
		return errors.New("launcher: no executable backup is available")
	}
	if err := s.Drain(); err != nil && !errors.Is(err, ErrNotRunning) {
		return err
	}
	if err := s.Stop(ctx); err != nil {
		return err
	}
	if err := replaceFile(chosen, s.opts.RelayLabPath); err != nil {
		return fmt.Errorf("launcher: restore backup: %w", err)
	}
	if err := s.Start(ctx); err != nil {
		return err
	}
	_ = s.Resume()
	return nil
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := destination + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, destination); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func replaceFile(source, destination string) error {
	tmp := destination + ".incoming.tmp"
	if err := copyFile(source, tmp); err != nil {
		return err
	}
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, destination); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Supervisor) controlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("/status", s.handleControl(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, s.Status()) }))
	mux.HandleFunc("/control/start", s.handleControl(func(w http.ResponseWriter, r *http.Request) {
		err := s.Start(r.Context())
		writeControlResult(w, err, s.Status())
	}))
	mux.HandleFunc("/control/stop", s.handleControl(func(w http.ResponseWriter, r *http.Request) {
		err := s.Stop(r.Context())
		writeControlResult(w, err, s.Status())
	}))
	mux.HandleFunc("/control/restart", s.handleControl(func(w http.ResponseWriter, r *http.Request) {
		err := s.Restart(r.Context())
		writeControlResult(w, err, s.Status())
	}))
	mux.HandleFunc("/control/drain", s.handleControl(func(w http.ResponseWriter, r *http.Request) { err := s.Drain(); writeControlResult(w, err, s.Status()) }))
	mux.HandleFunc("/control/resume", s.handleControl(func(w http.ResponseWriter, r *http.Request) {
		err := s.Resume()
		writeControlResult(w, err, s.Status())
	}))
	mux.HandleFunc("/control/replace", s.handleControl(s.handleReplace))
	mux.HandleFunc("/control/rollback", s.handleControl(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
			return
		}
		if !s.replaceMu.TryLock() {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "replacement_in_progress", "status": s.Status()})
			return
		}
		go func() {
			defer s.replaceMu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			if err := s.RollbackLatest(ctx, filepath.Join(s.opts.DataDir, "backups", "update")); err != nil {
				s.mu.Lock()
				s.lastError = err
				s.mu.Unlock()
			}
			_ = s.WriteStateFile()
		}()
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "status": s.Status()})
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRequest(r) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "loopback_required"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type replaceRequest struct {
	StagedPath string `json:"staged_path"`
	SHA256     string `json:"sha256"`
	BackupDir  string `json:"backup_dir"`
}

func (s *Supervisor) handleReplace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req replaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.StagedPath) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	staged, err := filepath.Abs(req.StagedPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_path"})
		return
	}
	stagedCheck, _ := filepath.EvalSymlinks(staged)
	if stagedCheck == "" {
		stagedCheck = staged
	}
	updatesRoot, _ := filepath.Abs(filepath.Join(s.opts.DataDir, "updates"))
	updatesCheck, _ := filepath.EvalSymlinks(updatesRoot)
	if updatesCheck == "" {
		updatesCheck = updatesRoot
	}
	rel, relErr := filepath.Rel(updatesCheck, stagedCheck)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "staged_path_outside_updates"})
		return
	}
	if !s.replaceMu.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "replacement_in_progress", "status": s.Status()})
		return
	}
	go func() {
		defer s.replaceMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := s.ReplaceBinary(ctx, staged, req.SHA256, req.BackupDir); err != nil {
			s.mu.Lock()
			s.lastError = err
			s.mu.Unlock()
		}
		_ = s.WriteStateFile()
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "status": s.Status()})
}

func (s *Supervisor) handleControl(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
			return
		}
		if !s.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (s *Supervisor) authorized(r *http.Request) bool {
	provided := r.Header.Get("X-RelayLab-Launcher-Token")
	if provided == "" {
		provided = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return provided != "" && provided == s.opts.ControlToken
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeControlResult(w http.ResponseWriter, err error, status Status) {
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "status": status})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteTokenFile writes the control token for local scripts. The file is
// intentionally outside the web API and is created with owner-only mode.
func (s *Supervisor) WriteTokenFile() (string, error) {
	path := filepath.Join(s.opts.DataDir, "launcher.token")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(s.opts.ControlToken+"\n"), 0600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

func (s *Supervisor) WriteStateFile() error {
	path := filepath.Join(s.opts.DataDir, "launcher.json")
	tmp := path + ".tmp"
	b, err := json.MarshalIndent(s.Status(), "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// SHA256File is shared by update code and launcher clients when validating a
// downloaded replacement before handing it to a supervisor.
func SHA256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
