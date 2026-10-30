package restorestate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJournalOwnershipRejectsCopiedOrUnboundRecords(t *testing.T) {
	parent := t.TempDir()
	first, second := filepath.Join(parent, "first"), filepath.Join(parent, "second")
	id := strings.Repeat("a", 32)
	if e := os.MkdirAll(first, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(second, 0700); e != nil {
		t.Fatal(e)
	}
	if Root(first) == Root(second) {
		t.Fatal("shared restore root")
	}
	if e := SaveOperation(first, Operation{ID: id, State: "replacing"}); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(OperationPath(first, id))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(OperationPath(second, id)), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(OperationPath(second, id), b, 0600); e != nil {
		t.Fatal(e)
	}
	var op Operation
	if e = ReadOperation(second, id, &op); e == nil {
		t.Fatal("copied foreign journal accepted")
	}
	if e = Save(OperationPath(second, id), Operation{ID: id, State: "replacing"}); e != nil {
		t.Fatal(e)
	}
	if e = ReadOperation(second, id, &op); e == nil {
		t.Fatal("legacy unbound journal accepted")
	}
}
func TestCleanupPreservesUnownedAndActiveStaging(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if e := os.MkdirAll(data, 0700); e != nil {
		t.Fatal(e)
	}
	id := strings.Repeat("b", 32)
	if e := os.MkdirAll(Stage(data, id), 0700); e != nil {
		t.Fatal(e)
	}
	p := Preflight{ID: id, Dir: Stage(data, id), ExpiresAt: time.Now().Add(-time.Hour)}
	if e := Save(PreflightPath(data, id), p); e != nil {
		t.Fatal(e)
	}
	Cleanup(data, time.Now().Add(2*time.Hour))
	if _, e := os.Stat(Stage(data, id)); e != nil {
		t.Fatal("unknown metadata deleted", e)
	}
	if e := SavePreflight(data, p); e != nil {
		t.Fatal(e)
	}
	op := Operation{ID: strings.Repeat("c", 32), PreflightID: id, State: "replacing"}
	if e := SaveOperation(data, op); e != nil {
		t.Fatal(e)
	}
	Cleanup(data, time.Now().Add(2*time.Hour))
	if _, e := os.Stat(Stage(data, id)); e != nil {
		t.Fatal("active staging deleted", e)
	}
	op.State = "succeeded"
	if e := SaveOperation(data, op); e != nil {
		t.Fatal(e)
	}
	Cleanup(data, time.Now().Add(2*time.Hour))
	if _, e := os.Stat(Stage(data, id)); !os.IsNotExist(e) {
		t.Fatal("expired owned staging not cleaned", e)
	}
}

func TestSubmissionSidecarDoesNotBlockExpiredPlaintextCleanup(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if e := os.MkdirAll(data, 0700); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	id, stageID := strings.Repeat("d", 32), strings.Repeat("e", 32)
	if e := SaveOperation(data, Operation{ID: id, State: "succeeded"}); e != nil {
		t.Fatal(e)
	}
	sidecar := filepath.Join(Root(data), "operations", id+".submission.json")
	if e := Save(sidecar, map[string]any{"operation_id": id, "unknown": true, "at": now.Add(-3 * time.Hour)}); e != nil {
		t.Fatal(e)
	}
	if e := SavePreflight(data, Preflight{ID: stageID, ExpiresAt: now.Add(-time.Minute)}); e != nil {
		t.Fatal(e)
	}
	Cleanup(data, now)
	if _, e := os.Stat(Stage(data, stageID)); !os.IsNotExist(e) {
		t.Fatal("submission prevented expired plaintext cleanup", e)
	}
	if _, e := os.Stat(sidecar); !os.IsNotExist(e) {
		t.Fatal("expired terminal submission was retained", e)
	}
}

func TestActiveOrMalformedSubmissionIsRetainedWithoutBlockingOtherStages(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			data := filepath.Join(t.TempDir(), "data")
			if e := os.MkdirAll(data, 0700); e != nil {
				t.Fatal(e)
			}
			now := time.Now().UTC()
			id, stageID := strings.Repeat("f", 32), strings.Repeat("1", 32)
			if active {
				if e := SaveOperation(data, Operation{ID: id, State: "replacing"}); e != nil {
					t.Fatal(e)
				}
			}
			sidecar := filepath.Join(Root(data), "operations", id+".submission.json")
			var payload any = map[string]any{"operation_id": "mismatch", "at": now.Add(-3 * time.Hour)}
			if active {
				payload = map[string]any{"operation_id": id, "unknown": true, "at": now.Add(-3 * time.Hour)}
			}
			if e := Save(sidecar, payload); e != nil {
				t.Fatal(e)
			}
			if e := SavePreflight(data, Preflight{ID: stageID, ExpiresAt: now.Add(-time.Minute)}); e != nil {
				t.Fatal(e)
			}
			Cleanup(data, now)
			if _, e := os.Stat(sidecar); e != nil {
				t.Fatal("active or unknown submission was removed", e)
			}
			if _, e := os.Stat(Stage(data, stageID)); !os.IsNotExist(e) {
				t.Fatal("submission blocked unrelated stage cleanup", e)
			}
		})
	}
}

func TestDamagedFormalJournalStillStopsPlaintextCleanup(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if e := os.MkdirAll(data, 0700); e != nil {
		t.Fatal(e)
	}
	id, stageID := strings.Repeat("2", 32), strings.Repeat("3", 32)
	if e := Save(OperationPath(data, id), map[string]string{"id": id, "data_dir": "unknown"}); e != nil {
		t.Fatal(e)
	}
	if e := SavePreflight(data, Preflight{ID: stageID, ExpiresAt: time.Now().Add(-time.Hour)}); e != nil {
		t.Fatal(e)
	}
	Cleanup(data, time.Now())
	if _, e := os.Stat(Stage(data, stageID)); e != nil {
		t.Fatal("damaged formal recovery journal did not guard plaintext staging", e)
	}
}
