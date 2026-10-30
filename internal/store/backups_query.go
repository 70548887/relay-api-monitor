package store

import (
	"context"
	"errors"
	"relaylab/internal/domain"
	"strings"
	"time"
)

type BackupFilter struct {
	State, Trigger, Protection, From, To string
	Limit, Offset                        int
}

func (s *Store) ListBackupRunsFiltered(ctx context.Context, f BackupFilter) ([]domain.BackupRun, error) {
	where := []string{"1=1"}
	args := []any{}
	if f.State != "" {
		allowed := ",queued,snapshotting,validating,encrypting,local_ready,uploading,verifying_remote,succeeded,failed,canceled,interrupted,skipped,unavailable,deleted,"
		if !strings.Contains(allowed, ","+f.State+",") {
			return nil, errors.New("备份状态筛选无效")
		}
		where = append(where, "state=?")
		args = append(args, f.State)
	}
	if f.Trigger != "" {
		if f.Trigger != "manual" && f.Trigger != "scheduled" && f.Trigger != "imported" {
			return nil, errors.New("备份来源筛选无效")
		}
		where = append(where, "json_extract(data,'$.trigger')=?")
		args = append(args, f.Trigger)
	}
	switch f.Protection {
	case "":
	case "ordinary":
		where = append(where, "COALESCE(json_extract(data,'$.protected'),0)=0 AND COALESCE(json_extract(data,'$.monthly_archive'),0)=0")
	case "monthly":
		where = append(where, "COALESCE(json_extract(data,'$.monthly_archive'),0)=1")
	case "protected":
		where = append(where, "COALESCE(json_extract(data,'$.protected'),0)=1")
	default:
		return nil, errors.New("备份保护筛选无效")
	}
	var from, to time.Time
	if f.From != "" {
		var e error
		from, e = time.Parse(time.RFC3339Nano, f.From)
		if e != nil {
			return nil, errors.New("开始时间筛选无效")
		}
		where = append(where, "created_at>=?")
		args = append(args, ts(from))
	}
	if f.To != "" {
		var e error
		to, e = time.Parse(time.RFC3339Nano, f.To)
		if e != nil {
			return nil, errors.New("结束时间筛选无效")
		}
		where = append(where, "created_at<=?")
		args = append(args, ts(to))
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		return nil, errors.New("开始时间不能晚于结束时间")
	}
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 20
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	args = append(args, f.Limit, f.Offset)
	return list[domain.BackupRun](ctx, s.DB, "SELECT data FROM backup_runs WHERE "+strings.Join(where, " AND ")+" ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?", args...)
}

func (s *Store) UpdateBackupStorageHealth(ctx context.Context, id string, h domain.BackupStorageHealth) error {
	raw, e := data(h)
	if e != nil {
		return e
	}
	_, e = s.DB.ExecContext(ctx, "UPDATE backup_storages SET data=json_set(data,'$.health',json(?)) WHERE id=?", raw, id)
	return e
}
