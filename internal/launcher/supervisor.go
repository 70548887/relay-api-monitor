// Package launcher owns process lifecycle and the durable update journal.
// It never opens a live database. Backup and restore are delegated to the
// verified executable after every child has closed.
package launcher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

const Protocol = 2
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
	RelayLabPath, DataDir, Address, ControlAddress, ControlToken, ConfigPath, WorkingDir, BackupDir, LauncherVersion string
	GracePeriod, StartupTimeout, DrainTimeout                                                                        time.Duration
}

func (o Options) withDefaults() (Options, error) {
	if o.RelayLabPath == "" {
		exe, e := os.Executable()
		if e != nil {
			return o, e
		}
		name := "relaylab"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		o.RelayLabPath = filepath.Join(filepath.Dir(exe), name)
	}
	var e error
	if o.RelayLabPath, e = filepath.Abs(o.RelayLabPath); e != nil {
		return o, e
	}
	if o.WorkingDir == "" {
		o.WorkingDir = filepath.Dir(o.RelayLabPath)
	}
	if o.WorkingDir, e = filepath.Abs(o.WorkingDir); e != nil {
		return o, e
	}
	if o.DataDir == "" {
		o.DataDir = filepath.Join(o.WorkingDir, "data")
	}
	if o.DataDir, e = filepath.Abs(o.DataDir); e != nil {
		return o, e
	}
	if o.BackupDir == "" {
		o.BackupDir = filepath.Join(o.DataDir, "backups")
	}
	if !filepath.IsAbs(o.BackupDir) {
		o.BackupDir = filepath.Join(o.WorkingDir, o.BackupDir)
	}
	if o.BackupDir, e = filepath.Abs(o.BackupDir); e != nil {
		return o, e
	}
	if o.Address == "" {
		o.Address = "127.0.0.1:42783"
	}
	if o.ControlAddress == "" {
		o.ControlAddress = "127.0.0.1:0"
	}
	if o.LauncherVersion == "" {
		o.LauncherVersion = "dev"
	}
	if o.GracePeriod <= 0 {
		o.GracePeriod = 30 * time.Second
	}
	if o.StartupTimeout <= 0 {
		// A confirmed dead worker can leave its 45-second database lease.
		// Keep startup validation longer than the worker's bounded lease wait.
		o.StartupTimeout = 90 * time.Second
	}
	if o.DrainTimeout <= 0 || o.DrainTimeout > 660*time.Second {
		o.DrainTimeout = 660 * time.Second
	}
	host, _, e := net.SplitHostPort(o.ControlAddress)
	if e != nil {
		return o, fmt.Errorf("launcher: invalid control address: %w", e)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return o, ErrNonLoopback
	}
	return o, nil
}

type BinaryMetadata struct {
	Version           string `json:"version"`
	Commit            string `json:"commit"`
	BuildTime         string `json:"build_time"`
	OS                string `json:"os"`
	Arch              string `json:"arch"`
	LauncherProtocol  int    `json:"launcher_protocol"`
	UsageGuard        int    `json:"usage_guard"`
	DatabaseSchema    int    `json:"database_schema"`
	MinDatabaseSchema int    `json:"min_database_schema"`
}
type RuntimeReport struct {
	PID              int       `json:"pid"`
	InstanceID       string    `json:"instance_id"`
	Role             string    `json:"role"`
	Version          string    `json:"version"`
	Commit           string    `json:"commit"`
	LauncherProtocol int       `json:"launcher_protocol"`
	DatabaseSchema   int       `json:"database_schema"`
	Maintenance      bool      `json:"maintenance"`
	Active           int       `json:"active"`
	Ready            bool      `json:"ready"`
	HeartbeatAt      time.Time `json:"heartbeat_at"`
}
type ChildStatus struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Running   bool      `json:"running"`
}
type Status struct {
	Protocol       int          `json:"protocol"`
	PID            int          `json:"pid"`
	Version        string       `json:"version"`
	InstanceID     string       `json:"instance_id"`
	State          string       `json:"state"`
	ControlAddress string       `json:"control_address"`
	DataDir        string       `json:"data_dir"`
	RelayLabPath   string       `json:"relaylab_path"`
	Address        string       `json:"address"`
	StartedAt      time.Time    `json:"started_at,omitempty"`
	UpdatedAt      time.Time    `json:"updated_at"`
	LastError      string       `json:"last_error,omitempty"`
	Serve          ChildStatus  `json:"serve"`
	Worker         ChildStatus  `json:"worker"`
	Maintenance    bool         `json:"maintenance"`
	Transaction    *Transaction `json:"transaction,omitempty"`
	Operation      *Transaction `json:"operation,omitempty"`
}
type child struct {
	role     string
	cmd      *exec.Cmd
	done     chan struct{}
	err      error // written before closing done; read only after receiving done
	started  time.Time
	instance string
	metadata BinaryMetadata
}
type Supervisor struct {
	opts          Options
	mu            sync.Mutex
	opMu          sync.Mutex // serializes every lifecycle operation and recovery
	transactionMu sync.Mutex // reserves an async operation before returning 202
	state         string
	lastError     error
	startedAt     time.Time
	maintenance   bool
	instance      string
	serve, worker *child
	server        *http.Server
	serverErr     chan error
	events        chan *child
	lifetime      context.Context
	cancel        context.CancelFunc
	lock          *os.File
	txn           *Transaction
	closed        bool
}

