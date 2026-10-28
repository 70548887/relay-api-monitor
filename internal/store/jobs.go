package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"relaylab/internal/domain"
)

func terminal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "canceled" || state == "uncertain"
}
func job(ctx context.Context, q queryer, id string) (*domain.Job, error) {
	var b, token string
	if e := q.QueryRowContext(ctx, "SELECT data,lease_token FROM jobs WHERE id=?", id).Scan(&b, &token); e != nil {
		return nil, e
	}
	var v domain.Job
	if e := json.Unmarshal([]byte(b), &v); e != nil {
		return nil, e
	}
	v.LeaseToken = token
	return &v, nil
}
func jobs(ctx context.Context, q queryer, where string, args ...any) ([]domain.Job, error) {
	rows, e := q.QueryContext(ctx, "SELECT data,lease_token FROM jobs "+where, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]domain.Job, 0)
	for rows.Next() {
		var v domain.Job
		var b, t string
		if e = rows.Scan(&b, &t); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(b), &v); e != nil {
			return nil, e
		}
		v.LeaseToken = t
		out = append(out, v)
	}
	return out, rows.Err()
}
func saveJob(ctx context.Context, q queryer, v domain.Job, insert bool) error {
	b, e := data(v)
	if e != nil {
		return e
	}
	if insert {
		var sid any
		if v.ScheduleID != "" {
			sid = v.ScheduleID
		}
		_, e = q.ExecContext(ctx, "INSERT INTO jobs(id,schedule_id,model_id,channel_id,kind,state,owner,lease_token,lease_until,deadline,scheduled_at,created_at,finished_at,cancel_requested,data) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", v.ID, sid, v.ModelID, v.ChannelID, v.Kind, v.State, v.Owner, v.LeaseToken, ptrTS(v.LeaseUntil), ptrTS(v.Deadline), ts(v.ScheduledAt), ts(v.CreatedAt), ptrTS(v.FinishedAt), v.CancelRequested, b)
	} else {
		e = affected(q.ExecContext(ctx, "UPDATE jobs SET state=?,owner=?,lease_token=?,lease_until=?,deadline=?,finished_at=?,cancel_requested=?,data=? WHERE id=?", v.State, v.Owner, v.LeaseToken, ptrTS(v.LeaseUntil), ptrTS(v.Deadline), ptrTS(v.FinishedAt), v.CancelRequested, b, v.ID))
	}
	return e
}
func busy(ctx context.Context, q queryer, modelID, kind string, now time.Time) (bool, error) {
	var n int
	e := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM jobs WHERE model_id=? AND kind=? AND (state IN ('queued','claimed','dispatching') OR (state='uncertain' AND deadline>?))", modelID, kind, ts(now.Add(-30*time.Second))).Scan(&n)
	return n > 0, e
}
func enqueue(ctx context.Context, q queryer, v *domain.Job, now time.Time) error {
	if v.Kind != "probe" && v.Kind != "artifact" {
		return fmt.Errorf("store: invalid job kind %q", v.Kind)
	}
	m, err := load[domain.Model](ctx, q, "SELECT data FROM models WHERE id=?", v.ModelID)
	if err != nil {
		return err
	}
	if v.ChannelID != "" && v.ChannelID != m.ChannelID {
		return ErrConfigurationChanged
	}
	v.ChannelID = m.ChannelID
	c, err := channel(ctx, q, m.ChannelID)
	if err != nil {
		return err
	}
	p, err := load[domain.Platform](ctx, q, "SELECT data FROM platforms WHERE id=?", c.PlatformID)
	if err != nil {
		return err
	}
	if m.Archived || m.Missing || c.Archived || !c.Enabled || p.Archived {
		return ErrConfigurationChanged
	}
	if v.ChannelRevision != 0 && v.ChannelRevision != c.Revision {
		return ErrConfigurationChanged
	}
	isBusy, e := busy(ctx, q, v.ModelID, v.Kind, now)
	if e != nil {
		return e
	}
	if isBusy {
		return ErrConflict
	}
	v.ID = nonemptyID(v.ID)
	v.State = "queued"
	v.Owner = ""
	v.LeaseToken = ""
	v.LeaseUntil = nil
	v.Deadline = nil
	v.StartedAt = nil
	v.FinishedAt = nil
	v.CancelRequested = false
	v.ProgressChars = 0
	v.ProgressAt = nil
	v.ReturnedModel = ""
	v.CreatedAt = created(v.CreatedAt)
	if v.ScheduledAt.IsZero() {
		v.ScheduledAt = v.CreatedAt
	}
	if v.ChannelRevision == 0 {
		v.ChannelRevision = c.Revision
	}
	e = saveJob(ctx, q, *v, true)
	if e != nil && strings.Contains(e.Error(), "UNIQUE constraint failed") {
		return ErrConflict
	}
	return e
}
func (s *Store) EnqueueJob(ctx context.Context, v domain.Job) (domain.Job, error) {
	e := s.transaction(ctx, func(tx *sql.Tx) error { return enqueue(ctx, tx, &v, nowUTC()) })
	return v, e
}
func (s *Store) GetJob(ctx context.Context, id string) (*domain.Job, error) {
	return job(ctx, s.DB, id)
}
func (s *Store) ListJobs(ctx context.Context, limit ...int) ([]domain.Job, error) {
	n := 200
	if len(limit) > 0 && limit[0] > 0 {
		n = limit[0]
	}
	return jobs(ctx, s.DB, "ORDER BY created_at DESC,id DESC LIMIT ?", n)
}
func (s *Store) ClaimJob(ctx context.Context, kind, owner string, now time.Time) (*domain.Job, error) {
	return s.ClaimJobForChannels(ctx, kind, owner, now, nil)
}

