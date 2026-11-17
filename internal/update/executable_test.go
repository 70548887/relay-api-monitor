package update

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// executableReleaseFixture serves a real native program through the same
// release fixture used by Check and Download. The applier must execute the
// downloaded file in place; copying it would hide a missing executable bit.
func executableReleaseFixture(t *testing.T) (*releaseFixture, *Manager) {
	t.Helper()
	f, m := newFixture(t)
	metadata, err := json.Marshal(map[string]any{
		"version":             f.manifest.Version,
		"commit":              f.manifest.Commit,
		"build_time":          f.manifest.BuildTime,
		"os":                  runtime.GOOS,
		"arch":                runtime.GOARCH,
		"launcher_protocol":   f.manifest.LauncherProtocol,
		"usage_guard":         f.manifest.UsageGuard,
		"database_schema":     f.manifest.DatabaseSchema,
		"min_database_schema": f.manifest.MinDatabaseSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "candidate.go")
	program := "package main\nimport (\"fmt\"; \"os\")\nfunc main() { if len(os.Args) != 3 || os.Args[1] != \"version\" || os.Args[2] != \"--json\" { os.Exit(2) }; fmt.Println(" + strconv.Quote(string(metadata)) + ") }\n"
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "candidate")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build native candidate: %v\n%s", err, output)
	}
	f.payload, err = os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(f.payload)
	f.manifest.Assets[0].Size = int64(len(f.payload))
	f.manifest.Assets[0].SHA256 = fmt.Sprintf("%x", hash)
	f.release.Assets[0].Size = int64(len(f.payload))
	return f, m
}

func executableApplier(t *testing.T, expectedPath string, calls *int) func(context.Context, string, UpdateInfo) error {
	t.Helper()
	return func(ctx context.Context, path string, info UpdateInfo) error {
		*calls = *calls + 1
		if path != expectedPath {
			return fmt.Errorf("applier received %q; downloaded path was %q", path, expectedPath)
		}
		output, err := exec.CommandContext(ctx, path, "version", "--json").CombinedOutput()
		if err != nil {
			return fmt.Errorf("execute downloaded candidate: %w: %s", err, output)
		}
		var got struct {
			Version           string `json:"version"`
			Commit            string `json:"commit"`
			LauncherProtocol  int    `json:"launcher_protocol"`
			UsageGuard        int    `json:"usage_guard"`
			DatabaseSchema    int    `json:"database_schema"`
			MinDatabaseSchema int    `json:"min_database_schema"`
		}
		if err := json.Unmarshal(output, &got); err != nil {
			return fmt.Errorf("decode candidate metadata: %w", err)
		}
		if got.Version != strings.TrimPrefix(info.LatestVersion, "v") || got.Commit != info.Commit || got.LauncherProtocol != info.LauncherProtocol || got.UsageGuard != info.UsageGuard || got.DatabaseSchema != info.DatabaseSchema || got.MinDatabaseSchema != info.MinDatabaseSchema {
			return fmt.Errorf("downloaded candidate metadata does not match release: %+v", got)
		}
		return nil
	}
}

func requirePrivateExecutable(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := stat.Mode().Perm(); got != 0700 {
		t.Fatalf("downloaded candidate permissions = %04o, want 0700", got)
	}
}

func TestDownloadedCandidateExecutesWithoutCopy(t *testing.T) {
	_, m := executableReleaseFixture(t)
	checkReady(t, m) // Check -> Download, including release and SHA-256 validation.
	path := m.state.StagedPath
	requirePrivateExecutable(t, path)
	calls := 0
	m.SetApplier(executableApplier(t, path, &calls))
	snapshot, err := m.Apply(context.Background())
	if err != nil || snapshot.State != "validating" || calls != 1 {
		t.Fatalf("downloaded executable was not applied: state=%s calls=%d err=%v", snapshot.State, calls, err)
	}
}

func TestRecoveredDownloadedCandidateRepairsPrivateExecutableMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable permission recovery")
	}
	f, m := executableReleaseFixture(t)
	checkReady(t, m)
	path := m.state.StagedPath
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(m.cfg, m.dataDir, m.build)
	if err != nil {
		t.Fatal(err)
	}
	recovered.apiBase = f.server.URL
	if state := recovered.Snapshot().State; state != "ready" {
		t.Fatalf("previously verified download was not recovered: %s", state)
	}
	calls := 0
	recovered.SetApplier(executableApplier(t, path, &calls))
	snapshot, err := recovered.Apply(context.Background())
	if err != nil || snapshot.State != "validating" || calls != 1 {
		t.Fatalf("cached candidate was not repaired and applied: state=%s calls=%d err=%v", snapshot.State, calls, err)
	}
	requirePrivateExecutable(t, path)
}