func New(opts Options) (*Supervisor, error) {
	var e error
	if opts, e = opts.withDefaults(); e != nil {
		return nil, e
	}
	if e = os.MkdirAll(opts.DataDir, 0700); e != nil {
		return nil, e
	}
	if opts.DataDir, e = filepath.EvalSymlinks(opts.DataDir); e != nil {
		return nil, e
	}
	if _, e = os.Stat(opts.RelayLabPath); e != nil {
		return nil, fmt.Errorf("launcher: executable: %w", e)
	}
	lock, e := acquireLock(filepath.Join(opts.DataDir, "launcher.lock"))
	if e != nil {
		return nil, e
	}
	fail := func(err error) (*Supervisor, error) { _ = releaseLock(lock); return nil, err }
	if opts.ControlToken == "" {
		opts.ControlToken, e = randomToken()
		if e != nil {
			return fail(e)
		}
	}
	instance, e := randomToken()
	if e != nil {
		return fail(e)
	}
	lifetime, cancel := context.WithCancel(context.Background())
	s := &Supervisor{opts: opts, state: StateStopped, instance: instance, lock: lock, lifetime: lifetime, cancel: cancel, events: make(chan *child, 16), serverErr: make(chan error, 1)}
	if b, err := os.ReadFile(s.journalPath()); err == nil {
		var txn Transaction
		if err = json.Unmarshal(b, &txn); err != nil {
			cancel()
			return fail(fmt.Errorf("launcher: invalid update journal: %w", err))
		}
		s.txn = &txn
	} else if !os.IsNotExist(err) {
		cancel()
		return fail(err)
	}
	_, e = os.Stat(filepath.Join(opts.DataDir, "maintenance.request"))
	s.maintenance = e == nil
	return s, nil
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func (s *Supervisor) Options() Options { s.mu.Lock(); defer s.mu.Unlock(); return s.opts }
func (s *Supervisor) Token() string    { return s.opts.ControlToken }
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := Status{Protocol: Protocol, PID: os.Getpid(), Version: s.opts.LauncherVersion, InstanceID: s.instance, State: s.state, ControlAddress: s.opts.ControlAddress, DataDir: s.opts.DataDir, RelayLabPath: s.opts.RelayLabPath, Address: s.opts.Address, StartedAt: s.startedAt, UpdatedAt: time.Now().UTC(), Maintenance: s.maintenance}
	if s.lastError != nil {
		status.LastError = s.lastError.Error()
	}
	for _, c := range []*child{s.serve, s.worker} {
		if c == nil {
			continue
		}
		running := true
		select {
		case <-c.done:
			running = false
		default:
		}
		cs := ChildStatus{PID: c.cmd.Process.Pid, StartedAt: c.started, Running: running}
		if c.role == "serve" {
			status.Serve = cs
		} else {
			status.Worker = cs
		}
	}
	if s.txn != nil {
		clone := *s.txn
		clone.History = append([]TransactionEvent(nil), s.txn.History...)
		status.Transaction = &clone
		status.Operation = &clone
	}
	return status
}
func (s *Supervisor) setState(state string, err error) {
	s.mu.Lock()
	s.state = state
	s.lastError = err
	s.mu.Unlock()
	_ = s.WriteStateFile()
}
func (s *Supervisor) Serve(ctx context.Context) (result error) {
	if e := s.startControl(); e != nil {
		_ = s.Close()
		return e
	}
	defer func() { result = errors.Join(result, s.Close()) }()
	if e := s.Recover(ctx); e != nil {
		return e
	}
	if s.Status().Serve.PID == 0 {
		if e := s.Start(ctx); e != nil {
			return e
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.lifetime.Done():
			return nil
		case e := <-s.serverErr:
			if e != nil && !errors.Is(e, http.ErrServerClosed) {
				return e
			}
		case c := <-s.events:
			s.mu.Lock()
			current := s.serve == c || s.worker == c
			state := s.state
			s.mu.Unlock()
			if current && (state == StateRunning || state == StateDraining) {
				e := fmt.Errorf("launcher: %s PID %d exited unexpectedly: %v", c.role, c.cmd.Process.Pid, c.err)
				s.setState(StateFailed, e)
				return e
			}
		}
	}
}
func (s *Supervisor) Close() error {
	s.cancel()
	s.transactionMu.Lock()
	defer s.transactionMu.Unlock()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	server := s.server
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.DrainTimeout+2*s.opts.GracePeriod)
	defer cancel()
	e := s.stopLocked(ctx)
	if e != nil {
		s.setState(StateFailed, e)
		return e
	} // retain both the control endpoint and ownership if a child might still write
	if server != nil {
		shutdown, done := context.WithTimeout(context.Background(), 2*time.Second)
		_ = server.Shutdown(shutdown)
		done()
	}
	s.mu.Lock()
	s.closed = true
	lock := s.lock
	s.lock = nil
	s.mu.Unlock()
	_ = s.WriteStateFile()
	if lock != nil {
		return releaseLock(lock)
	}
	return nil
}
func (s *Supervisor) Start(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.startLocked(ctx)
}
func (s *Supervisor) startLocked(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("launcher: closed")
	}
	if s.serve != nil || s.worker != nil {
		s.mu.Unlock()
		return ErrAlreadyRunning
	}
	s.mu.Unlock()
	if e := s.quiesceOrphans(ctx); e != nil {
		return e
	}
	metadata, e := readBinaryMetadata(ctx, s.opts.RelayLabPath)
	if e != nil {
		return s.failStart(e)
	}
	for _, name := range []string{"stop.request", "serve.stop.request", "worker.stop.request"} {
		if e := removeIfExists(filepath.Join(s.opts.DataDir, name)); e != nil {
			return s.failStart(e)
		}
	}
	s.mu.Lock()
	s.startedAt = time.Now().UTC()
	s.mu.Unlock()
	s.setState(StateStarting, nil)
	ready, cancel := context.WithTimeout(ctx, s.opts.StartupTimeout)
	defer cancel()
	for _, role := range []string{"serve", "worker"} {
		c, e := s.startChild(role, metadata)
		if c != nil {
			s.mu.Lock()
			if role == "serve" {
				s.serve = c
			} else {
				s.worker = c
			}
			s.mu.Unlock()
		}
		if e != nil {
			_ = s.stopStartupChildren()
			return s.failStart(e)
		}
		if role == "serve" {
			// The HTTP child owns first-run database/key initialization. Its
			// exact ready report must exist before a worker may open that data.
			if e = s.waitReports(ready, false); e != nil {
				if stopErr := s.stopStartupChildren(); stopErr != nil {
					e = errors.Join(e, stopErr)
				}
				return s.failStart(e)
			}
		}
	}
	e = s.waitReports(ready, false)
	if e != nil {
		if stopErr := s.stopStartupChildren(); stopErr != nil {
			e = errors.Join(e, stopErr)
		}
		return s.failStart(e)
	}
	s.mu.Lock()
	maintenance := s.maintenance
	s.mu.Unlock()
	if maintenance {
		s.setState(StateDraining, nil)
	} else {
		s.setState(StateRunning, nil)
	}
	return nil
}
func (s *Supervisor) failStart(e error) error { s.setState(StateFailed, e); return e }
func (s *Supervisor) startChild(role string, m BinaryMetadata) (*child, error) {
	args := []string{role, "--addr", s.opts.Address, "--data-dir", s.opts.DataDir}
	if s.opts.ConfigPath != "" {
		args = append(args, "--config", s.opts.ConfigPath)
	}
	cmd := exec.Command(s.opts.RelayLabPath, args...)
	cmd.Dir = s.opts.WorkingDir
	env := make([]string, 0, len(os.Environ())+6)
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if key != "RELAYLAB_LAUNCHER" && key != "RELAYLAB_LAUNCHER_INSTANCE" && key != "RELAYLAB_LAUNCHER_CONTROL_ADDR" && key != "RELAYLAB_LAUNCHER_TOKEN" && key != "RELAYLAB_CONFIG" && key != "RELAYLAB_CHILD_STARTED_AT" {
			env = append(env, v)
		}
	}
	cmd.Env = append(env, "RELAYLAB_LAUNCHER=1", "RELAYLAB_LAUNCHER_INSTANCE="+s.instance, "RELAYLAB_LAUNCHER_CONTROL_ADDR="+s.opts.ControlAddress, "RELAYLAB_LAUNCHER_TOKEN="+s.opts.ControlToken)
	if s.opts.ConfigPath != "" {
		cmd.Env = append(cmd.Env, "RELAYLAB_CONFIG="+s.opts.ConfigPath)
	}
	f, e := os.OpenFile(filepath.Join(s.opts.DataDir, role+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return nil, e
	}
	cmd.Stdout = f
	cmd.Stderr = f
	intent := childIntent{Role: role, InstanceID: s.instance, Executable: s.opts.RelayLabPath, StartedAt: time.Now().UTC()}
	cmd.Env = append(cmd.Env, "RELAYLAB_CHILD_STARTED_AT="+intent.StartedAt.Format(time.RFC3339Nano))
	if e = s.saveChildIntent(intent); e != nil {
		_ = f.Close()
		return nil, e
	}
	if e = cmd.Start(); e != nil {
		_ = f.Close()
		_ = removeIfExists(s.childIntentPath(role))
		return nil, e
	}
	c := &child{role: role, cmd: cmd, done: make(chan struct{}), started: time.Now().UTC(), instance: s.instance, metadata: m}
	go func() {
		c.err = cmd.Wait()
		_ = f.Close()
		_ = s.removeChildIntent(role, c.instance, c.cmd.Process.Pid)
		close(c.done)
		select {
		case s.events <- c:
		default:
		}
	}()
	intent.PID = cmd.Process.Pid
	identity, identityErr := processIdentity(intent.PID)
	if identityErr == nil {
		intent.ProcessIdentity = identity
	}
	if e = s.saveChildIntent(intent); e != nil {
		return c, e
	}
	if identityErr != nil {
		select {
		case <-c.done:
			return c, nil
		default:
		}
		return c, fmt.Errorf("launcher: child identity: %w", identityErr)
	}
	return c, nil
}
func (s *Supervisor) children() []*child {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []*child{s.worker, s.serve}
}
func (s *Supervisor) readReport(c *child) (RuntimeReport, error) {
	var r RuntimeReport
	b, e := os.ReadFile(filepath.Join(s.opts.DataDir, "runtime-"+c.role+".json"))
	if e != nil {
		return r, e
	}
	if len(b) > 16384 {
		return r, errors.New("launcher: oversized runtime report")
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return r, e
	}
	if r.PID != c.cmd.Process.Pid || r.InstanceID != c.instance || r.Role != c.role || r.Version != c.metadata.Version || r.Commit != c.metadata.Commit || r.LauncherProtocol != Protocol || r.DatabaseSchema != c.metadata.DatabaseSchema {
		return r, fmt.Errorf("launcher: %s runtime identity/version/schema mismatch", c.role)
	}
	if age := time.Since(r.HeartbeatAt); age > 3*time.Second || age < -time.Second || r.HeartbeatAt.Before(c.started.Add(-time.Second)) {
		return r, fmt.Errorf("launcher: %s heartbeat stale", c.role)
	}
	return r, nil
}
func (s *Supervisor) waitReports(ctx context.Context, drain bool) error {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var last error
	for {
		ready := true
		for _, c := range s.children() {
			if c == nil {
				continue
			}
			select {
			case <-c.done:
				if drain {
					continue
				}
				return fmt.Errorf("launcher: %s exited before ready: %v", c.role, c.err)
			default:
			}
			r, e := s.readReport(c)
			if e != nil {
				ready = false
				last = e
				continue
			}
			if !r.Ready || (drain && (!r.Maintenance || r.Active != 0)) {
				ready = false
				last = fmt.Errorf("launcher: %s ready=%t maintenance=%t active=%d", c.role, r.Ready, r.Maintenance, r.Active)
			}
			if !drain {
				s.mu.Lock()
				maintenance := s.maintenance
				s.mu.Unlock()
				if maintenance && !r.Maintenance {
					ready = false
					last = fmt.Errorf("launcher: %s did not enter maintenance", c.role)
				}
			}
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("launcher: reports timed out: %w (%v)", ctx.Err(), last)
		case <-tick.C:
		}
	}
}
func (s *Supervisor) Drain() error { s.opMu.Lock(); defer s.opMu.Unlock(); return s.drainLocked() }
func (s *Supervisor) drainLocked() error {
	if e := atomicWrite(filepath.Join(s.opts.DataDir, "maintenance.request"), []byte("maintenance\n"), 0600); e != nil {
		return e
	}
	s.mu.Lock()
	s.maintenance = true
	if s.serve != nil || s.worker != nil {
		s.state = StateDraining
	}
	s.mu.Unlock()
	return s.WriteStateFile()
}
func (s *Supervisor) Resume() error { s.opMu.Lock(); defer s.opMu.Unlock(); return s.resumeLocked() }
func (s *Supervisor) resumeLocked() error {
	if err := s.unmanagedIntentError(); err != nil {
		return s.recoveryBlocked(err)
	}
	if e := removeIfExists(filepath.Join(s.opts.DataDir, "maintenance.request")); e != nil {
		return e
	}
	s.mu.Lock()
	s.maintenance = false
	if s.serve != nil && s.worker != nil {
		s.state = StateRunning
	}
	s.mu.Unlock()
	return s.WriteStateFile()
}
func (s *Supervisor) Stop(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.stopLocked(ctx)
}
func (s *Supervisor) stopLocked(ctx context.Context) error {
	children := s.children()
	if children[0] == nil && children[1] == nil {
		if err := s.unmanagedIntentError(); err != nil {
			return s.recoveryBlocked(err)
		}
		s.setState(StateStopped, nil)
		return nil
	}
	if e := s.drainLocked(); e != nil {
		return e
	}
	drain, cancel := context.WithTimeout(ctx, s.opts.DrainTimeout)
	e := s.waitReports(drain, true)
	cancel()
	if e != nil {
		return e
	}
	s.setState(StateStopping, nil)
	// Per-child completion avoids consuming another process's exit or an old generation's event.
	for _, c := range children {
		if c == nil {
			continue
		}
		if e := atomicWrite(filepath.Join(s.opts.DataDir, c.role+".stop.request"), []byte("stop\n"), 0600); e != nil {
			return e
		}
		if e := s.waitChild(ctx, c); e != nil {
			return e
		}
		s.mu.Lock()
		if s.worker == c {
			s.worker = nil
		}
		if s.serve == c {
			s.serve = nil
		}
		s.mu.Unlock()
	}
	s.setState(StateStopped, nil)
	return nil
}
func (s *Supervisor) waitChild(ctx context.Context, c *child) error {
	deadline, cancel := context.WithTimeout(ctx, s.opts.GracePeriod)
	defer cancel()
	select {
	case <-c.done:
		return nil
	case <-deadline.Done():
		return fmt.Errorf("launcher: %s PID %d has not closed; no replacement performed: %w", c.role, c.cmd.Process.Pid, deadline.Err())
	}
}
func (s *Supervisor) stopStartupChildren() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*s.opts.GracePeriod)
	defer cancel()
	if e := atomicWrite(filepath.Join(s.opts.DataDir, "stop.request"), []byte("startup failed\n"), 0600); e != nil {
		return e
	}
	var result error
	for _, c := range s.children() {
		if c == nil {
			continue
		}
		if e := s.waitChild(ctx, c); e != nil {
			result = errors.Join(result, e)
			continue
		}
		s.mu.Lock()
		if s.worker == c {
			s.worker = nil
		}
		if s.serve == c {
			s.serve = nil
		}
		s.mu.Unlock()
	}
	return result
}
func (s *Supervisor) Restart(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if e := s.stopLocked(ctx); e != nil {
		return e
	}
	if e := s.startLocked(ctx); e != nil {
		return e
	}
	return s.resumeLocked()
}

