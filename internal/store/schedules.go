package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"hash/fnv"
	"time"

	"relaylab/internal/domain"
)

func (s *Store) ListSchedules(ctx context.Context, modelID ...string) ([]domain.Schedule, error) {
	q := "SELECT data FROM schedules"
	var args []any
	if len(modelID) > 0 && modelID[0] != "" {
		q += " WHERE model_id=?"
		args = append(args, modelID[0])
	}
	return list[domain.Schedule](ctx, s.DB, q+" ORDER BY next_run_at,id", args...)
}
func (s *Store) GetSchedule(ctx context.Context, id string) (*domain.Schedule, error) {
	return load[domain.Schedule](ctx, s.DB, "SELECT data FROM schedules WHERE id=?", id)
}
func saveSchedule(ctx context.Context, q queryer, v domain.Schedule) error {
	b, e := data(v)
	if e != nil {
		return e
	}
	_, e = q.ExecContext(ctx, "INSERT INTO schedules(id,model_id,kind,template_id,interval_seconds,next_run_at,enabled,data) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET model_id=excluded.model_id,kind=excluded.kind,template_id=excluded.template_id,interval_seconds=excluded.interval_seconds,next_run_at=excluded.next_run_at,enabled=excluded.enabled,data=excluded.data", v.ID, v.ModelID, v.Kind, v.TemplateID, v.IntervalSeconds, ts(v.NextRunAt), v.Enabled, b)
	return e
}
func (s *Store) SaveSchedule(ctx context.Context, v domain.Schedule) error {
	v.ID = nonemptyID(v.ID)
	if v.IntervalSeconds < 1 {
		v.IntervalSeconds = 300
	}
	if v.NextRunAt.IsZero() {
		v.NextRunAt = nowUTC()
	}
	return s.transaction(ctx, func(tx *sql.Tx) error { return saveSchedule(ctx, tx, v) })
}
func stagger(id string, interval int) time.Duration {
	h := fnv.New64a()
	h.Write([]byte(id))
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], h.Sum64())
	seconds := int(binary.LittleEndian.Uint64(b[:]) % uint64(interval))
	return time.Duration(seconds) * time.Second
}
func (s *Store) ReconcileSchedules(ctx context.Context, m domain.Model) error {
	return s.transaction(ctx, func(tx *sql.Tx) error { return reconcileSchedules(ctx, tx, m) })
}

