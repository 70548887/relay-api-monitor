package server

import (
	"context"
	"github.com/gin-gonic/gin"
	"relaylab/internal/domain"
	"time"
)

func visibilityReasons(p domain.Platform, c domain.Channel, m domain.Model) []string {
	reasons := []string{}
	if !p.Public {
		reasons = append(reasons, "平台未公开")
	}
	if !c.Public {
		reasons = append(reasons, "分组未公开")
	}
	if !m.Public {
		reasons = append(reasons, "模型未公开")
	}
	if p.Archived || c.Archived || m.Archived {
		reasons = append(reasons, "对象已归档")
	}
	return reasons
}
func planStatus(p domain.Platform, c domain.Channel, m domain.Model, plan domain.Schedule, active []domain.Job, online, paused bool, now time.Time) domain.IntelligencePlanStatus {
	source := "model"
	if c.IntelligenceSchedule != nil {
		source = "group"
	}
	out := domain.IntelligencePlanStatus{ModelID: m.ID, RequestedModel: m.UpstreamID, TemplateID: plan.TemplateID, Source: source, Enabled: plan.Enabled, IntervalSeconds: plan.IntervalSeconds, State: "disabled", BlockingReason: "自动已关闭", VisibilityReasons: visibilityReasons(p, c, m)}
	if plan.Enabled {
		at := plan.NextRunAt
		out.NextRunAt = &at
	}
	if m.ID == "" || m.Archived || m.Missing || c.Archived || !c.Enabled || p.Archived {
		out.State = "unavailable"
		out.BlockingReason = "模型缺失、分组停用或对象已归档"
		return out
	}
	if !plan.Enabled {
		return out
	}
	if paused {
		out.State = "maintenance"
		out.BlockingReason = "维护暂停，恢复后继续调度"
		return out
	}
	if !online {
		out.State = "worker_offline"
		out.BlockingReason = "Worker 离线，恢复后继续调度"
		return out
	}
	out.State = "waiting"
	out.BlockingReason = "等待下次预计触发时间"
	if !plan.NextRunAt.After(now) {
		out.State = "due"
		out.BlockingReason = "计划已到期，等待 Worker 派发"
	}
	for _, j := range active {
		if j.ModelID != m.ID || j.Kind != "artifact" {
			continue
		}
		if j.State == "uncertain" {
			out.State = "cooldown"
			out.BlockingReason = "旧请求结果未知，等待截止时间及缓冲结束"
			return out
		}
		if j.TemplateID == plan.TemplateID {
			if j.State == "queued" {
				out.State = "queued"
				out.BlockingReason = "已排队，实际开始时间取决于并发空位"
			} else {
				out.State = "running"
				out.BlockingReason = "执行中，下次时间为计划触发时间"
			}
			return out
		}
		if !plan.NextRunAt.After(now) {
			out.State = "waiting"
			out.BlockingReason = "同一模型其他模板正在执行，保留本次待执行周期"
		}
	}
	return out
}
func (s *Server) intelligenceStatusSnapshot(ctx context.Context) (domain.IntelligenceStatusResponse, error) {
	out := domain.IntelligenceStatusResponse{UpdatedAt: time.Now().UTC(), Maintenance: s.Maintenance(), Groups: []domain.IntelligenceGroupStatus{}}
	platforms, e := s.Store.ListPlatforms(ctx)
	if e != nil {
		return out, e
	}
	pm := map[string]domain.Platform{}
	for _, p := range platforms {
		pm[p.ID] = p
	}
	channels, e := s.Store.ListChannels(ctx)
	if e != nil {
		return out, e
	}
	models, e := s.Store.ListModels(ctx)
	if e != nil {
		return out, e
	}
	mm := map[string]domain.Model{}
	for _, m := range models {
		mm[m.ID] = m
	}
	plans, e := s.Store.ListSchedules(ctx)
	if e != nil {
		return out, e
	}
	jobs, e := s.Store.ListActiveJobs(ctx)
	if e != nil {
		return out, e
	}
	health, e := s.Store.WorkerHealth(ctx)
	if e != nil {
		return out, e
	}
	out.WorkerOnline = health.Online
	for _, c := range channels {
		if c.Archived {
			continue
		}
		p := pm[c.PlatformID]
		group := domain.IntelligenceGroupStatus{ChannelID: c.ID, ChannelName: c.Name, PlatformID: c.PlatformID, PlatformName: p.Name, VisibilityReasons: []string{}, Plans: []domain.IntelligencePlanStatus{}}
		if !p.Public {
			group.VisibilityReasons = append(group.VisibilityReasons, "平台未公开")
		}
		if !c.Public {
			group.VisibilityReasons = append(group.VisibilityReasons, "分组未公开")
		}
		for _, plan := range plans {
			m, ok := mm[plan.ModelID]
			if !ok || m.ChannelID != c.ID || m.Archived || plan.Kind != "artifact" {
				continue
			}
			if c.IntelligenceSchedule != nil {
				if c.IntelligenceSchedule.ModelScope != "all" && c.IntelligenceSchedule.ModelID != m.ID {
					continue
				}
				found := false
				for _, id := range c.IntelligenceSchedule.TemplateIDs {
					if id == plan.TemplateID {
						found = true
					}
				}
				if !found {
					continue
				}
			} else if !plan.Enabled && !m.ArtifactChinese && !m.ArtifactEnglish && c.IntelligenceModelID != m.ID {
				continue
			}
			group.Plans = append(group.Plans, planStatus(p, c, m, plan, jobs, health.Online, out.Maintenance, out.UpdatedAt))
		}
		out.Groups = append(out.Groups, group)
	}
	return out, nil
}
func (s *Server) intelligenceStatus(c *gin.Context) {
	value, err := s.intelligenceStatusSnapshot(c.Request.Context())
	if err != nil {
		dbError(c, err)
		return
	}
	c.JSON(200, value)
}

func (s *Server) adminJobViews(ctx context.Context, values []domain.Job) ([]domain.AdminJobView, error) {
	platforms, e := s.Store.ListPlatforms(ctx)
	if e != nil {
		return nil, e
	}
	pm := map[string]domain.Platform{}
	for _, p := range platforms {
		pm[p.ID] = p
	}
	channels, e := s.Store.ListChannels(ctx)
	if e != nil {
		return nil, e
	}
	cm := map[string]domain.Channel{}
	for _, c := range channels {
		cm[c.ID] = c
	}
	models, e := s.Store.ListModels(ctx)
	if e != nil {
		return nil, e
	}
	mm := map[string]domain.Model{}
	for _, m := range models {
		mm[m.ID] = m
	}
	hidden := map[string]bool{}
	rows, e := s.Store.DB.QueryContext(ctx, "SELECT job_id FROM artifacts WHERE hidden=1")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		hidden[id] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := make([]domain.AdminJobView, 0, len(values))
	for _, j := range values {
		c := cm[j.ChannelID]
		m := mm[j.ModelID]
		reasons := visibilityReasons(pm[c.PlatformID], c, m)
		if hidden[j.ID] {
			reasons = append(reasons, "作品已隐藏")
		}
		out = append(out, domain.AdminJobView{Job: j, RequestedModel: m.UpstreamID, ChannelName: c.Name, ContextPolicy: "independent", VisibilityReasons: reasons})
	}
	return out, nil
}
