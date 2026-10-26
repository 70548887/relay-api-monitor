package main

import (
	"context"
	"errors"
	"relaylab/internal/config"
	"relaylab/internal/update"
	"testing"
)

type automaticFixture struct {
	state             update.Snapshot
	checks, downloads int
	err               error
	auto              bool
}

func (f *automaticFixture) Check(context.Context) (update.Snapshot, error) {
	f.checks++
	return f.state, f.err
}
func (f *automaticFixture) Config() config.UpdateConfig {
	return config.UpdateConfig{Enabled: true, AutoDownload: f.auto}
}
func (f *automaticFixture) Download(context.Context) (update.Snapshot, error) {
	f.downloads++
	f.state.State = "ready"
	return f.state, nil
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