// ClaimJobForChannels applies admission before claiming, so saturated channels
// cannot consume the execution slots of unrelated channels. Nil means all.
func (s *Store) ClaimJobForChannels(ctx context.Context, kind, owner string, now time.Time, channelIDs []string) (*domain.Job, error) {
	if owner == "" {
		return nil, errors.New("store: job owner is required")
	}
	var claimed *domain.Job
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := checkJobWorker(ctx, tx, owner); err != nil {
			return err
		}
		where := "WHERE state='queued' AND kind=? AND cancel_requested=0"
		args := []any{kind}
		if channelIDs != nil {
			if len(channelIDs) == 0 {
				return nil
			}
			where += " AND channel_id IN (" + strings.TrimRight(strings.Repeat("?,", len(channelIDs)), ",") + ")"
			for _, id := range channelIDs {
				args = append(args, id)
			}
		}
		selected, e := jobs(ctx, tx, where+" ORDER BY created_at,id LIMIT 1", args...)
		if e != nil {
			return e
		}
		if len(selected) == 0 {
			return nil
		}
		v := selected[0]
		v.State = "claimed"
		v.Owner = owner
		v.LeaseToken = domain.NewID()
		until := now.Add(WorkerLeaseDuration)
		v.LeaseUntil = &until
		v.StartedAt = &now
		if e = saveJob(ctx, tx, v, false); e != nil {
			return e
		}
		claimed = &v
		return nil
	})
	return claimed, e
}
func (s *Store) MarkDispatching(ctx context.Context, id, token string, deadline time.Time, snapshot string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		v, e := job(ctx, tx, id)
		if e != nil {
			return e
		}
		n := nowUTC()
		if v.State != "claimed" || v.LeaseToken != token || token == "" || v.CancelRequested || v.LeaseUntil == nil || !v.LeaseUntil.After(n) || !deadline.After(n) {
			return ErrConflict
		}
		if e = checkJobWorker(ctx, tx, v.Owner); e != nil {
			return e
		}
		m, e := load[domain.Model](ctx, tx, "SELECT data FROM models WHERE id=?", v.ModelID)
		if e != nil {
			return e
		}
		c, e := channel(ctx, tx, v.ChannelID)
		if e != nil {
			return e
		}
		p, e := load[domain.Platform](ctx, tx, "SELECT data FROM platforms WHERE id=?", c.PlatformID)
		if e != nil {
			return e
		}
		if m.ChannelID != c.ID || c.Revision != v.ChannelRevision || m.Archived || m.Missing || c.Archived || !c.Enabled || p.Archived {
			return ErrConfigurationChanged
		}
		if v.ScheduleID != "" {
			plan, err := load[domain.Schedule](ctx, tx, "SELECT data FROM schedules WHERE id=?", v.ScheduleID)
			if err != nil {
				return err
			}
			if !plan.Enabled || plan.ModelID != m.ID || plan.Kind != v.Kind || plan.TemplateID != v.TemplateID || !c.Enabled || m.Missing {
				return ErrConfigurationChanged
			}
			if v.Kind == "artifact" && !c.ScheduledArtifactEnabled(*m, v.TemplateID) {
				return ErrConfigurationChanged
			}
			if v.Kind == "probe" && !c.ScheduledProbeEnabled(*m) {
				return ErrConfigurationChanged
			}
		}
		v.State = "dispatching"
		v.Deadline = &deadline
		v.Snapshot = snapshot
		return saveJob(ctx, tx, *v, false)
	})
}
func (s *Store) RenewJob(ctx context.Context, id, token string, until time.Time) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		v, e := job(ctx, tx, id)
		if e != nil {
			return e
		}
		if (v.State != "claimed" && v.State != "dispatching") || token == "" || v.LeaseToken != token || v.LeaseUntil == nil || !v.LeaseUntil.After(nowUTC()) {
			return ErrConflict
		}
		if !until.After(nowUTC()) {
			return ErrConflict
		}
		if e = checkJobWorker(ctx, tx, v.Owner); e != nil {
			return e
		}
		v.LeaseUntil = &until
		return saveJob(ctx, tx, *v, false)
	})
}