func reconcileSchedules(ctx context.Context, tx queryer, m domain.Model) error {
	old, e := list[domain.Schedule](ctx, tx, "SELECT data FROM schedules WHERE model_id=?", m.ID)
	if e != nil {
		return e
	}
	byKey := map[string]domain.Schedule{}
	for _, v := range old {
		byKey[v.Kind+":"+v.TemplateID] = v
	}
	cfg, e := load[domain.Settings](ctx, tx, "SELECT data FROM settings WHERE id=1")
	if e != nil {
		return e
	}
	pi := m.ProbeIntervalSeconds
	if pi <= 0 {
		pi = cfg.ProbeIntervalSeconds
	}
	if pi <= 0 {
		pi = 300
	}
	ai := m.ArtifactIntervalSeconds
	if ai <= 0 {
		ai = cfg.ArtifactIntervalSeconds
	}
	if ai <= 0 {
		ai = 1800
	}
	c, e := channel(ctx, tx, m.ChannelID)
	if e != nil {
		return e
	}
	if c.IntelligenceSchedule != nil && c.IntelligenceSchedule.IntervalSeconds > 0 {
		ai = c.IntelligenceSchedule.IntervalSeconds
	}
	p, e := load[domain.Platform](ctx, tx, "SELECT data FROM platforms WHERE id=?", c.PlatformID)
	if e != nil {
		return e
	}
	available := !m.Archived && !m.Missing && !c.Archived && c.Enabled && !p.Archived
	desired := []domain.Schedule{{ModelID: m.ID, Kind: "probe", IntervalSeconds: pi, Enabled: available && c.ScheduledProbeEnabled(m)}, {ModelID: m.ID, Kind: "artifact", TemplateID: "cn_html", IntervalSeconds: ai, Enabled: available && c.ScheduledArtifactEnabled(m, "cn_html")}, {ModelID: m.ID, Kind: "artifact", TemplateID: "en_svg", IntervalSeconds: ai, Enabled: available && c.ScheduledArtifactEnabled(m, "en_svg")}}
	n := nowUTC()
	for _, want := range desired {
		key := want.Kind + ":" + want.TemplateID
		if prior, ok := byKey[key]; ok {
			want.ID = prior.ID
			want.NextRunAt = prior.NextRunAt
			if want.Enabled && !prior.Enabled {
				if want.Kind == "artifact" {
					want.NextRunAt = n
				} else {
					want.NextRunAt = n.Add(stagger(want.ID, want.IntervalSeconds))
				}
			} else if want.Enabled && prior.IntervalSeconds != want.IntervalSeconds {
				want.NextRunAt = n.Add(time.Duration(want.IntervalSeconds) * time.Second)
			}
		} else {
			want.ID = domain.NewID()
			want.NextRunAt = n.Add(stagger(want.ID, want.IntervalSeconds))
			if want.Enabled && want.Kind == "artifact" {
				want.NextRunAt = n
			}
		}
		if e = saveSchedule(ctx, tx, want); e != nil {
			return e
		}
		if !want.Enabled {
			where := "schedule_id=?"
			if available && (c.ModelControls != nil || c.IntelligenceSchedule != nil) {
				where += " AND state IN ('queued','claimed')"
			}
			if e = cancelJobs(ctx, tx, where, want.ID); e != nil {
				return e
			}
		}
	}
	return nil
}

// TickSchedules coalesces missed ticks into one current slot and advances every
// considered schedule from now. It never replays a historical backlog.
func (s *Store) TickSchedules(ctx context.Context, now time.Time) (int, error) {
	count := 0
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		due, e := list[domain.Schedule](ctx, tx, "SELECT data FROM schedules WHERE enabled=1 AND next_run_at<=? ORDER BY next_run_at,id", ts(now))
		if e != nil {
			return e
		}
		for _, v := range due {
			m, e := load[domain.Model](ctx, tx, "SELECT data FROM models WHERE id=?", v.ModelID)
			if e != nil {
				return e
			}
			c, e := channel(ctx, tx, m.ChannelID)
			if e != nil {
				return e
			}
			p, e := load[domain.Platform](ctx, tx, "SELECT data FROM platforms WHERE id=?", c.PlatformID)
			if e != nil {
				return e
			}
			enabled := !m.Archived && !m.Missing && !c.Archived && c.Enabled && !p.Archived && ((v.Kind == "probe" && c.ScheduledProbeEnabled(*m)) || (v.Kind == "artifact" && c.ScheduledArtifactEnabled(*m, v.TemplateID)))
			if enabled {
				job := domain.Job{ID: domain.NewID(), ScheduleID: v.ID, ModelID: m.ID, ChannelID: c.ID, ChannelRevision: c.Revision, Kind: v.Kind, TemplateID: v.TemplateID, State: "queued", ScheduledAt: v.NextRunAt, CreatedAt: now}
				if e = enqueue(ctx, tx, &job, now); e == nil {
					count++
				} else if e != ErrConflict {
					return e
				} else if v.Kind == "artifact" {
					// Keep one due slot while manual or scheduled same-model work is
					// active. The next tick enqueues once the execution fence clears.
					continue
				}

			}
			if v.IntervalSeconds < 1 {
				v.IntervalSeconds = 300
			}
			v.NextRunAt = now.Add(time.Duration(v.IntervalSeconds) * time.Second)
			if !enabled {
				v.Enabled = false
			}
			if e = saveSchedule(ctx, tx, v); e != nil {
				return e
			}
		}
		return nil
	})
	return count, err
}
