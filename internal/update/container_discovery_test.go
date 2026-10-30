package update

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"testing"
)

func TestContainerDiscoversReleasesBeyondBinaryInstallerCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*releaseFixture)
	}{
		{"launcher protocol", func(f *releaseFixture) { f.manifest.LauncherProtocol = LauncherProtocol + 1 }},
		{"schema minimum", func(f *releaseFixture) {
			f.manifest.DatabaseSchema = DatabaseSchema + 1
			f.manifest.MinDatabaseSchema = DatabaseSchema + 1
		}},
		{"future usage guard", func(f *releaseFixture) { f.manifest.UsageGuard = RequiredUsageGuard + 1 }},
		{"legacy usage guard", func(f *releaseFixture) { f.manifest.UsageGuard = 0 }},
		{"missing platform binary", func(f *releaseFixture) {
			otherOS := "linux"
			if runtime.GOOS == otherOS {
				otherOS = "windows"
			}
			f.manifest.Assets[0].OS = otherOS
			f.manifest.Assets[0].Arch = "amd64"
			name := platformAssetName(otherOS, "amd64")
			f.manifest.Assets[0].Name = name
			f.release.Assets[0].Name = name
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m := newFixture(t)
			m.cfg.InstallMode = "container"
			tc.mutate(f)
			snapshot, err := m.Check(context.Background())
			if err != nil || snapshot.State != "available" || snapshot.Latest == nil || !snapshot.Latest.Available || snapshot.Latest.Compatible || snapshot.Release == nil || snapshot.InstallEnabled {
				t.Fatalf("container discovery lost release: %+v %v", snapshot, err)
			}
			if snapshot.Latest.LatestVersion != f.release.TagName || snapshot.Latest.Commit != f.manifest.Commit || snapshot.Latest.ReleaseURL != f.release.HTMLURL {
				t.Fatal("release identity lost during discovery")
			}
			recovered, err := New(m.Config(), m.dataDir, m.build)
			if err != nil {
				t.Fatal(err)
			}
			after := recovered.Snapshot()
			if !reflect.DeepEqual(after.Latest, snapshot.Latest) || !reflect.DeepEqual(after.Release, snapshot.Release) || after.State != "available" {
				t.Fatalf("restart lost discovered release: %+v", after)
			}
			for _, operation := range []func(context.Context) (Snapshot, error){recovered.Download, recovered.Apply, recovered.Rollback} {
				if _, err := operation(context.Background()); !errors.Is(err, ErrContainerManaged) {
					t.Fatalf("discovery enabled binary installation: %v", err)
				}
			}
			f.mu.Lock()
			coreRequests := f.requests["/download/core"] + f.requests["/api/core"]
			f.mu.Unlock()
			if coreRequests != 0 {
				t.Fatal("container discovery downloaded an executable")
			}
		})
	}
}

func TestContainerDiscoveryStillRejectsMalformedManifestIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*releaseFixture)
	}{
		{"zero protocol", func(f *releaseFixture) { f.manifest.LauncherProtocol = 0 }},
		{"invalid schema range", func(f *releaseFixture) { f.manifest.MinDatabaseSchema = f.manifest.DatabaseSchema + 1 }},
		{"negative usage guard", func(f *releaseFixture) { f.manifest.UsageGuard = -1 }},
		{"missing commit", func(f *releaseFixture) { f.manifest.Commit = "" }},
		{"wrong version", func(f *releaseFixture) { f.manifest.Version = "1.2.3" }},
		{"missing checksum", func(f *releaseFixture) { f.manifest.Assets[0].SHA256 = "" }},
		{"size mismatch", func(f *releaseFixture) { f.manifest.Assets[0].Size++ }},
		{"unsafe asset URL", func(f *releaseFixture) { f.release.Assets[0].BrowserURL = "https://example.invalid/payload" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m := newFixture(t)
			m.cfg.InstallMode = "container"
			tc.mutate(f)
			snapshot, err := m.Check(context.Background())
			if err == nil || snapshot.State != "failed" || snapshot.Latest != nil {
				t.Fatalf("invalid release was advertised: %+v %v", snapshot, err)
			}
		})
	}
}
