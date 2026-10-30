package update

import (
	"context"
	"testing"
)

func TestOperationReservationSerializesAcceptance(t *testing.T) {
	_, m := newFixture(t)
	release, ok := m.TryReserveOperation()
	if !ok {
		t.Fatal("first reservation failed")
	}
	contender := make(chan bool)
	go func() {
		unlock, acquired := m.TryReserveOperation()
		if acquired {
			unlock()
		}
		contender <- acquired
	}()
	if <-contender {
		t.Fatal("concurrent operation bypassed reservation")
	}
	release()
	release() // Early returns and handoff cleanup are idempotent.
	nextRelease, ok := m.TryReserveOperation()
	if !ok {
		t.Fatal("completed operation did not release reservation")
	}
	nextRelease()
}

func TestFailedCheckKindSurvivesRecovery(t *testing.T) {
	f, m := newFixture(t)
	f.manifest.Commit = "invalid"
	snapshot, err := m.Check(context.Background())
	if err == nil || snapshot.Operation == nil || snapshot.Operation.Kind != "check" || snapshot.Operation.State != "failed" {
		t.Fatalf("failed check lost operation identity: %+v %v", snapshot, err)
	}
	recovered, err := New(m.Config(), m.dataDir, m.build)
	if err != nil {
		t.Fatal(err)
	}
	if got := recovered.Snapshot().Operation; got == nil || got.Kind != "check" || got.State != "failed" {
		t.Fatalf("recovered operation lost check failure: %+v", got)
	}
}
