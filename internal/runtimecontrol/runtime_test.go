package runtimecontrol

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFenceKeepsExistingAdmissionAndRejectsNewWork(t *testing.T) {
	var gate Gate
	path := filepath.Join(t.TempDir(), "maintenance.request")
	gate.Configure(path)
	leave, ok := gate.Enter()
	if !ok {
		t.Fatal("initial admission")
	}
	if err := os.WriteFile(path, []byte("operation"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := gate.Enter(); ok {
		t.Fatal("admitted while fenced")
	}
	if paused, active := gate.Snapshot(); !paused || active != 1 {
		t.Fatalf("paused=%v active=%d", paused, active)
	}
	leave()
	leave()
	if _, active := gate.Snapshot(); active != 0 {
		t.Fatal("release must be idempotent")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	leave, ok = gate.Enter()
	if !ok {
		t.Fatal("resume failed")
	}
	leave()
}

func TestUsageLockCoversInitializationAndCleanup(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireUsage(dir, "serve")
	if err != nil {
		t.Fatal(err)
	}
	if other, err := AcquireUsage(dir, "serve"); err == nil {
		_ = other()
		t.Fatal("second process lifetime admitted")
	}
	worker, err := AcquireUsage(dir, "worker")
	if err != nil {
		t.Fatal(err)
	}
	_ = worker()
	if err = release(); err != nil {
		t.Fatal(err)
	}
	if err = release(); err != nil {
		t.Fatal("release is not idempotent")
	}
	replacement, err := AcquireUsage(dir, "serve")
	if err != nil {
		t.Fatal(err)
	}
	_ = replacement()
}
