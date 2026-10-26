package server

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"relaylab/internal/artifact"
	"relaylab/internal/domain"
	"relaylab/internal/security"
)

type publicCatalog struct {
	platforms    []domain.Platform
	channels     []domain.Channel
	models       []domain.Model
	platformByID map[string]domain.Platform
	channelByID  map[string]domain.Channel
	modelByID    map[string]domain.Model
}

func (s *Server) publicCatalog(ctx context.Context) (publicCatalog, error) {
	result := publicCatalog{platforms: []domain.Platform{}, channels: []domain.Channel{}, models: []domain.Model{}, platformByID: map[string]domain.Platform{}, channelByID: map[string]domain.Channel{}, modelByID: map[string]domain.Model{}}
	platforms, err := s.Store.ListPlatforms(ctx)
	if err != nil {
		return result, err
	}
	for _, p := range platforms {
		if p.Public && !p.Archived {
			result.platforms = append(result.platforms, p)
			result.platformByID[p.ID] = p
		}
	}
	channels, err := s.Store.ListChannels(ctx)
	if err != nil {
		return result, err
	}
	for _, ch := range channels {
		if _, ok := result.platformByID[ch.PlatformID]; ok && ch.Public && !ch.Archived {
			result.channels = append(result.channels, ch)
			result.channelByID[ch.ID] = ch
		}
	}
	models, err := s.Store.ListModels(ctx)
	if err != nil {
		return result, err
	}
	for _, m := range models {
		if _, ok := result.channelByID[m.ChannelID]; ok && m.Public && !m.Archived {
			result.models = append(result.models, m)
			result.modelByID[m.ID] = m
		}
	}
	return result, nil
}

type hourlyBucket struct {
	At     time.Time `json:"at"`
	Status string    `json:"status"`
	Count  int       `json:"count"`
}
type publicModel struct {
	ID              string              `json:"id"`
	ChannelID       string              `json:"channel_id"`
	UpstreamID      string              `json:"upstream_id"`
	DisplayName     string              `json:"display_name"`
	Missing         bool                `json:"missing"`
	Protocol        string              `json:"protocol"`
	ProbeEnabled    bool                `json:"probe_enabled"`
	ArtifactChinese bool                `json:"artifact_chinese"`
	ArtifactEnglish bool                `json:"artifact_english"`
	Sort            int                 `json:"sort"`
	CreatedAt       time.Time           `json:"created_at"`
	PlatformID      string              `json:"platform_id"`
	PlatformName    string              `json:"platform_name"`
	ChannelName     string              `json:"channel_name"`
	Status          string              `json:"status"`
	Stale           bool                `json:"stale"`
	Running         bool                `json:"running"`
	LastProbe       *domain.ProbeResult `json:"last_probe"`
	SuccessRate     *float64            `json:"success_rate"`
	Samples         int                 `json:"samples"`
	Timeline        []hourlyBucket      `json:"timeline"`
}

func displayStatus(status string) string {
	switch status {
	case "success", "ok":
		return "ok"
	case "slow":
		return "slow"
	case "failed":
		return "failed"
	default:
		return "unknown"
	}
}
func statusRank(status string) int {
	switch status {
	case "failed":
		return 3
	case "slow":
		return 2
	case "ok":
		return 1
	}
	return 0
}
func safeProbe(probe domain.ProbeResult) domain.ProbeResult {
	probe.Status = displayStatus(probe.Status)
	if probe.ErrorMessage != "" {
		probe.ErrorMessage = "上游检测失败"
	}
	return probe
}

type probeStatistics struct {
	last               *domain.ProbeResult
	samples, successes int
	hours              map[string]hourlyBucket
}

const databaseTimeFormat = "2006-01-02T15:04:05.000000000Z"