// UpdateJobProgress stores only non-secret stream metadata. Both the task lease
// and the global worker lease fence writers after a worker takeover.
func (s *Store) UpdateJobProgress(ctx context.Context, id, token string, chars int64, returnedModel string) error {
	if chars < 0 {
		return errors.New("store: progress character count must not be negative")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		v, err := job(ctx, tx, id)
		if err != nil {
			return err
		}
		n := nowUTC()
		if v.State != "dispatching" || token == "" || v.LeaseToken != token || v.LeaseUntil == nil || !v.LeaseUntil.After(n) || v.CancelRequested {
			return ErrConflict
		}
		if err = checkJobWorker(ctx, tx, v.Owner); err != nil {
			return err
		}
		if chars < v.ProgressChars {
			return nil
		}
		if chars == v.ProgressChars && (returnedModel == "" || returnedModel == v.ReturnedModel) {
			return nil
		}
		v.ProgressChars = chars
		v.ProgressAt = &n
		if returnedModel != "" {
			v.ReturnedModel = returnedModel
		}
		return saveJob(ctx, tx, *v, false)
	})
}
func (s *Store) FinishJob(ctx context.Context, v domain.Job, probe *domain.ProbeResult, artifact *domain.Artifact) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		current, e := job(ctx, tx, v.ID)
		if e != nil {
			return e
		}
		n := nowUTC()
		if current.State != "claimed" && current.State != "dispatching" {
			return ErrConflict
		}
		if current.Owner != v.Owner || current.LeaseToken == "" || current.LeaseToken != v.LeaseToken || current.LeaseUntil == nil || !current.LeaseUntil.After(n) {
			return ErrConflict
		}
		if e = checkJobWorker(ctx, tx, current.Owner); e != nil {
			return e
		}
		if !terminal(v.State) || v.State == "uncertain" {
			return errors.New("store: invalid completion state")
		}
		if current.CancelRequested {
			v.State = "canceled"
			probe = nil
			artifact = nil
		}
		if v.State == "canceled" {
			probe = nil
			artifact = nil
		}
		if probe != nil {
			if v.Kind != "probe" || probe.ModelID != "" && probe.ModelID != current.ModelID || probe.JobID != "" && probe.JobID != v.ID {
				return errors.New("store: probe completion does not match job")
			}
			probe.ID = nonemptyID(probe.ID)
			probe.JobID = current.ID
			probe.ModelID = current.ModelID
			probe.CreatedAt = created(probe.CreatedAt)
			b, e := data(probe)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO probe_results(id,job_id,model_id,created_at,data) VALUES(?,?,?,?,?)", probe.ID, probe.JobID, probe.ModelID, ts(probe.CreatedAt), b); e != nil {
				return e
			}
		}
		if artifact != nil {
			if v.Kind != "artifact" || v.State != "succeeded" || artifact.ModelID != "" && artifact.ModelID != current.ModelID || artifact.JobID != "" && artifact.JobID != v.ID {
				return errors.New("store: artifact completion does not match job")
			}
			artifact.ID = nonemptyID(artifact.ID)
			artifact.JobID = current.ID
			artifact.ModelID = current.ModelID
			artifact.ChannelID = current.ChannelID
			artifact.TemplateID = current.TemplateID
			artifact.CreatedAt = created(artifact.CreatedAt)
			b, e := data(artifact)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO artifacts(id,job_id,model_id,template_id,created_at,hidden,data) VALUES(?,?,?,?,?,?,?)", artifact.ID, artifact.JobID, artifact.ModelID, artifact.TemplateID, ts(artifact.CreatedAt), artifact.Hidden, b); e != nil {
				return e
			}
			if e = trimArtifacts(ctx, tx, artifact.ModelID, artifact.TemplateID); e != nil {
				return e
			}
		}
		current.State = v.State
		current.ErrorCode = v.ErrorCode
		current.ErrorMessage = v.ErrorMessage
		current.ReceivedBytes, current.TextBytes = v.ReceivedBytes, v.TextBytes
		current.FirstTextMS, current.DurationMS = v.FirstTextMS, v.DurationMS
		current.HTTPStatus, current.FinishReason, current.Usage = v.HTTPStatus, v.FinishReason, v.Usage
		if v.ProgressChars > current.ProgressChars {
			current.ProgressChars = v.ProgressChars
			current.ProgressAt = v.ProgressAt
			if v.ReturnedModel != "" {
				current.ReturnedModel = v.ReturnedModel
			}
			if current.ProgressAt == nil {
				current.ProgressAt = &n
			}
		}
		if current.ReturnedModel == "" && v.ReturnedModel != "" {
			current.ReturnedModel = v.ReturnedModel
		}
		current.FinishedAt = &n
		current.LeaseUntil = nil
		current.CancelRequested = current.CancelRequested || v.CancelRequested
		return saveJob(ctx, tx, *current, false)
	})
}
func (s *Store) RequestCancel(ctx context.Context, id string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		v, e := job(ctx, tx, id)
		if e != nil {
			return e
		}
		if terminal(v.State) {
			return ErrConflict
		}
		v.CancelRequested = true
		if v.State == "queued" {
			n := nowUTC()
			v.State = "canceled"
			v.FinishedAt = &n
		}
		return saveJob(ctx, tx, *v, false)
	})
}

