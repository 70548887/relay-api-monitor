// Package runtimecontrol fences new work and reports managed process readiness.
package runtimecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const Protocol = 2

type Build struct {
	Version           string `json:"version"`
	Commit            string `json:"commit"`
	BuildTime         string `json:"build_time"`
	OS                string `json:"os"`
	Arch              string `json:"arch"`
	LauncherProtocol  int    `json:"launcher_protocol"`
	UsageGuard        int    `json:"usage_guard"`
	DatabaseSchema    int    `json:"database_schema"`
	MinDatabaseSchema int    `json:"min_database_schema"`
}

type Report struct {
	Build
	PID         int       `json:"pid"`
	InstanceID  string    `json:"instance_id"`
	Role        string    `json:"role"`
	Maintenance bool      `json:"maintenance"`
	Active      int       `json:"active"`
	Ready       bool      `json:"ready"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
}

// Gate serializes admission against maintenance detection. A report cannot
// acknowledge maintenance with zero active work while a pre-fence admission
// is still waiting to be counted.
type Gate struct {
	mu     sync.Mutex
	path   string
	forced bool
	active int
}

func (g *Gate) Configure(path string) { g.mu.Lock(); defer g.mu.Unlock(); g.path = path }
func (g *Gate) Set(paused bool)       { g.mu.Lock(); defer g.mu.Unlock(); g.forced = paused }
func Paused(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist)
}
func (g *Gate) Enter() (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.forced || Paused(g.path) {
		return nil, false
	}
	g.active++
	var once sync.Once
	return func() { once.Do(func() { g.mu.Lock(); g.active--; g.mu.Unlock() }) }, true
}
func (g *Gate) Snapshot() (bool, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.forced || Paused(g.path), g.active
}

func WriteJSON(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".runtime-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Reports are written only for managed children. A stale report never counts
// as readiness; the launcher checks PID, instance ID and heartbeat freshness.
func StartReporter(ctx context.Context, dataDir, role string, build Build, state func() (bool, int, bool)) func() {
	instance := os.Getenv("RELAYLAB_LAUNCHER_INSTANCE")
	if instance == "" {
		return func() {}
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	path := filepath.Join(dataDir, "runtime-"+role+".json")
	go func() {
		defer close(done)
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			paused, active, ready := state()
			_ = WriteJSON(path, Report{Build: build, PID: os.Getpid(), InstanceID: instance, Role: role, Maintenance: paused, Active: active, Ready: ready, HeartbeatAt: time.Now().UTC()})
			select {
			case <-runCtx.Done():
				_ = os.Remove(path)
				return
			case <-tick.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
