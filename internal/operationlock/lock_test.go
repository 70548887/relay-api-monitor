package operationlock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLockExcludesAnotherProcessAndReleases(t *testing.T) {
	if dataDir := os.Getenv("RELAYLAB_OPERATIONLOCK_TEST_DIR"); dataDir != "" {
		release, err := Acquire(dataDir)
		if !errors.Is(err, ErrBusy) {
			if release != nil {
				_ = release()
			}
			t.Fatalf("expected busy got %v", err)
		}
		return
	}
	dir := filepath.Join(t.TempDir(), "data")
	release, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestLockExcludesAnotherProcessAndReleases$")
	cmd.Env = append(os.Environ(), "RELAYLAB_OPERATIONLOCK_TEST_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, out)
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	releaseAgain, err := Acquire(dir)
	if err != nil {
		t.Fatal("lock not released", err)
	}
	_ = releaseAgain()
	if err = release(); err != nil {
		t.Fatal("release not idempotent", err)
	}
}
