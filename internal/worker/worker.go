// Package worker executes durable model checks independently from HTTP serving.
package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/store"
	"relaylab/internal/upstream"
)

var ErrWorkerActive = errors.New("worker: another worker already owns this database")
var ErrLeaseLost = errors.New("worker: execution lease lost")

type activeJob struct {
	kind, channelID string
	cancel          context.CancelFunc
}
type Worker struct {
	st                  *store.Store
	vault               *security.Vault
	client              *upstream.Client
	owner               string
	running             atomic.Bool
	mu                  sync.Mutex
	active              map[string]activeJob
	maintenanceChannels map[string]int
	fatal               chan error
	wg                  sync.WaitGroup
	syncBusy            bool
	nextSync            map[string]time.Time
	syncRevision        map[string]int
	nextCleanup         time.Time
	nextMaintenance     time.Time
}

func New(st *store.Store, vault *security.Vault, client *upstream.Client) *Worker {
	if client == nil {
		client = upstream.New()
	}
	return &Worker{st: st, vault: vault, client: client, owner: domain.NewID(), active: map[string]activeJob{}, maintenanceChannels: map[string]int{}, fatal: make(chan error, 1), nextSync: map[string]time.Time{}, syncRevision: map[string]int{}}
}
func bounded(value, def, max int) int {
	if value <= 0 {
		return def
	}
	if value > max {
		return max
	}
	return value
}
func normalized(s domain.Settings) domain.Settings {
	s.ProbeConcurrency = bounded(s.ProbeConcurrency, 4, 128)
	s.ArtifactConcurrency = bounded(s.ArtifactConcurrency, 2, 64)
	s.ChannelConcurrency = bounded(s.ChannelConcurrency, 2, 128)
	return s
}

func (w *Worker) Run(ctx context.Context) error {
	if w.st == nil || w.vault == nil {
		return errors.New("worker: store and vault are required")
	}
	if !w.running.CompareAndSwap(false, true) {
		return ErrWorkerActive
	}
	defer w.running.Store(false)
	acquired, err := w.st.AcquireWorkerLease(ctx, w.owner, time.Now().UTC())
	if err != nil {
		return err
	}
	if !acquired {
		return ErrWorkerActive
	}
	for {
		select {
		case <-w.fatal:
			continue
		default:
		}
		break
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		w.mu.Lock()
		for _, v := range w.active {
			v.cancel()
		}
		w.mu.Unlock()
		w.wg.Wait()
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		_ = w.st.ReleaseWorkerLease(releaseCtx, w.owner)
	}()
	if err = w.tick(runCtx, time.Now().UTC()); err != nil {
		return err
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-runCtx.Done():
			return ctx.Err()
		case err := <-w.fatal:
			cancel()
			return err
		case now := <-heartbeat.C:
			if err = w.st.RenewWorkerLease(runCtx, w.owner, now.UTC()); err != nil {
				cancel()
				return fmt.Errorf("%w: %v", ErrLeaseLost, err)
			}
		case now := <-tick.C:
			if err = w.tick(runCtx, now.UTC()); err != nil {
				cancel()
				return err
			}
		}
	}
}

func (w *Worker) tick(ctx context.Context, now time.Time) error {
	if w.running.Load() {
		if err := w.st.CheckWorkerLease(ctx, w.owner); err != nil {
			return fmt.Errorf("%w: %v", ErrLeaseLost, err)
		}
	}
	cfg, err := w.st.GetSettings(ctx)
	if err != nil {
		return err
	}
	cfg = normalized(cfg)
	if err = w.st.RecoverJobs(ctx, now); err != nil {
		return err
	}
	if _, err = w.st.TickSchedules(ctx, now); err != nil {
		return err
	}
	for _, kind := range []string{"probe", "artifact"} {
		if err = w.admit(ctx, kind, cfg); err != nil {
			return err
		}
	}
	w.startMaintenance(ctx, now)
	return nil
}

func (w *Worker) admit(ctx context.Context, kind string, cfg domain.Settings) error {
	channels, err := w.st.ListChannels(ctx)
	if err != nil {
		return err
	}
	for {
		w.mu.Lock()
		kindActive := 0
		channelActive := map[string]int{}
		for id, n := range w.maintenanceChannels {
			channelActive[id] = n
		}
		for _, a := range w.active {
			if a.kind == kind {
				kindActive++
			}
			channelActive[a.channelID]++
		}
		limit := cfg.ProbeConcurrency
		if kind == "artifact" {
			limit = cfg.ArtifactConcurrency
		}
		if kindActive >= limit {
			w.mu.Unlock()
			return nil
		}
		allowed := make([]string, 0, len(channels))
		for _, c := range channels {
			if channelActive[c.ID] < cfg.ChannelConcurrency {
				allowed = append(allowed, c.ID)
			}
		}
		if len(allowed) == 0 {
			w.mu.Unlock()
			return nil
		}
		j, err := w.st.ClaimJobForChannels(ctx, kind, w.owner, time.Now().UTC(), allowed)
		if err != nil {
			w.mu.Unlock()
			return err
		}
		if j == nil {
			w.mu.Unlock()
			return nil
		}
		child, cancel := context.WithCancel(ctx)
		w.active[j.ID] = activeJob{kind: kind, channelID: j.ChannelID, cancel: cancel}
		w.mu.Unlock()
		w.wg.Add(1)
		go func(j domain.Job) {
			defer w.wg.Done()
			defer cancel()
			defer func() { w.mu.Lock(); delete(w.active, j.ID); w.mu.Unlock() }()
			if err := w.execute(child, j); errors.Is(err, ErrLeaseLost) {
				select {
				case w.fatal <- err:
				default:
				}
			}
		}(*j)
	}
}

func (w *Worker) WorkOne(ctx context.Context, kind string) (bool, error) {
	if w.st == nil || w.vault == nil {
		return false, errors.New("worker: store and vault are required")
	}
	j, err := w.st.ClaimJob(ctx, kind, w.owner, time.Now().UTC())
	if err != nil || j == nil {
		return false, err
	}
	return true, w.execute(ctx, *j)
}

// watchJob keeps leases alive even during a long response body and propagates
// explicit cancellation to the HTTP context. It never restarts a request.
func (w *Worker) watchJob(ctx context.Context, j domain.Job, cancel context.CancelFunc, lost *atomic.Bool, done chan<- struct{}) {
	defer close(done)
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	renew := time.NewTicker(10 * time.Second)
	defer renew.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			if w.running.Load() {
				if err := w.st.CheckWorkerLease(ctx, w.owner); err != nil {
					lost.Store(true)
					cancel()
					return
				}
			}
			current, err := w.st.GetJob(ctx, j.ID)
			if err != nil || current.LeaseToken != j.LeaseToken || current.Owner != j.Owner || current.State != "claimed" && current.State != "dispatching" {
				lost.Store(true)
				cancel()
				return
			}
			if current.CancelRequested {
				cancel()
				return
			}
		case now := <-renew.C:
			if err := w.st.RenewJob(ctx, j.ID, j.LeaseToken, now.UTC().Add(store.WorkerLeaseDuration)); err != nil {
				lost.Store(true)
				cancel()
				return
			}
		}
	}
}
