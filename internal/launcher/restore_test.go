package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relaylab/internal/backup"
	"relaylab/internal/domain"
	"relaylab/internal/operationlock"
	"relaylab/internal/restorestate"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

func realRestoreDatabase(t *testing.T, dir, label string, key byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "master.key"), bytes.Repeat([]byte{key}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := security.LoadVault(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "relaylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err = st.SavePlatform(ctx, domain.Platform{ID: "platform", Name: label}); err != nil {
		t.Fatal(err)
	}
	cipher, err := vault.Encrypt("secret-" + label)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SaveChannel(ctx, domain.Channel{ID: "channel", PlatformID: "platform", Name: label, EncryptedKey: cipher}); err != nil {
		t.Fatal(err)
	}
}

func realRestoreSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	s := newProcessSupervisor(t)
	if err := os.Remove(filepath.Join(s.opts.DataDir, "relaylab.db")); err != nil {
		t.Fatal(err)
	}
	realRestoreDatabase(t, s.opts.DataDir, "current", 0x31)
	return s
}

func assertRestoredPair(t *testing.T, dir, label string) {
	t.Helper()
	vault, err := security.LoadVault(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenExisting(filepath.Join(dir, "relaylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := st.GetChannel(context.Background(), "channel")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := vault.Decrypt(c.EncryptedKey)
	if err != nil || plain != "secret-"+label {
		t.Fatalf("DB and master key mismatch: %s %v", label, err)
	}
}

func restorePreflightFixture(t *testing.T, s *Supervisor, id string) restorestate.Preflight {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	realRestoreDatabase(t, source, "restored", 0x72)
	archive := filepath.Join(t.TempDir(), "source.zip")
	if err := backup.Create(context.Background(), source, archive, backup.Options{Legacy: true}); err != nil {
		t.Fatal(err)
	}
	v, err := backup.Validate(context.Background(), archive, "", restorestate.Stage(s.opts.DataDir, id))
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(v.Manifest)
	p := restorestate.Preflight{ID: id, Dir: v.Dir, Manifest: manifest, ExpiresAt: time.Now().Add(time.Hour)}
	p.DBHash, _, err = SHA256File(v.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	p.KeyHash, _, err = SHA256File(v.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = restorestate.SavePreflight(s.opts.DataDir, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRestorePreflightRejectsExpiredEscapedAndChangedFiles(t *testing.T) {
	for _, tc := range []string{"expired", "escaped", "changed"} {
		t.Run(tc, func(t *testing.T) {
			s := realRestoreSupervisor(t)
			id := strings.Repeat("a", 32)
			p := restorePreflightFixture(t, s, id)
			switch tc {
			case "expired":
				p.ExpiresAt = time.Now().Add(-time.Second)
			case "escaped":
				p.Dir = t.TempDir()
			case "changed":
				if err := os.WriteFile(filepath.Join(p.Dir, "master.key"), bytes.Repeat([]byte{3}, 32), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := restorestate.SavePreflight(s.opts.DataDir, p); err != nil {
				t.Fatal(err)
			}
			if _, err := s.loadRestorePreflight(id); err == nil {
				t.Fatal("untrusted preflight accepted")
			}
			assertRestoredPair(t, s.opts.DataDir, "current")
		})
	}
}

func TestRestoreCannotRunWhileAnotherProcessOperationOwnsLock(t *testing.T) {
	s := realRestoreSupervisor(t)
	release, err := operationlock.Acquire(s.opts.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	op := restorestate.Operation{ID: strings.Repeat("b", 32), PreflightID: strings.Repeat("a", 32), State: "accepted"}
	err = s.restoreLocked(context.Background(), &op)
	if !errors.Is(err, operationlock.ErrBusy) || op.State != "failed" || op.ErrorCode != "operation_busy" {
		t.Fatal("operation lock ignored", err, op)
	}
	assertRestoredPair(t, s.opts.DataDir, "current")
}

func TestRestoreReplacesPairAndPreservesCurrentSafetySnapshot(t *testing.T) {
	s := realRestoreSupervisor(t)
	preflightID := strings.Repeat("a", 32)
	restorePreflightFixture(t, s, preflightID)
	startFixture(t, s)
	op := restorestate.Operation{ID: strings.Repeat("b", 32), PreflightID: preflightID, State: "accepted", StartedAt: time.Now().UTC()}
	if err := s.restoreLocked(context.Background(), &op); err != nil {
		t.Fatal(err)
	}
	if op.State != "succeeded" || s.Status().Maintenance {
		t.Fatal("restore did not finish", op, s.Status())
	}
	assertRestoredPair(t, s.opts.DataDir, "restored")
	assertRestoredPair(t, op.SafetyDir, "current")
	st, err := store.OpenExisting(filepath.Join(s.opts.DataDir, "relaylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	policy, err := st.BackupPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if policy.Enabled || !policy.ReconciliationRequired {
		t.Fatal("restored backup index not fenced", policy)
	}
}

func TestRestoreRecoveryRestoresMatchingPairAfterEachInterruptedReplacement(t *testing.T) {
	for _, stage := range []string{"before_replace", "after_db", "after_key", "starting"} {
		t.Run(stage, func(t *testing.T) {
			s := realRestoreSupervisor(t)
			opID := strings.Repeat("c", 32)
			safety := filepath.Join(restorestate.Root(s.opts.DataDir), "safety", opID)
			if err := os.MkdirAll(safety, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"relaylab.db", "master.key"} {
				if err := copyFile(filepath.Join(s.opts.DataDir, name), filepath.Join(safety, name)); err != nil {
					t.Fatal(err)
				}
			}
			op := restorestate.Operation{ID: opID, State: "replacing", SafetyDir: safety, DataChanged: true}
			op.SafetyDBHash, _, _ = SHA256File(filepath.Join(safety, "relaylab.db"))
			op.SafetyKeyHash, _, _ = SHA256File(filepath.Join(safety, "master.key"))
			source := filepath.Join(t.TempDir(), "new-source")
			realRestoreDatabase(t, source, "restored", 0x72)
			if stage != "before_replace" {
				if err := copyFile(filepath.Join(source, "relaylab.db"), filepath.Join(s.opts.DataDir, "relaylab.db")); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "after_key" || stage == "starting" {
				if err := copyFile(filepath.Join(source, "master.key"), filepath.Join(s.opts.DataDir, "master.key")); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "starting" {
				op.State = "starting"
			}
			if err := restorestate.SaveOperation(s.opts.DataDir, op); err != nil {
				t.Fatal(err)
			}
			if err := restorestate.SaveCurrent(s.opts.DataDir, opID); err != nil {
				t.Fatal(err)
			}
			handled, err := s.recoverRestore(context.Background())
			if err != nil || !handled {
				t.Fatal("interrupted restore not recovered", handled, err)
			}
			assertRestoredPair(t, s.opts.DataDir, "current")
			current, err := s.restoreCurrent()
			if err != nil || current.State != "rolled_back" || current.DataChanged {
				t.Fatal("recovery journal wrong", current, err)
			}
			if s.Status().Maintenance || !s.Status().Worker.Running {
				t.Fatal("service did not resume", s.Status())
			}
		})
	}
}

func TestRestoreRecoveryRejectsTamperedSafetyAndKeepsMaintenance(t *testing.T) {
	s := realRestoreSupervisor(t)
	id := strings.Repeat("d", 32)
	dir := filepath.Join(restorestate.Root(s.opts.DataDir), "safety", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	op := restorestate.Operation{ID: id, State: "replacing", SafetyDir: dir, SafetyDBHash: strings.Repeat("0", 64), SafetyKeyHash: strings.Repeat("0", 64), DataChanged: true}
	for _, name := range []string{"relaylab.db", "master.key"} {
		if err := copyFile(filepath.Join(s.opts.DataDir, name), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := restorestate.SaveOperation(s.opts.DataDir, op); err != nil {
		t.Fatal(err)
	}
	if err := restorestate.SaveCurrent(s.opts.DataDir, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.recoverRestore(context.Background()); err == nil {
		t.Fatal("tampered safety accepted")
	}
	if !s.Status().Maintenance {
		t.Fatal("unsafe restore resumed service")
	}
	current, err := s.restoreCurrent()
	if err != nil || current.State != "recovery_required" {
		t.Fatal("missing recovery required state", current, err)
	}
	assertRestoredPair(t, s.opts.DataDir, "current")
}

func TestRestoreCommittedRecoveryNeverRollsBackNewData(t *testing.T) {
	s := realRestoreSupervisor(t)
	id := strings.Repeat("e", 32)
	source := filepath.Join(t.TempDir(), "committed-source")
	realRestoreDatabase(t, source, "restored", 0x72)
	if err := s.installRestoreFiles(source); err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenExisting(filepath.Join(s.opts.DataDir, "relaylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SavePlatform(context.Background(), domain.Platform{ID: "after-commit", Name: "new data after commit"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	op := restorestate.Operation{ID: id, State: "committed", DataChanged: true}
	if err = restorestate.SaveOperation(s.opts.DataDir, op); err != nil {
		t.Fatal(err)
	}
	if err = restorestate.SaveCurrent(s.opts.DataDir, id); err != nil {
		t.Fatal(err)
	}
	handled, err := s.recoverRestore(context.Background())
	if err != nil || handled {
		t.Fatal("committed operation did not proceed to normal startup", handled, err)
	}
	assertRestoredPair(t, s.opts.DataDir, "restored")
	st, err = store.OpenExisting(filepath.Join(s.opts.DataDir, "relaylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.GetPlatform(context.Background(), "after-commit")
	if err != nil || p.Name != "new data after commit" {
		t.Fatal("post-commit writes lost", p, err)
	}
	current, err := s.restoreCurrent()
	if err != nil || current.State != "succeeded" {
		t.Fatal("committed journal not finalized", current, err)
	}
}

func TestRestoreSiblingInstanceIgnoresOtherInstanceJournal(t *testing.T) {
	first := realRestoreSupervisor(t)
	secondDir := filepath.Join(filepath.Dir(first.opts.DataDir), "other-instance")
	realRestoreDatabase(t, secondDir, "other-instance", 0x51)
	second, e := New(Options{RelayLabPath: first.opts.RelayLabPath, DataDir: secondDir, StartupTimeout: first.opts.StartupTimeout, DrainTimeout: first.opts.DrainTimeout, GracePeriod: first.opts.GracePeriod})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = second.Close() })
	id := strings.Repeat("7", 32)
	safety := filepath.Join(restorestate.Root(first.opts.DataDir), "safety", id)
	if e = os.MkdirAll(safety, 0700); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"relaylab.db", "master.key"} {
		if e = copyFile(filepath.Join(first.opts.DataDir, name), filepath.Join(safety, name)); e != nil {
			t.Fatal(e)
		}
	}
	op := restorestate.Operation{ID: id, State: "replacing", DataChanged: true, SafetyDir: safety}
	op.SafetyDBHash, _, _ = SHA256File(filepath.Join(safety, "relaylab.db"))
	op.SafetyKeyHash, _, _ = SHA256File(filepath.Join(safety, "master.key"))
	if e = restorestate.SaveOperation(first.opts.DataDir, op); e != nil {
		t.Fatal(e)
	}
	if e = restorestate.SaveCurrent(first.opts.DataDir, id); e != nil {
		t.Fatal(e)
	}
	if handled, e := second.recoverRestore(context.Background()); e != nil || handled {
		t.Fatal("foreign journal executed", handled, e)
	}
	assertRestoredPair(t, secondDir, "other-instance")
}
func TestRestoreRejectsLegacyUnboundJournalWithoutMigration(t *testing.T) {
	s := realRestoreSupervisor(t)
	canonical, e := operationlock.CanonicalDataDir(s.opts.DataDir)
	if e != nil {
		t.Fatal(e)
	}
	legacy := filepath.Join(filepath.Dir(canonical), "backups", ".restore", "current.json")
	if e = restorestate.Save(legacy, map[string]string{"id": strings.Repeat("8", 32)}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.restoreCurrent(); e == nil {
		t.Fatal("legacy unbound pointer silently ignored")
	}
	assertRestoredPair(t, s.opts.DataDir, "current")
	if _, e = os.Stat(legacy); e != nil {
		t.Fatal("legacy evidence modified", e)
	}
}
func TestRestorePartialStopFailureRestartsBothOriginalServices(t *testing.T) {
	s := realRestoreSupervisor(t)
	s.opts.GracePeriod = 100 * time.Millisecond
	s.opts.DrainTimeout = 500 * time.Millisecond
	id := strings.Repeat("9", 32)
	restorePreflightFixture(t, s, id)
	startFixture(t, s)
	hold := filepath.Join(s.opts.DataDir, "hold-cleanup-worker")
	if e := os.WriteFile(hold, []byte("hold"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = os.Remove(hold) })
	go func() {
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			_, stopErr := os.Stat(filepath.Join(s.opts.DataDir, "worker.stop.request"))
			_, reportErr := os.Stat(filepath.Join(s.opts.DataDir, "runtime-worker.json"))
			if stopErr == nil && os.IsNotExist(reportErr) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(2 * s.opts.GracePeriod)
		_ = os.Remove(hold)
	}()
	op := restorestate.Operation{ID: strings.Repeat("6", 32), PreflightID: id, State: "accepted"}
	if e := s.restoreLocked(context.Background(), &op); e == nil {
		t.Fatal("expected initial stop timeout")
	}
	if op.State != "failed" || op.ErrorCode != "drain_failed" {
		t.Fatal("wrong failed operation", op)
	}
	status := s.Status()
	if !status.Serve.Running || !status.Worker.Running || status.Maintenance {
		t.Fatal("original services not restored", status)
	}
	assertRestoredPair(t, s.opts.DataDir, "current")
}

func TestRestorePendingJournalCannotBeBypassedByPublicLifecycleControls(t *testing.T) {
	for _, action := range []string{"start", "resume", "restart"} {
		t.Run(action, func(t *testing.T) {
			s := realRestoreSupervisor(t)
			id := strings.Repeat("5", 32)
			if e := restorestate.SaveOperation(s.opts.DataDir, restorestate.Operation{ID: id, State: "recovery_required", DataChanged: true}); e != nil {
				t.Fatal(e)
			}
			if e := restorestate.SaveCurrent(s.opts.DataDir, id); e != nil {
				t.Fatal(e)
			}
			if e := s.Drain(); e != nil {
				t.Fatal(e)
			}
			var e error
			switch action {
			case "start":
				e = s.Start(context.Background())
			case "resume":
				e = s.Resume()
			case "restart":
				e = s.Restart(context.Background())
			}
			if e == nil {
				t.Fatal("public lifecycle control bypassed restore fence")
			}
			if !s.Status().Maintenance {
				t.Fatal("pending restore maintenance was lifted")
			}
		})
	}
}