func cancelJobs(ctx context.Context, q queryer, where string, args ...any) error {
	active, err := jobs(ctx, q, "WHERE state IN ('queued','claimed','dispatching') AND "+where, args...)
	if err != nil {
		return err
	}
	n := nowUTC()
	for _, j := range active {
		j.CancelRequested = true
		if j.State == "queued" {
			j.State = "canceled"
			j.FinishedAt = &n
			j.ErrorCode = "canceled"
			j.ErrorMessage = "任务已取消"
		}
		if err = saveJob(ctx, q, j, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CancelJobsForChannel(ctx context.Context, channelID string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error { return cancelJobs(ctx, tx, "channel_id=?", channelID) })
}

func (s *Store) CancelJobsForModel(ctx context.Context, modelID string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error { return cancelJobs(ctx, tx, "model_id=?", modelID) })
}

func (s *Store) JobStateCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT state,COUNT(*) FROM jobs GROUP BY state")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err = rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		out[state] = n
	}
	return out, rows.Err()
}

func (s *Store) LatestArtifactAttempts(ctx context.Context, modelID, templateID string) ([]domain.Job, error) {
	return s.latestArtifactAttempts(ctx, "model_id", modelID, templateID, false)
}

func (s *Store) LatestChannelArtifactAttempts(ctx context.Context, channelID, templateID string) ([]domain.Job, error) {
	return s.latestArtifactAttempts(ctx, "channel_id", channelID, templateID, false)
}

func (s *Store) LatestPublicChannelArtifactAttempts(ctx context.Context, channelID, templateID string) ([]domain.Job, error) {
	return s.latestArtifactAttempts(ctx, "channel_id", channelID, templateID, true)
}

