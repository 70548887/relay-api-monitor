package backups

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"relaylab/internal/domain"
)

func validateSchedule(p *Schedule) error {
	if p.Timezone == "" {
		p.Timezone = "Asia/Shanghai"
	}
	if _, e := time.LoadLocation(p.Timezone); e != nil {
		return errors.New("备份时区不正确")
	}
	if p.Mode == "" {
		p.Mode = "local"
	}
	if p.Mode != "local" && p.Mode != "local_s3" {
		return errors.New("备份位置必须是local或local_s3")
	}
	if p.Cron == "" {
		p.Cron = "0 2 * * *"
	}
	if len(strings.Fields(p.Cron)) != 5 {
		return errors.New("请输入五段Cron表达式")
	}
	if _, e := cron.ParseStandard(p.Cron); e != nil {
		return errors.New("Cron表达式不正确")
	}
	if p.RetainDays < 0 || p.RetainDays > 36500 || p.RetainCount < 0 || p.RetainCount > 10000 || p.MonthlyRetainCount < 0 || p.MonthlyRetainCount > 10000 {
		return errors.New("保留天数或份数超出范围")
	}
	days := map[int]bool{}
	for _, d := range p.MonthlyDays {
		if d < 1 || d > 31 {
			return errors.New("月度日期必须为1至31")
		}
		days[d] = true
	}
	p.MonthlyDays = []int{}
	for d := range days {
		p.MonthlyDays = append(p.MonthlyDays, d)
	}
	sort.Ints(p.MonthlyDays)
	if p.MonthlyEnabled && len(p.MonthlyDays) == 0 && !p.MonthlyMonthEnd {
		return errors.New("请至少选择一个月度归档日期")
	}
	return nil
}
func Preview(p Schedule) ([]time.Time, error)              { return previewAt(p, time.Now().UTC()) }
func (m *Manager) Preview(p Schedule) ([]time.Time, error) { return Preview(p) }
func previewAt(p Schedule, now time.Time) ([]time.Time, error) {
	if e := validateSchedule(&p); e != nil {
		return nil, e
	}
	loc, _ := time.LoadLocation(p.Timezone)
	schedule, e := cron.ParseStandard(p.Cron)
	if e != nil {
		return nil, e
	}
	out := []time.Time{}
	next := now.In(loc)
	for i := 0; i < 3; i++ {
		next = schedule.Next(next)
		if next.IsZero() {
			return nil, errors.New("Cron在可计算范围内没有执行时间")
		}
		out = append(out, next.UTC())
	}
	return out, nil
}
func (m *Manager) tick(now time.Time) {
	m.policyMu.Lock()
	p, e := m.opts.Store.BackupPolicy(m.ctx)
	if e != nil || !p.Enabled || p.ReconciliationRequired {
		m.policyMu.Unlock()
		return
	}
	if p.NextRunAt == nil {
		next, e := previewAt(p, now)
		if e == nil {
			p.NextRunAt = &next[0]
			m.opts.Store.SaveBackupPolicy(m.ctx, p)
		}
		m.policyMu.Unlock()
		return
	}
	if p.NextRunAt.After(now) {
		m.policyMu.Unlock()
		return
	}
	slot := *p.NextRunAt
	next, e := previewAt(p, now)
	if e != nil {
		m.policyMu.Unlock()
		return
	}
	p.NextRunAt = &next[0]
	if e = m.opts.Store.SaveBackupPolicy(m.ctx, p); e != nil {
		m.policyMu.Unlock()
		return
	}
	m.policyMu.Unlock()
	_, e = m.create(m.ctx, "scheduled:"+slot.UTC().Format(time.RFC3339), &slot)
	if e == nil {
		return
	}
	n := time.Now().UTC()
	r := Run{ID: domain.NewID(), RequestID: "scheduled:" + slot.UTC().Format(time.RFC3339), State: "skipped", Trigger: "scheduled", ScheduledFor: &slot, CreatedAt: n, StartedAt: n, FinishedAt: &n, HeartbeatAt: n, Version: m.opts.BuildVersion, DatabaseSchema: 4, ErrorCode: "scheduled_skipped", Message: "本周期未执行：已有操作、维护或配置尚未就绪"}
	m.opts.Store.CreateBackupRun(context.Background(), r)
}
func archiveDates(start time.Time, p Schedule) []string {
	loc, e := time.LoadLocation(p.Timezone)
	if e != nil {
		return nil
	}
	start = start.In(loc)
	last := time.Date(start.Year(), start.Month()+1, 0, 0, 0, 0, 0, loc).Day()
	unique := map[int]bool{}
	for _, d := range p.MonthlyDays {
		unique[min(d, last)] = true
	}
	if p.MonthlyMonthEnd {
		unique[last] = true
	}
	out := []string{}
	for d := range unique {
		if d <= start.Day() {
			out = append(out, strconv.Itoa(start.Year())+"-"+twoDigits(int(start.Month()))+"-"+twoDigits(d))
		}
	}
	sort.Strings(out)
	return out
}
func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