// Only latest documents and SQL aggregates cross into Go, rather than every
// historical response document on each monitor refresh.
func (s *Server) probeStatistics(ctx context.Context, now time.Time) (map[string]*probeStatistics, map[string]bool, error) {
	stats := map[string]*probeStatistics{}
	running := map[string]bool{}
	get := func(id string) *probeStatistics {
		if stats[id] == nil {
			stats[id] = &probeStatistics{hours: map[string]hourlyBucket{}}
		}
		return stats[id]
	}
	latest, err := s.Store.DB.QueryContext(ctx, `SELECT p.model_id,p.data FROM probe_results p JOIN (SELECT model_id,MAX(created_at) AS at FROM probe_results WHERE created_at<=? GROUP BY model_id) latest ON p.model_id=latest.model_id AND p.created_at=latest.at ORDER BY p.id DESC`, now.Format(databaseTimeFormat))
	if err != nil {
		return nil, nil, err
	}
	for latest.Next() {
		var id, raw string
		if err = latest.Scan(&id, &raw); err != nil {
			latest.Close()
			return nil, nil, err
		}
		if get(id).last != nil {
			continue
		}
		var probe domain.ProbeResult
		if err = json.Unmarshal([]byte(raw), &probe); err != nil {
			latest.Close()
			return nil, nil, err
		}
		safe := safeProbe(probe)
		get(id).last = &safe
	}
	err = latest.Err()
	latest.Close()
	if err != nil {
		return nil, nil, err
	}
	weekly, err := s.Store.DB.QueryContext(ctx, `SELECT model_id,COUNT(*),SUM(CASE WHEN json_extract(data,'$.status') IN ('success','ok','slow') THEN 1 ELSE 0 END) FROM probe_results WHERE created_at>=? AND created_at<=? GROUP BY model_id`, now.Add(-7*24*time.Hour).Format(databaseTimeFormat), now.Format(databaseTimeFormat))
	if err != nil {
		return nil, nil, err
	}
	for weekly.Next() {
		var id string
		var samples, successes int
		if err = weekly.Scan(&id, &samples, &successes); err != nil {
			weekly.Close()
			return nil, nil, err
		}
		value := get(id)
		value.samples = samples
		value.successes = successes
	}
	err = weekly.Err()
	weekly.Close()
	if err != nil {
		return nil, nil, err
	}
	hourStart := now.Truncate(time.Hour).Add(-23 * time.Hour)
	hourly, err := s.Store.DB.QueryContext(ctx, `SELECT model_id,substr(created_at,1,13),COUNT(*),MAX(CASE json_extract(data,'$.status') WHEN 'failed' THEN 3 WHEN 'slow' THEN 2 WHEN 'success' THEN 1 WHEN 'ok' THEN 1 ELSE 0 END) FROM probe_results WHERE created_at>=? AND created_at<=? GROUP BY model_id,substr(created_at,1,13)`, hourStart.Format(databaseTimeFormat), now.Format(databaseTimeFormat))
	if err != nil {
		return nil, nil, err
	}
	for hourly.Next() {
		var id, hour string
		var count, rank int
		if err = hourly.Scan(&id, &hour, &count, &rank); err != nil {
			hourly.Close()
			return nil, nil, err
		}
		status := "unknown"
		switch rank {
		case 1:
			status = "ok"
		case 2:
			status = "slow"
		case 3:
			status = "failed"
		}
		get(id).hours[hour] = hourlyBucket{Status: status, Count: count}
	}
	err = hourly.Err()
	hourly.Close()
	if err != nil {
		return nil, nil, err
	}
	active, err := s.Store.DB.QueryContext(ctx, `SELECT DISTINCT model_id FROM jobs WHERE kind='probe' AND state IN ('claimed','dispatching')`)
	if err != nil {
		return nil, nil, err
	}
	for active.Next() {
		var id string
		if err = active.Scan(&id); err != nil {
			active.Close()
			return nil, nil, err
		}
		running[id] = true
	}
	err = active.Err()
	active.Close()
	return stats, running, err
}

