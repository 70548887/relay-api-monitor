package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveReadyFailurePreventsFinalPublication(t *testing.T) {
	f := newArchiveFixture(t)
	out := filepath.Join(t.TempDir(), "backup.zip.age")
	rejected := errors.New("persist failed")
	called := 0
	e := CreateFromStore(context.Background(), f.store, f.dir, out, Options{Recipient: f.recipient, OnReady: func(member Member) error {
		called++
		if member.Size <= 0 || len(member.SHA256) != 64 {
			t.Fatal("invalid ready digest")
		}
		if _, e := os.Stat(out); !os.IsNotExist(e) {
			t.Fatal("archive published before ready callback")
		}
		return rejected
	}})
	if !errors.Is(e, rejected) || called != 1 {
		t.Fatal(e, called)
	}
	if _, e = os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("failed ready callback published archive")
	}
	partials, e := filepath.Glob(filepath.Join(filepath.Dir(out), ".backup-*.partial"))
	if e != nil || len(partials) != 0 {
		t.Fatal("failed write left partial file", partials, e)
	}
}
