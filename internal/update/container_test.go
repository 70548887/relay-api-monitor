package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"relaylab/internal/config"
)

func TestContainerChecksAndRejectsInstallOperationsWithoutSideEffects(t *testing.T) {
	f, m := newFixture(t)
	m.cfg.InstallMode = "container"
	if m.CanInstall() {
		t.Fatal("container installation enabled")
	}
	snap, err := m.Check(context.Background())
	if err != nil || !snap.Enabled || snap.InstallEnabled || snap.InstallMode != "container" || snap.Latest == nil || !snap.Latest.Available {
		t.Fatalf("container release check failed: %+v %v", snap, err)
	}
	before := m.Snapshot()
	journal, err := os.ReadFile(filepath.Join(m.dataDir, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	requests := 0
	for _, count := range f.requests {
		requests += count
	}
	f.mu.Unlock()
	called := false
	m.SetApplier(func(context.Context, string, UpdateInfo) error { called = true; return nil })
	m.SetRollback(func(context.Context) error { called = true; return nil })
	for _, op := range []struct {
		name string
		fn   func(context.Context) (Snapshot, error)
	}{{"download", m.Download}, {"apply", m.Apply}, {"rollback", m.Rollback}} {
		t.Run(op.name, func(t *testing.T) {
			got, err := op.fn(context.Background())
			if !errors.Is(err, ErrContainerManaged) || !reflect.DeepEqual(got, before) || called {
				t.Fatalf("container operation changed state: %+v %v called=%v", got, err, called)
			}
		})
	}
	afterJournal, err := os.ReadFile(filepath.Join(m.dataDir, stateFileName))
	if err != nil || string(afterJournal) != string(journal) {
		t.Fatal("blocked installation changed journal", err)
	}
	f.mu.Lock()
	afterRequests := 0
	for _, count := range f.requests {
		afterRequests += count
	}
	f.mu.Unlock()
	if requests != afterRequests {
		t.Fatal("blocked installation sent network requests")
	}
}

func TestContainerModePreservesRecoveredReadyStateAndDisablesApply(t *testing.T) {
	_, m := newFixture(t)
	checkReady(t, m)
	cfg := m.Config()
	cfg.InstallMode = "container"
	recovered, err := New(cfg, m.dataDir, m.build)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := recovered.Apply(context.Background())
	if !errors.Is(err, ErrContainerManaged) || snap.State != "ready" || snap.InstallEnabled || recovered.CanInstall() {
		t.Fatalf("recovered staging bypassed container mode: %+v %v", snap, err)
	}
}

func TestUpdateInstallModeAndDisabledCompatibility(t *testing.T) {
	for _, mode := range []string{"", "binary", "container"} {
		for _, enabled := range []bool{false, true} {
			m, err := New(config.UpdateConfig{Enabled: enabled, InstallMode: mode}, t.TempDir(), BuildInfo{Version: "1.4.2"})
			if err != nil || m.CanInstall() != (enabled && mode != "container") {
				t.Fatalf("mode=%q enabled=%v: %v", mode, enabled, err)
			}
			if !enabled {
				if _, err := m.Download(context.Background()); !errors.Is(err, ErrDisabled) {
					t.Fatalf("explicit disabled mode=%q: %v", mode, err)
				}
			}
		}
	}
	if _, err := New(config.UpdateConfig{Enabled: true, InstallMode: "invalid"}, t.TempDir(), BuildInfo{}); err == nil {
		t.Fatal("invalid install mode accepted")
	}
}
