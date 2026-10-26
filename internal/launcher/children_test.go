package launcher

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relaylab/internal/runtimecontrol"
)

// This subprocess owns the real launcher lock and real child. Killing it
// exercises an OS-level parent crash without running cleanup defers.
func TestCrashOwnerHelper(t *testing.T) {
	if os.Getenv("RELAYLAB_CRASH_OWNER_HELPER") != "1" {
		return
	}
	var opts Options
	if err := json.Unmarshal([]byte(os.Getenv("RELAYLAB_CRASH_OWNER_OPTIONS")), &opts); err != nil {
		t.Fatal(err)
	}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("RELAYLAB_CRASH_OWNER_CLEANUP") == "1" {
		if err = s.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	select {}
}

func waitReviewCondition(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatal("condition timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRecoveryUnknownSpawnIntentKeepsFence(t *testing.T) {
	s := newProcessSupervisor(t)
	intent := childIntent{Role: "serve", InstanceID: "unpersisted-pid", Executable: s.opts.RelayLabPath, StartedAt: time.Now().UTC()}
	if err := s.saveChildIntent(intent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(s.childIntentPath("serve")) })
	before, err := os.ReadFile(filepath.Join(s.opts.DataDir, "relaylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Recover(context.Background()); err == nil || !strings.Contains(err.Error(), "recovery_required") {
		t.Fatalf("unknown spawn outcome accepted: %v", err)
	}
	if !s.Status().Maintenance {
		t.Fatal("unknown child did not preserve fence")
	}
	after, err := os.ReadFile(filepath.Join(s.opts.DataDir, "relaylab.db"))
	if err != nil || string(after) != string(before) {
		t.Fatal("database changed with unresolved child")
	}
	if _, err = os.Stat(s.childIntentPath("serve")); err != nil {
		t.Fatal("unresolved intent was discarded")
	}
	if err = s.Resume(); err == nil {
		t.Fatal("resume bypassed unresolved spawn outcome")
	}
	if !s.Status().Maintenance {
		t.Fatal("resume removed the unresolved-child fence")
	}
}

func TestRecoveryDoesNotRestoreWhileUnreportedChildUsesDatabase(t *testing.T) {
	for _, scenario := range []string{"before-first-report", "after-report-removed"} {
		t.Run(scenario, func(t *testing.T) {
			s := newProcessSupervisor(t)
			opts := s.Options()
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			holdName := "hold-first-report-serve"
			if scenario == "after-report-removed" {
				holdName = "hold-cleanup-worker"
			}
			holdPath := filepath.Join(opts.DataDir, holdName)
			if err := os.WriteFile(holdPath, []byte("hold"), 0600); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(opts)
			if err != nil {
				t.Fatal(err)
			}
			owner := exec.Command(os.Args[0], "-test.run=^TestCrashOwnerHelper$")
			owner.Env = append(os.Environ(), "RELAYLAB_CRASH_OWNER_HELPER=1", "RELAYLAB_CRASH_OWNER_OPTIONS="+string(encoded))
			if scenario == "after-report-removed" {
				owner.Env = append(owner.Env, "RELAYLAB_CRASH_OWNER_CLEANUP=1")
			}
			if err = owner.Start(); err != nil {
				t.Fatal(err)
			}
			var childPID int
			role := "serve"
			if scenario == "after-report-removed" {
				role = "worker"
			}
			t.Cleanup(func() {
				_ = os.Remove(holdPath)
				_ = os.WriteFile(filepath.Join(opts.DataDir, "stop.request"), []byte("cleanup"), 0600)
				if owner.ProcessState == nil {
					_ = owner.Process.Kill()
					_ = owner.Wait()
				}
				if childPID > 0 {
					waitReviewCondition(t, func() bool { _, e := processIdentity(childPID); return os.IsNotExist(e) })
				}
			})
			waitReviewCondition(t, func() bool {
				b, e := os.ReadFile(filepath.Join(opts.DataDir, "launcher-child-"+role+".json"))
				if e != nil {
					return false
				}
				var intent childIntent
				if json.Unmarshal(b, &intent) != nil || intent.PID <= 0 || intent.ProcessIdentity == "" {
					return false
				}
				childPID = intent.PID
				release, e := runtimecontrol.AcquireUsage(opts.DataDir, role)
				if e == nil {
					_ = release()
					return false
				}
				_, e = os.Stat(filepath.Join(opts.DataDir, "runtime-"+role+".json"))
				if !os.IsNotExist(e) {
					return false
				}
				if scenario == "after-report-removed" {
					_, e = os.Stat(filepath.Join(opts.DataDir, role+".stop.request"))
					return e == nil
				}
				return true
			})
			if err = owner.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = owner.Wait(); err == nil {
				t.Fatal("helper unexpectedly exited successfully")
			}
			next, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = next.Close() })
			// A recoverable snapshot would overwrite this exact sentinel if
			// recovery incorrectly treated the missing report as child exit.
			old := filepath.Join(t.TempDir(), "old"+filepath.Ext(opts.RelayLabPath))
			if err = copyFile(fixturePaths["old"], old); err != nil {
				t.Fatal(err)
			}
			oldHash, _, err := SHA256File(old)
			if err != nil {
				t.Fatal(err)
			}
			meta, err := readBinaryMetadata(context.Background(), old)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := filepath.Join(t.TempDir(), "snapshot.zip")
			f, err := os.Create(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			zw := zip.NewWriter(f)
			for name, data := range map[string][]byte{"relaylab.db": []byte("restored-data"), "master.key": make([]byte, 32)} {
				w, e := zw.Create(name)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = w.Write(data); e != nil {
					t.Fatal(e)
				}
			}
			if err = zw.Close(); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			snapshotHash, _, err := SHA256File(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			txn := &Transaction{ID: "interrupted-start", OperationID: "interrupted-start", State: "starting", BinaryChanged: true, DataMayHaveChanged: true, OldBinary: old, OldSHA256: oldHash, OldMetadata: meta, Snapshot: snapshot, SnapshotSHA256: snapshotHash}
			if err = next.saveTransaction(txn); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			err = next.Recover(ctx)
			cancel()
			if err == nil || !strings.Contains(err.Error(), "recovery_required") {
				t.Fatalf("unreported live child accepted: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(opts.DataDir, "relaylab.db"))
			if err != nil || string(data) != "original-data" {
				t.Fatalf("restored under live child: %q %v", data, err)
			}
			if !next.Status().Maintenance {
				t.Fatal("live child did not preserve maintenance fence")
			}
			_ = os.Remove(holdPath)
			waitReviewCondition(t, func() bool { _, e := processIdentity(childPID); return os.IsNotExist(e) })
			if err = next.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			data, err = os.ReadFile(filepath.Join(opts.DataDir, "relaylab.db"))
			if err != nil || string(data) != "restored-data" {
				t.Fatalf("snapshot not restored after positive exit: %q %v", data, err)
			}
		})
	}
}