func (s *Store) latestArtifactAttempts(ctx context.Context, partition, id, templateID string, public bool) ([]domain.Job, error) {
	where := "jobs.kind='artifact'"
	var args []any
	if id != "" {
		where += " AND jobs." + partition + "=?"
		args = append(args, id)
	}
	if templateID != "" {
		where += " AND json_extract(jobs.data,'$.template_id')=?"
		args = append(args, templateID)
	}
	from := "jobs"
	if public {
		from += " JOIN models ON models.id=jobs.model_id JOIN channels ON channels.id=models.channel_id JOIN platforms ON platforms.id=channels.platform_id"
		where += " AND " + publicCatalogCondition
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT data,lease_token FROM (SELECT jobs.data,jobs.lease_token,row_number() OVER(PARTITION BY jobs."+partition+",json_extract(jobs.data,'$.template_id') ORDER BY jobs.created_at DESC,jobs.id DESC) AS n FROM "+from+" WHERE "+where+") WHERE n=1", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Job, 0)
	for rows.Next() {
		var j domain.Job
		var raw string
		if err = rows.Scan(&raw, &j.LeaseToken); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &j); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Store) RecoverJobs(ctx context.Context, now time.Time) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		stale, e := jobs(ctx, tx, "WHERE (state='claimed' AND lease_until<=?) OR (state='dispatching' AND (lease_until<=? OR deadline<=?))", ts(now), ts(now), ts(now))
		if e != nil {
			return e
		}
		for _, v := range stale {
			if v.State == "claimed" {
				v.State = "queued"
				v.Owner = ""
				v.LeaseToken = ""
				v.LeaseUntil = nil
				v.StartedAt = nil
				if v.CancelRequested {
					v.State = "canceled"
					v.FinishedAt = &now
				}
			} else {
				v.State = "uncertain"
				v.ErrorCode = "worker_lost"
				v.ErrorMessage = "Worker lease or request deadline expired after dispatch; request is not retried."
				v.FinishedAt = &now
				v.LeaseUntil = nil
			}
			if e = saveJob(ctx, tx, v, false); e != nil {
				return e
			}
		}
		return nil
	})
}
func (s *Store) AcquireWorkerLease(ctx context.Context, owner string, now time.Time) (bool, error) {
	if owner == "" {
		return false, errors.New("store: worker owner required")
	}
	acquired := false
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		r, e := tx.ExecContext(ctx, "INSERT INTO worker_lease(id,owner,heartbeat_at,lease_until) VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET owner=excluded.owner,heartbeat_at=excluded.heartbeat_at,lease_until=excluded.lease_until WHERE worker_lease.lease_until<=excluded.heartbeat_at OR worker_lease.owner=excluded.owner", owner, ts(now), ts(now.Add(WorkerLeaseDuration)))
		if e != nil {
			return e
		}
		n, e := r.RowsAffected()
		acquired = n == 1
		return e
	})
	return acquired, e
}
func (s *Store) RenewWorkerLease(ctx context.Context, owner string, now time.Time) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		return affected(tx.ExecContext(ctx, "UPDATE worker_lease SET heartbeat_at=?,lease_until=? WHERE id=1 AND owner=? AND lease_until>?", ts(now), ts(now.Add(WorkerLeaseDuration)), owner, ts(now)))
	})
}
func (s *Store) WorkerHealth(ctx context.Context) (domain.WorkerHealth, error) {
	var v domain.WorkerHealth
	var heartbeat, until string
	e := s.DB.QueryRowContext(ctx, "SELECT owner,heartbeat_at,lease_until FROM worker_lease WHERE id=1").Scan(&v.Owner, &heartbeat, &until)
	if errors.Is(e, sql.ErrNoRows) {
		return v, nil
	}
	if e != nil {
		return v, e
	}
	if v.HeartbeatAt, e = time.Parse(timeFormat, heartbeat); e != nil {
		return v, e
	}
	if v.LeaseUntil, e = time.Parse(timeFormat, until); e != nil {
		return v, e
	}
	v.Online = v.LeaseUntil.After(nowUTC())
	return v, nil
}

// WorkOne is usable without a process lease. Once a process lease exists, all
// claims, dispatches, renewals and results must belong to its current owner.
func checkJobWorker(ctx context.Context, q queryer, owner string) error {
	var current, until string
	err := q.QueryRowContext(ctx, "SELECT owner,lease_until FROM worker_lease WHERE id=1").Scan(&current, &until)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if current != owner || until <= ts(nowUTC()) {
		return ErrConflict
	}
	return nil
}

func (s *Store) CheckWorkerLease(ctx context.Context, owner string) error {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM worker_lease WHERE id=1 AND owner=? AND lease_until>?", owner, ts(nowUTC())).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

// ListActiveJobs returns only live execution fences, not historical payloads.
func (s *Store) ListActiveJobs(ctx context.Context) ([]domain.Job, error) {
	return jobs(ctx, s.DB, "WHERE state IN ('queued','claimed','dispatching') OR (state='uncertain' AND deadline>?) ORDER BY created_at,id", ts(nowUTC().Add(-30*time.Second)))
}
