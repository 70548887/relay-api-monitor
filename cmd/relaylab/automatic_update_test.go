package main

import (
	"context"
	"errors"
	"relaylab/internal/config"
	"relaylab/internal/update"
	"testing"
	"time"
)

type automaticFixture struct {
	state             update.Snapshot
	checks, downloads int
	err               error
	auto              bool
	mode              string
	reserved          bool
}

func (f *automaticFixture) TryReserveOperation() (func(), bool) {
	if f.reserved {
		return nil, false
	}
	f.reserved = true
	return func() { f.reserved = false }, true
}

func (f *automaticFixture) Check(context.Context) (update.Snapshot, error) {
	f.checks++
	return f.state, f.err
}
func (f *automaticFixture) Config() config.UpdateConfig {
	return config.UpdateConfig{Enabled: true, AutoDownload: f.auto, InstallMode: f.mode}
}
func (f *automaticFixture) Download(context.Context) (update.Snapshot, error) {
	f.downloads++
	f.state.State = "ready"
	return f.state, nil
}

func TestAutomaticContainerCheckNeverDownloads(t *testing.T) {
	f := &automaticFixture{auto: true, mode: "container", state: update.Snapshot{State: "available", Latest: &update.UpdateInfo{Available: true, Compatible: true}}}
	runAutomaticCheck(context.Background(), f)
	if f.checks != 1 || f.downloads != 0 {
		t.Fatalf("checks=%d downloads=%d", f.checks, f.downloads)
	}
}

func TestAutomaticCheckSkipsReservedManualOperation(t *testing.T) {
	f := &automaticFixture{auto: true, reserved: true}
	runAutomaticCheck(context.Background(), f)
	if f.checks != 0 || f.downloads != 0 || !f.reserved {
		t.Fatal("automatic check interfered with a reserved manual operation")
	}
	f.reserved = false
	f.err = errors.New("offline")
	runAutomaticCheck(context.Background(), f)
	if f.checks != 1 || f.reserved {
		t.Fatal("failed automatic check leaked its reservation")
	}
}

type scheduledUpdateFixture struct{ checks chan struct{} }

func (f *scheduledUpdateFixture) TryReserveOperation() (func(), bool) {
	return func() {}, true
}

func (f *scheduledUpdateFixture) Check(context.Context) (update.Snapshot, error) {
	f.checks <- struct{}{}
	return update.Snapshot{}, errors.New("offline")
}
func (f *scheduledUpdateFixture) Config() config.UpdateConfig {
	return config.UpdateConfig{Enabled: true}
}
func (f *scheduledUpdateFixture) Download(context.Context) (update.Snapshot, error) {
	panic("automatic check must not download")
}

func TestUpdateChecksRunAtStartupAndContinueAfterFailure(t *testing.T) {
	f := &scheduledUpdateFixture{checks: make(chan struct{}, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { runUpdateChecks(ctx, f, 40*time.Millisecond); close(done) }()
	for i := 0; i < 2; i++ {
		select {
		case <-f.checks:
		case <-time.After(2 * time.Second):
			t.Fatal("missing startup or periodic check")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("update checks did not stop")
	}
}

func TestUpdateChecksRespectManualOnlyAndCanceledContext(t *testing.T) {
	f := &scheduledUpdateFixture{checks: make(chan struct{}, 2)}
	runUpdateChecks(context.Background(), f, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runUpdateChecks(ctx, f, time.Hour)
	if len(f.checks) != 0 {
		t.Fatal("disabled or canceled schedule performed a network check")
	}
}

func TestAutomaticCheckOnlyDownloadsNewUnstagedCompatibleVersion(t *testing.T) {
	for _, tc := range []struct {
		name, state                              string
		latest, available, compatible, auto, err bool
		downloads                                int
	}{
		{"current version", "idle", true, false, true, true, false, 0},
		{"already downloaded", "ready", true, true, true, true, false, 0},
		{"new version", "available", true, true, true, true, false, 1},
		{"incompatible", "available", true, true, false, true, false, 0},
		{"auto disabled", "available", true, true, true, false, false, 0},
		{"no release", "idle", false, false, false, true, false, 0},
		{"check failed", "failed", true, true, true, true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &automaticFixture{auto: tc.auto, state: update.Snapshot{State: tc.state}}
			if tc.latest {
				f.state.Latest = &update.UpdateInfo{Available: tc.available, Compatible: tc.compatible}
			}
			if tc.err {
				f.err = errors.New("offline")
			}
			runAutomaticCheck(context.Background(), f)
			runAutomaticCheck(context.Background(), f)
			if f.checks != 2 || f.downloads != tc.downloads {
				t.Fatalf("checks=%d downloads=%d", f.checks, f.downloads)
			}
		})
	}
}
