package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var fixturePaths map[string]string

func fixtureVersion(label string) string {
	return map[string]string{"old": "1.0.0", "new": "1.1.0", "bad": "1.2.0"}[label]
}

func TestMain(m *testing.M) {
	dir, e := os.MkdirTemp("", "relaylab-launcher-fixtures-")
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	fixturePaths = make(map[string]string)
	for _, v := range []string{"old", "new", "bad"} {
		name := v
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+v, "-o", path, "./testdata/fixture")
		if out, e := cmd.CombinedOutput(); e != nil {
			fmt.Fprintf(os.Stderr, "fixture build: %v\n%s", e, out)
			os.RemoveAll(dir)
			os.Exit(1)
		}
		fixturePaths[v] = path
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
func newProcessSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data with spaces")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	executable := filepath.Join(t.TempDir(), "relaylab"+filepath.Ext(fixturePaths["old"]))
	if e := copyFile(fixturePaths["old"], executable); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "relaylab.db"), []byte("original-data"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "master.key"), make([]byte, 32), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := New(Options{RelayLabPath: executable, DataDir: dir, GracePeriod: 2 * time.Second, StartupTimeout: 750 * time.Millisecond, DrainTimeout: 250 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(s.opts.DataDir, "hold-active"))
		_ = os.Remove(filepath.Join(s.opts.DataDir, "wrong-identity"))
		if e := s.Close(); e != nil {
			t.Errorf("cleanup: %v", e)
		}
	})
	return s
}
func stagedRequest(t *testing.T, s *Supervisor, version, id string) InstallRequest {
	t.Helper()
	path := filepath.Join(s.opts.DataDir, "updates", id, "relaylab"+filepath.Ext(s.opts.RelayLabPath))
	if e := copyFile(fixturePaths[version], path); e != nil {
		t.Fatal(e)
	}
	sha, size, e := SHA256File(path)
	if e != nil {
		t.Fatal(e)
	}
	return InstallRequest{OperationID: id, StagedPath: path, SHA256: sha, Size: size, Version: fixtureVersion(version), Commit: "commit-" + version, LauncherProtocol: Protocol, DatabaseSchema: 1, MinDatabaseSchema: 1}
}
func startFixture(t *testing.T, s *Supervisor) {
	t.Helper()
	if e := s.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestProcessFreshDirectoryWaitsForServeInitializationBeforeWorker(t *testing.T) {
	s := newProcessSupervisor(t)
	for _, name := range []string{"relaylab.db", "master.key"} {
		if err := os.Remove(filepath.Join(s.opts.DataDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	startFixture(t, s)
	state := s.Status()
	if !state.Serve.Running || !state.Worker.Running || state.State != StateRunning {
		t.Fatalf("fresh startup failed: %+v", state)
	}
	for _, c := range s.children() {
		r, err := s.readReport(c)
		if err != nil || !r.Ready {
			t.Fatalf("fresh child %s not verified: %+v %v", c.role, r, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(s.opts.DataDir, "relaylab.db"))
	if err != nil || string(data) != "original-data" {
		t.Fatalf("database initialization=%q err=%v", data, err)
	}
}

func TestProcessUpdateCommitsThenManualRollbackPreservesCurrentData(t *testing.T) {
	s := newProcessSupervisor(t)
	startFixture(t, s)
	oldPID := s.Status().Worker.PID
	req := stagedRequest(t, s, "new", "successful-update")
	req.BackupDir = filepath.Join(t.TempDir(), "caller-cannot-select")
	if e := s.Install(context.Background(), req); e != nil {
		t.Fatal(e)
	}
	state := s.Status()
	if state.Transaction.State != "succeeded" || state.Maintenance || state.Worker.PID == oldPID {
		t.Fatalf("bad committed state: %+v", state)
	}
	if _, e := os.Stat(filepath.Join(s.opts.BackupDir, "update", req.OperationID, "snapshot.zip")); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(s.opts.DataDir, "relaylab.db"), []byte("new-user-data"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := s.RollbackLatest(context.Background(), "ignored"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.latestRollback(context.Background()); e == nil {
		t.Fatal("rollback offered a newer executable after reverting")
	}
	m, e := readBinaryMetadata(context.Background(), s.opts.RelayLabPath)
	if e != nil || m.Version != fixtureVersion("old") {
		t.Fatalf("rollback metadata=%+v err=%v", m, e)
	}
	b, e := os.ReadFile(filepath.Join(s.opts.DataDir, "relaylab.db"))
	if e != nil || string(b) != "new-user-data" {
		t.Fatalf("manual rollback lost current data=%q err=%v", b, e)
	}
	events, _ := os.ReadFile(filepath.Join(s.opts.DataDir, "fixture-events.log"))
	if strings.Index(string(events), "worker stop old") > strings.Index(string(events), "serve stop old") {
		t.Fatalf("serve stopped before worker: %s", events)
	}
}

func TestProcessUpdateBadStartupRestoresSnapshotAndOldBinary(t *testing.T) {
	s := newProcessSupervisor(t)
	startFixture(t, s)
	req := stagedRequest(t, s, "bad", "bad-startup")
	if e := s.Install(context.Background(), req); e == nil {
		t.Fatal("expected startup failure")
	}
	state := s.Status()
	if state.Transaction.State != "rolled_back" || state.Maintenance || !state.Worker.Running || !state.Serve.Running {
		t.Fatalf("rollback incomplete: %+v", state)
	}
	b, _ := os.ReadFile(filepath.Join(s.opts.DataDir, "relaylab.db"))
	if string(b) != "original-data" {
		t.Fatalf("snapshot not restored: %q", b)
	}
	if _, e := os.Stat(filepath.Join(s.opts.DataDir, "launcher-update.json")); e != nil {
		t.Fatalf("journal lost during restore: %v", e)
	}
}

func TestProcessDrainTimeoutNeverKillsActiveChildrenOrReplacesBinary(t *testing.T) {
	s := newProcessSupervisor(t)
	startFixture(t, s)
	before := s.Status()
	if e := os.WriteFile(filepath.Join(s.opts.DataDir, "hold-active"), []byte("1"), 0600); e != nil {
		t.Fatal(e)
	}
	time.Sleep(60 * time.Millisecond)
	req := stagedRequest(t, s, "new", "drain-timeout")
	if e := s.Install(context.Background(), req); e == nil {
		t.Fatal("expected drain timeout")
	}
	after := s.Status()
	if !after.Worker.Running || !after.Serve.Running || after.Worker.PID != before.Worker.PID || after.Serve.PID != before.Serve.PID || after.Maintenance {
		t.Fatalf("active children changed: %+v", after)
	}
	m, e := readBinaryMetadata(context.Background(), s.opts.RelayLabPath)
	if e != nil || m.Version != fixtureVersion("old") {
		t.Fatalf("replaced during active work: %+v %v", m, e)
	}
}

func TestProcessDrainWaitsForBothRolesBeforeBackup(t *testing.T) {
	s := newProcessSupervisor(t)
	startFixture(t, s)
	s.opts.DrainTimeout = 2 * time.Second
	if e := os.WriteFile(filepath.Join(s.opts.DataDir, "hold-active"), []byte("1"), 0600); e != nil {
		t.Fatal(e)
	}
	time.Sleep(60 * time.Millisecond)
	done := make(chan error, 1)
	req := stagedRequest(t, s, "new", "wait-drain")
	go func() { done <- s.Install(context.Background(), req) }()
	time.Sleep(120 * time.Millisecond)
	if _, e := os.Stat(filepath.Join(s.opts.BackupDir, "update", req.OperationID, "snapshot.zip")); !os.IsNotExist(e) {
		t.Fatalf("snapshot taken before drain: %v", e)
	}
	select {
	case e := <-done:
		t.Fatalf("update finished with active work: %v", e)
	default:
	}
	if e := os.Remove(filepath.Join(s.opts.DataDir, "hold-active")); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestProcessBothReadinessAndGenerationIdentityRequired(t *testing.T) {
	s := newProcessSupervisor(t)
	if e := os.WriteFile(filepath.Join(s.opts.DataDir, "wrong-identity"), []byte("1"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := s.Start(context.Background()); e == nil {
		t.Fatal("unrelated instance report accepted")
	}
	if e := os.Remove(filepath.Join(s.opts.DataDir, "wrong-identity")); e != nil {
		t.Fatal(e)
	}
	startFixture(t, s)
	first := s.children()
	if e := s.Start(context.Background()); !errors.Is(e, ErrAlreadyRunning) {
		t.Fatalf("double start: %v", e)
	}
	if e := s.Stop(context.Background()); e != nil {
		t.Fatal(e)
	}
	startFixture(t, s)
	current := s.children()
	if first[0] == current[0] || first[1] == current[1] {
		t.Fatal("child identity was reused")
	}
	select {
	case <-current[0].done:
		t.Fatal("old generation exit completed current generation")
	default:
	}
}

func TestProcessDeferredInstallRecoversAtNextLaunch(t *testing.T) {
	s := newProcessSupervisor(t)
	startFixture(t, s)
	req := stagedRequest(t, s, "new", "pending-restart")
	req.DeferRestart = true
	if e := s.Install(context.Background(), req); e != nil {
		t.Fatal(e)
	}
	if s.Status().Transaction.State != "pending_restart" || s.Status().Maintenance {
		t.Fatal("not deferred")
	}
	opts := s.Options()
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	next, e := New(opts)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	if e = next.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	if next.Status().Transaction.State != "succeeded" || next.Status().Maintenance {
		t.Fatalf("pending restart not committed: %+v", next.Status())
	}
}

func TestDataDirectoryLockAndMutationMethods(t *testing.T) {
	s := newProcessSupervisor(t)
	if other, e := New(s.Options()); e == nil {
		other.Close()
		t.Fatal("double launcher acquired data directory")
	}
	request := httptest.NewRequest(http.MethodGet, "/control/drain", nil)
	request.RemoteAddr = "127.0.0.1:1000"
	request.Header.Set("Authorization", "Bearer "+s.Token())
	response := httptest.NewRecorder()
	s.controlHandler().ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || s.Status().Maintenance {
		t.Fatalf("GET mutation accepted: %d", response.Code)
	}
}

func TestInstallRejectsHashMetadataAndStagingEscapesBeforeDowntime(t *testing.T) {
	s := newProcessSupervisor(t)
	startFixture(t, s)
	req := stagedRequest(t, s, "new", "bad-hash")
	req.SHA256 = strings.Repeat("0", 64)
	if e := s.Install(context.Background(), req); e == nil {
		t.Fatal("bad hash accepted")
	}
	req = stagedRequest(t, s, "new", "wrong-manifest")
	req.Commit = "wrong"
	if e := s.Install(context.Background(), req); e == nil {
		t.Fatal("wrong embedded commit accepted")
	}
	req = stagedRequest(t, s, "new", "outside-path")
	req.StagedPath = fixturePaths["new"]
	if e := s.Install(context.Background(), req); e == nil {
		t.Fatal("outside stage accepted")
	}
	if s.Status().Maintenance || !s.Status().Serve.Running {
		t.Fatal("validation interrupted service")
	}
}

func TestHTTPAcceptanceDetachedFromCancelledRequest(t *testing.T) {
	s := newProcessSupervisor(t)
	startFixture(t, s)
	req := stagedRequest(t, s, "new", "async-http")
	b, _ := json.Marshal(req)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/control/replace", strings.NewReader(string(b))).WithContext(ctx)
	request.RemoteAddr = "127.0.0.1:9000"
	request.Header.Set("X-RelayLab-Launcher-Token", s.Token())
	response := httptest.NewRecorder()
	s.controlHandler().ServeHTTP(response, request)
	cancel()
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d %s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		txn := s.transactionCopy()
		if txn.State == "succeeded" {
			return
		}
		if txn.State == "failed" || txn.State == "rolled_back" {
			t.Fatalf("request cancellation aborted update: %+v", txn)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("asynchronous operation did not complete")
}

func TestProcessFailureBeforeBackupRestartsOldWithoutRestoringData(t *testing.T) {
	s := newProcessSupervisor(t)
	startFixture(t, s)
	if e := os.WriteFile(s.opts.BackupDir, []byte("backup-root-is-a-file"), 0600); e != nil {
		t.Fatal(e)
	}
	req := stagedRequest(t, s, "new", "backup-fails")
	if e := s.Install(context.Background(), req); e == nil {
		t.Fatal("backup path failure ignored")
	}
	state := s.Status()
	if state.Transaction.BinaryChanged || state.Transaction.State != "failed" || state.Maintenance || !state.Serve.Running || !state.Worker.Running {
		t.Fatalf("pre-backup recovery incorrect: %+v", state)
	}
	b, _ := os.ReadFile(filepath.Join(s.opts.DataDir, "relaylab.db"))
	if string(b) != "original-data" {
		t.Fatalf("data modified on pre-backup failure: %q", b)
	}
}

func TestProcessInterruptedRecoveryRestoresBeforeStartingChildren(t *testing.T) {
	s := newProcessSupervisor(t)
	startFixture(t, s)
	req := stagedRequest(t, s, "new", "interrupted-update")
	if e := s.Install(context.Background(), req); e != nil {
		t.Fatal(e)
	}
	if e := s.Stop(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(s.opts.DataDir, "relaylab.db"), []byte("interrupted-migration"), 0600); e != nil {
		t.Fatal(e)
	}
	txn := s.transactionCopy()
	txn.State = "starting"
	if e := s.saveTransaction(txn); e != nil {
		t.Fatal(e)
	}
	opts := s.Options()
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	next, e := New(opts)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	if e = next.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(opts.DataDir, "relaylab.db"))
	if string(b) != "original-data" {
		t.Fatalf("interrupted data published: %q", b)
	}
	m, e := readBinaryMetadata(context.Background(), opts.RelayLabPath)
	if e != nil || m.Version != fixtureVersion("old") {
		t.Fatalf("recovery binary: %+v %v", m, e)
	}
	if next.Status().Transaction.State != "rolled_back" || next.Status().Maintenance || !next.Status().Worker.Running {
		t.Fatalf("interrupted recovery incomplete: %+v", next.Status())
	}
}

func TestRecoveryRejectsCorruptedBackupAndLeavesDataFenced(t *testing.T) {
	for _, member := range []string{"executable", "snapshot"} {
		t.Run(member, func(t *testing.T) {
			s := newProcessSupervisor(t)
			startFixture(t, s)
			req := stagedRequest(t, s, "new", "corrupt-"+member)
			if e := s.Install(context.Background(), req); e != nil {
				t.Fatal(e)
			}
			if e := s.Stop(context.Background()); e != nil {
				t.Fatal(e)
			}
			txn := s.transactionCopy()
			txn.State = "starting"
			target := txn.Snapshot
			if member == "executable" {
				target = txn.OldBinary
			}
			if e := os.WriteFile(target, []byte("corrupted"), 0600); e != nil {
				t.Fatal(e)
			}
			if e := s.saveTransaction(txn); e != nil {
				t.Fatal(e)
			}
			opts := s.Options()
			if e := s.Close(); e != nil {
				t.Fatal(e)
			}
			next, e := New(opts)
			if e != nil {
				t.Fatal(e)
			}
			defer next.Close()
			if e = next.Recover(context.Background()); e == nil {
				t.Fatal("corrupt backup accepted")
			}
			if !next.Status().Maintenance || next.Status().Serve.PID != 0 || next.Status().Worker.PID != 0 {
				t.Fatalf("unsafe recovery state: %+v", next.Status())
			}
		})
	}
}

func TestProcessControlShutdownJoinsChildrenAndReleasesLock(t *testing.T) {
	s := newProcessSupervisor(t)
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()
	deadline := time.Now().Add(3 * time.Second)
	for s.Status().State != StateRunning && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	status := s.Status()
	if status.State != StateRunning {
		t.Fatalf("serve never started: %+v", status)
	}
	request, e := http.NewRequest(http.MethodPost, "http://"+status.ControlAddress+"/control/shutdown", nil)
	if e != nil {
		t.Fatal(e)
	}
	request.Header.Set("Authorization", "Bearer "+s.Token())
	response, e := http.DefaultClient.Do(request)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("shutdown HTTP %d", response.StatusCode)
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not join children")
	}
	if s.Status().Serve.PID != 0 || s.Status().Worker.PID != 0 {
		t.Fatalf("children survived shutdown: %+v", s.Status())
	}
	next, e := New(s.Options())
	if e != nil {
		t.Fatal(e)
	}
	if e = next.Close(); e != nil {
		t.Fatal(e)
	}
}

func TestNewTransactionPreservesUnfinishedJournal(t *testing.T) {
	for _, state := range []string{"accepted", "checking", "draining", "stopped", "backed_up", "replacing", "starting", "validating", "rolling_back", "pending_restart", "recovery_required", "future_phase"} {
		t.Run(state, func(t *testing.T) {
			s := newProcessSupervisor(t)
			old, err := s.newTransaction(InstallRequest{OperationID: "existing-operation"}, "update")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.phase(old, state, 10, "existing durable transaction", ""); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(s.journalPath())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.newTransaction(InstallRequest{OperationID: "replacement-operation"}, "update"); err == nil {
				t.Fatalf("unfinished %s journal overwritten", state)
			}
			after, err := os.ReadFile(s.journalPath())
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("journal changed after rejected operation")
			}
		})
	}
}

func TestNewTransactionOnlyAllowsDurableTerminalStates(t *testing.T) {
	for _, test := range []struct {
		state   string
		changed bool
		allowed bool
	}{{"succeeded", true, true}, {"rolled_back", true, true}, {"failed", false, true}, {"failed", true, false}} {
		t.Run(fmt.Sprintf("%s_changed_%t", test.state, test.changed), func(t *testing.T) {
			s := newProcessSupervisor(t)
			old, err := s.newTransaction(InstallRequest{OperationID: "completed-operation"}, "update")
			if err != nil {
				t.Fatal(err)
			}
			old.BinaryChanged = test.changed
			if err = s.phase(old, test.state, 100, "previous operation", ""); err != nil {
				t.Fatal(err)
			}
			_, err = s.newTransaction(InstallRequest{OperationID: "next-operation"}, "update")
			if (err == nil) != test.allowed {
				t.Fatalf("state %s changed=%t allowed=%t err=%v", test.state, test.changed, test.allowed, err)
			}
		})
	}
}