func (s *Server) monitor(c *gin.Context) {
	ctx := c.Request.Context()
	catalog, err := s.publicCatalog(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	now := time.Now().UTC()
	stats, running, err := s.probeStatistics(ctx, now)
	if err != nil {
		dbError(c, err)
		return
	}
	models := []publicModel{}
	summary := gin.H{"platforms": len(catalog.platforms), "models": len(catalog.models), "normal": 0, "slow": 0, "failed": 0, "stale": 0}
	hourStart := now.Truncate(time.Hour).Add(-23 * time.Hour)
	for _, m := range catalog.models {
		ch := catalog.channelByID[m.ChannelID]
		p := catalog.platformByID[ch.PlatformID]
		v := publicModel{ID: m.ID, ChannelID: m.ChannelID, UpstreamID: m.UpstreamID, DisplayName: m.DisplayName, Missing: m.Missing, Protocol: m.Protocol, ProbeEnabled: m.ProbeEnabled, ArtifactChinese: m.ArtifactChinese, ArtifactEnglish: m.ArtifactEnglish, Sort: m.Sort, CreatedAt: m.CreatedAt, PlatformID: p.ID, PlatformName: p.Name, ChannelName: ch.Name, Status: "unknown", Running: running[m.ID], Timeline: make([]hourlyBucket, 24)}
		for index := range v.Timeline {
			v.Timeline[index] = hourlyBucket{At: hourStart.Add(time.Duration(index) * time.Hour), Status: "unknown"}
		}
		if value := stats[m.ID]; value != nil {
			v.LastProbe = value.last
			if value.last != nil {
				v.Status = value.last.Status
			}
			v.Samples = value.samples
			if v.Samples > 0 {
				rate := 100 * float64(value.successes) / float64(v.Samples)
				v.SuccessRate = &rate
			}
			for index := range v.Timeline {
				if bucket, ok := value.hours[v.Timeline[index].At.Format("2006-01-02T15")]; ok {
					v.Timeline[index].Status = bucket.Status
					v.Timeline[index].Count = bucket.Count
				}
			}
		}
		if !ch.Enabled || !m.ProbeEnabled {
			v.Status = "disabled"
			v.Running = false
		} else {
			interval := m.ProbeIntervalSeconds
			if interval <= 0 {
				interval = settings.ProbeIntervalSeconds
			}
			if interval < 30 {
				interval = 30
			}
			v.Stale = v.LastProbe == nil || now.Sub(v.LastProbe.CreatedAt) > time.Duration(interval*2)*time.Second
		}
		if v.Status == "ok" {
			summary["normal"] = summary["normal"].(int) + 1
		}
		if v.Status == "slow" {
			summary["slow"] = summary["slow"].(int) + 1
		}
		if v.Status == "failed" {
			summary["failed"] = summary["failed"].(int) + 1
		}
		if v.Stale {
			summary["stale"] = summary["stale"].(int) + 1
		}
		models = append(models, v)
	}
	channels := []gin.H{}
	for _, ch := range catalog.channels {
		channels = append(channels, gin.H{"id": ch.ID, "platform_id": ch.PlatformID, "name": ch.Name, "sort": ch.Sort})
	}
	c.JSON(200, gin.H{"platforms": catalog.platforms, "channels": channels, "models": models, "summary": summary, "updated_at": now})
}

func (s *Server) history(c *gin.Context) {
	ctx := c.Request.Context()
	catalog, err := s.publicCatalog(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	if _, ok := catalog.modelByID[c.Param("id")]; !ok {
		fail(c, 404, "not_found", "模型不存在")
		return
	}
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	days := settings.RetentionDays
	if days < 1 {
		days = 30
	}
	limit, err := security.ValidateInt(c.DefaultQuery("limit", "1000"), 1, 5000)
	if err != nil {
		fail(c, 400, "invalid_limit", "历史条数须为 1 至 5000")
		return
	}
	values, err := s.Store.ListProbeResultsLimit(ctx, c.Param("id"), time.Now().UTC().Add(-time.Duration(days)*24*time.Hour), limit)
	if err != nil {
		dbError(c, err)
		return
	}
	out := []domain.ProbeResult{}
	for _, probe := range values {
		out = append(out, safeProbe(probe))
	}
	c.JSON(200, out)
}

func publicAttempt(job domain.Job) gin.H {
	message := ""
	if job.ErrorMessage != "" {
		message = "最近生成尝试未成功，请稍后重试"
	}
	var snapshot struct {
		RequestedModel  string `json:"requested_model"`
		Protocol        string `json:"protocol"`
		ReasoningEffort string `json:"reasoning_effort"`
		ThinkingMode    string `json:"thinking_mode"`
		ThinkingBudget  int    `json:"thinking_budget"`
		Parameters      struct {
			ReasoningEffort string `json:"reasoning_effort"`
			ThinkingMode    string `json:"thinking_mode"`
			ThinkingBudget  int    `json:"thinking_budget"`
		} `json:"parameters"`
	}
	_ = json.Unmarshal([]byte(job.Snapshot), &snapshot)
	if snapshot.ReasoningEffort == "" {
		snapshot.ReasoningEffort = snapshot.Parameters.ReasoningEffort
	}
	if snapshot.ThinkingMode == "" {
		snapshot.ThinkingMode = snapshot.Parameters.ThinkingMode
	}
	if snapshot.ThinkingBudget == 0 {
		snapshot.ThinkingBudget = snapshot.Parameters.ThinkingBudget
	}
	return gin.H{"id": job.ID, "model_id": job.ModelID, "channel_id": job.ChannelID, "template_id": job.TemplateID, "kind": job.Kind, "state": job.State, "created_at": job.CreatedAt, "started_at": job.StartedAt, "finished_at": job.FinishedAt, "error_code": job.ErrorCode, "error_message": message, "progress_chars": job.ProgressChars, "progress_at": job.ProgressAt, "returned_model": job.ReturnedModel, "requested_model": snapshot.RequestedModel, "protocol": snapshot.Protocol, "reasoning_effort": snapshot.ReasoningEffort, "thinking_mode": snapshot.ThinkingMode, "thinking_budget": snapshot.ThinkingBudget}
}
func publicArtifactMap(a domain.Artifact, catalog publicCatalog, full bool) map[string]any {
	// Select public fields rather than copying an internal persistence document.
	// Parameters remain comparable without exposing newly added transport fields.
	var parameters struct {
		MaxTokens       int    `json:"max_tokens,omitempty"`
		TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
		ReasoningEffort string `json:"reasoning_effort,omitempty"`
		ThinkingMode    string `json:"thinking_mode,omitempty"`
		ThinkingBudget  int    `json:"thinking_budget,omitempty"`
	}
	_ = json.Unmarshal([]byte(a.Parameters), &parameters)
	encoded, _ := json.Marshal(parameters)
	v := map[string]any{"id": a.ID, "model_id": a.ModelID, "channel_id": a.ChannelID, "template_id": a.TemplateID, "template_version": a.TemplateVersion, "prompt": a.Prompt, "protocol": a.Protocol, "requested_model": a.RequestedModel, "returned_model": a.ReturnedModel, "parameters": string(encoded), "kind": a.Kind, "first_text_ms": a.FirstTextMS, "duration_ms": a.DurationMS, "usage": a.Usage, "finish_reason": a.FinishReason, "started_at": a.StartedAt, "created_at": a.CreatedAt}
	v["reasoning_effort"], v["thinking_mode"], v["thinking_budget"] = a.ReasoningEffort, a.ThinkingMode, a.ThinkingBudget
	v["verified_group"], v["group_verification_status"] = a.VerifiedGroup, a.GroupVerificationStatus
	m := catalog.modelByID[a.ModelID]
	ch := catalog.channelByID[m.ChannelID]
	p := catalog.platformByID[ch.PlatformID]
	v["platform_name"] = p.Name
	v["channel_name"] = ch.Name
	v["display_name"] = m.DisplayName
	if full {
		v["code"] = a.Code
		v["preview_document"] = artifact.Preview(a.Code, a.Kind)
	}
	return v
}
func visibleArtifact(a domain.Artifact, catalog publicCatalog) bool {
	m, ok := catalog.modelByID[a.ModelID]
	return ok && !a.Hidden && a.ChannelID == m.ChannelID
}

func (s *Server) publicArtifacts(c *gin.Context) {
	ctx := c.Request.Context()
	catalog, err := s.publicCatalog(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	values, err := s.Store.ListArtifactsSummary(ctx, c.Query("model_id"))
	if err != nil {
		dbError(c, err)
		return
	}
	jobs, err := s.Store.LatestArtifactAttempts(ctx, c.Query("model_id"), c.Query("template_id"))
	if err != nil {
		dbError(c, err)
		return
	}
	latest := map[string]domain.Job{}
	for _, job := range jobs {
		if job.Kind != "artifact" {
			continue
		}
		key := job.ModelID + "\x00" + job.TemplateID
		if _, ok := latest[key]; !ok {
			latest[key] = job
		}
	}
	out := []map[string]any{}
	for _, a := range values {
		if !visibleArtifact(a, catalog) || (c.Query("template_id") != "" && a.TemplateID != c.Query("template_id")) {
			continue
		}
		v := publicArtifactMap(a, catalog, false)
		if attempt, ok := latest[a.ModelID+"\x00"+a.TemplateID]; ok {
			v["last_attempt"] = publicAttempt(attempt)
		}
		out = append(out, v)
	}
	c.JSON(200, out)
}
func (s *Server) publicArtifact(c *gin.Context) {
	ctx := c.Request.Context()
	catalog, err := s.publicCatalog(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	a, err := s.Store.GetArtifact(ctx, c.Param("id"))
	if err != nil {
		dbError(c, err)
		return
	}
	if !visibleArtifact(*a, catalog) {
		fail(c, 404, "not_found", "作品不存在")
		return
	}
	v := publicArtifactMap(*a, catalog, true)
	jobs, err := s.Store.LatestArtifactAttempts(ctx, a.ModelID, a.TemplateID)
	if err != nil {
		dbError(c, err)
		return
	}
	for _, job := range jobs {
		if job.Kind == "artifact" && job.ModelID == a.ModelID && job.TemplateID == a.TemplateID {
			v["last_attempt"] = publicAttempt(job)
			break
		}
	}
	c.JSON(200, v)
}
func (s *Server) source(c *gin.Context) {
	ctx := c.Request.Context()
	catalog, err := s.publicCatalog(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	a, err := s.Store.GetArtifact(ctx, c.Param("id"))
	if err != nil {
		dbError(c, err)
		return
	}
	if !visibleArtifact(*a, catalog) {
		fail(c, 404, "not_found", "作品不存在")
		return
	}
	extension := "html"
	if a.Kind == "svg" {
		extension = "svg"
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "artifact." + extension}))
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(a.Code))
}