// Recovery fences and closes children surviving a previous launcher. It never
// kills by a stale PID: an ambiguous process leaves the fence in place.
func (s *Supervisor) quiesceOrphans(ctx context.Context) error {
	if err := s.quiesceRegistered(ctx); err != nil {
		return err
	}
	var live []RuntimeReport
	for _, role := range []string{"worker", "serve"} {
		managedRole := false
		for _, c := range s.children() {
			if c != nil && c.role == role {
				managedRole = true
			}
		}
		if managedRole {
			continue
		} // managed identities are validated/retried by waitReports
		var r RuntimeReport
		b, e := os.ReadFile(filepath.Join(s.opts.DataDir, "runtime-"+role+".json"))
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if e = json.Unmarshal(b, &r); e != nil {
			return fmt.Errorf("launcher: invalid orphan report: %w", e)
		}
		managed := false
		for _, c := range s.children() {
			if c != nil && c.role == role && c.cmd.Process.Pid == r.PID && c.instance == r.InstanceID {
				managed = true
			}
		}
		if managed {
			continue
		}
		if r.PID > 0 && processAlive(r.PID) {
			if r.Role != role || r.LauncherProtocol != Protocol || r.InstanceID == "" {
				return errors.New("launcher: existing process has no trustworthy protocol-2 identity")
			}
			live = append(live, r)
		}
	}
	if len(live) == 0 {
		return nil
	}
	if e := s.drainLocked(); e != nil {
		return e
	}
	drain, cancel := context.WithTimeout(ctx, s.opts.DrainTimeout)
	defer cancel()
	for _, old := range live {
		for processAlive(old.PID) {
			var r RuntimeReport
			b, e := os.ReadFile(filepath.Join(s.opts.DataDir, "runtime-"+old.Role+".json"))
			if e == nil && json.Unmarshal(b, &r) == nil && r.PID == old.PID && r.InstanceID == old.InstanceID && r.Maintenance && r.Active == 0 && time.Since(r.HeartbeatAt) < 3*time.Second {
				break
			}
			select {
			case <-drain.Done():
				return fmt.Errorf("launcher: orphan %s did not drain: %w", old.Role, drain.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	for _, old := range live {
		if e := atomicWrite(filepath.Join(s.opts.DataDir, old.Role+".stop.request"), []byte("recovery\n"), 0600); e != nil {
			return e
		}
		closeCtx, done := context.WithTimeout(ctx, s.opts.GracePeriod)
		for processAlive(old.PID) {
			select {
			case <-closeCtx.Done():
				done()
				return fmt.Errorf("launcher: orphan PID %d did not close", old.PID)
			case <-time.After(50 * time.Millisecond):
			}
		}
		done()
	}
	return nil
}
func readBinaryMetadata(ctx context.Context, path string) (BinaryMetadata, error) {
	var m BinaryMetadata
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probe, path, "version", "--json")
	out, e := cmd.Output()
	if e != nil {
		return m, fmt.Errorf("launcher: version probe: %w", e)
	}
	if len(out) > 16384 {
		return m, errors.New("launcher: oversized binary metadata")
	}
	if e = json.Unmarshal(out, &m); e != nil {
		return m, fmt.Errorf("launcher: binary requires manual protocol-2 bootstrap: %w", e)
	}
	if m.LauncherProtocol != Protocol || m.UsageGuard != 1 || m.Version == "" || m.Commit == "" || m.OS != runtime.GOOS || m.Arch != runtime.GOARCH || m.DatabaseSchema < 1 || m.MinDatabaseSchema < 1 || m.MinDatabaseSchema > m.DatabaseSchema {
		return m, errors.New("launcher: incompatible binary metadata; protocol-2 bootstrap required")
	}
	return m, nil
}
func removeIfExists(path string) error {
	e := os.Remove(path)
	if os.IsNotExist(e) {
		return nil
	}
	return e
}
func (s *Supervisor) WriteTokenFile() (string, error) {
	path := filepath.Join(s.opts.DataDir, "launcher.token")
	if e := atomicWrite(path, []byte(s.opts.ControlToken+"\n"), 0600); e != nil {
		return "", e
	}
	return path, nil
}
func (s *Supervisor) WriteStateFile() error {
	b, e := json.MarshalIndent(s.Status(), "", "  ")
	if e != nil {
		return e
	}
	return atomicWrite(filepath.Join(s.opts.DataDir, "launcher.json"), append(b, '\n'), 0600)
}
