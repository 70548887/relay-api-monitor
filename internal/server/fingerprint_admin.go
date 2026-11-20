package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"relaylab/internal/domain"
	"relaylab/internal/fingerprint"
)

// A refresh downloads the whole bank, about 5 MB, from hosts that can be slow to reach.
const fingerprintRefreshWait = 5 * time.Minute

// fingerprintModelSettings reads the settings of a model the way a detection round applies them.
func fingerprintModelSettings(m domain.Model) domain.FingerprintModelSettings {
	out := domain.FingerprintModelSettings{Mode: domain.FingerprintModeAuto, Questions: fingerprint.Questions(m), Scheduled: m.FingerprintScheduled}
	switch m.FingerprintMode {
	case domain.FingerprintModeManual:
		out.Mode, out.Model = domain.FingerprintModeManual, m.FingerprintModel
	case domain.FingerprintModeUnlisted:
		out.Mode = domain.FingerprintModeUnlisted
	}
	return out
}

func (s *Server) adminFingerprint(c *gin.Context) {
	ctx := c.Request.Context()
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	platforms, err := s.Store.ListPlatforms(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	channels, err := s.Store.ListChannels(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	models, err := s.Store.ListModels(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	rounds, err := s.Store.ListLatestFingerprintResults(ctx, 1)
	if err != nil {
		dbError(c, err)
		return
	}
	jobs, err := s.Store.ListLatestFingerprintJobs(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	schedules, err := s.Store.ListSchedules(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	platformByID := map[string]domain.Platform{}
	for _, p := range platforms {
		platformByID[p.ID] = p
	}
	channelByID := map[string]domain.Channel{}
	for _, ch := range channels {
		channelByID[ch.ID] = ch
	}
	latest := map[string]*domain.FingerprintResult{}
	for i := range rounds {
		latest[rounds[i].ModelID] = &rounds[i]
	}
	job := map[string]*domain.Job{}
	for i := range jobs {
		job[jobs[i].ModelID] = &jobs[i]
	}
	nextRun := map[string]*time.Time{}
	for i := range schedules {
		if plan := &schedules[i]; plan.Kind == "fingerprint" && plan.Enabled {
			nextRun[plan.ModelID] = &plan.NextRunAt
		}
	}

	out := domain.AdminFingerprintOverview{Bank: domain.FingerprintBank{Source: "online"}, IntervalSeconds: settings.FingerprintIntervalSeconds, References: []domain.FingerprintModel{}, Models: []domain.AdminFingerprintModel{}}
	bankCtx, cancel := context.WithTimeout(ctx, fingerprintPageWait)
	defer cancel()
	if s.Fingerprint != nil {
		bank, references := s.Fingerprint.Catalog(bankCtx, settings.FingerprintBankDir)
		out.Bank = bank
		if references != nil {
			out.References = references
		}
	}
	for _, m := range models {
		ch, ok := channelByID[m.ChannelID]
		p, known := platformByID[ch.PlatformID]
		if !ok || !known || m.Archived || ch.Archived || p.Archived {
			continue
		}
		row := domain.AdminFingerprintModel{
			ID: m.ID, UpstreamID: m.UpstreamID, DisplayName: m.DisplayName, ChannelID: ch.ID, ChannelName: ch.Name, PlatformID: p.ID, PlatformName: p.Name,
			Runnable: !m.Missing && ch.Enabled, Settings: fingerprintModelSettings(m), Latest: latest[m.ID], Job: job[m.ID], NextRunAt: nextRun[m.ID],
		}
		if out.Bank.Ready {
			if expected, err := s.Fingerprint.Expected(bankCtx, settings.FingerprintBankDir, m); err == nil {
				row.Expected = expected
			}
		}
		out.Models = append(out.Models, row)
	}
	c.JSON(200, out)
}

func (s *Server) saveFingerprintModel(c *gin.Context) {
	var b domain.FingerprintModelSettings
	if !bind(c, &b) {
		return
	}
	b.Mode, b.Model = strings.TrimSpace(b.Mode), strings.TrimSpace(b.Model)
	if b.Mode == "" {
		b.Mode = domain.FingerprintModeAuto
	}
	if b.Questions == 0 {
		b.Questions = fingerprint.MaxChallenges
	}
	if (b.Mode != domain.FingerprintModeAuto && b.Mode != domain.FingerprintModeManual && b.Mode != domain.FingerprintModeUnlisted) || b.Questions < 1 || b.Questions > fingerprint.MaxChallenges {
		fail(c, 400, "invalid_fingerprint_settings", "期望模型的方式或题数不正确")
		return
	}
	ctx := c.Request.Context()
	m, err := s.Store.GetModel(ctx, c.Param("id"))
	if err != nil {
		dbError(c, err)
		return
	}
	if b.Mode != domain.FingerprintModeManual {
		b.Model = ""
	} else if !s.acceptFingerprintReference(c, b.Model) {
		return
	}
	if err = s.Store.SaveFingerprintModelSettings(ctx, m.ID, b); err != nil {
		dbError(c, err)
		return
	}
	s.auditEvent(ctx, "fingerprint_model_saved", m.ID, fmt.Sprintf("mode=%s model=%s questions=%d scheduled=%t", b.Mode, b.Model, b.Questions, b.Scheduled))
	c.JSON(200, b)
}

// acceptFingerprintReference reports whether id is a model of the reference bank, and answers the request when it is
// not or the bank cannot tell.
func (s *Server) acceptFingerprintReference(c *gin.Context, id string) bool {
	if id == "" {
		fail(c, 400, "invalid_fingerprint_model", "请选择期望模型")
		return false
	}
	if s.Fingerprint == nil {
		fingerprintUnavailable(c)
		return false
	}
	settings, err := s.Store.GetSettings(c.Request.Context())
	if err != nil {
		dbError(c, err)
		return false
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), fingerprintJudgeWait)
	defer cancel()
	bank, references := s.Fingerprint.Catalog(ctx, settings.FingerprintBankDir)
	if !bank.Ready {
		fingerprintUnavailable(c)
		return false
	}
	for _, reference := range references {
		if reference.ID == id {
			return true
		}
	}
	fail(c, 400, "invalid_fingerprint_model", "参考库中没有这个模型")
	return false
}

func (s *Server) adminFingerprintResults(c *gin.Context) {
	ctx := c.Request.Context()
	m, err := s.Store.GetModel(ctx, c.Param("id"))
	if err != nil {
		dbError(c, err)
		return
	}
	rounds, err := s.Store.ListFingerprintResults(ctx, m.ID)
	if err != nil {
		dbError(c, err)
		return
	}
	c.JSON(200, rounds)
}

func (s *Server) refreshFingerprintBank(c *gin.Context) {
	if s.Fingerprint == nil {
		fingerprintUnavailable(c)
		return
	}
	settings, err := s.Store.GetSettings(c.Request.Context())
	if err != nil {
		dbError(c, err)
		return
	}
	if settings.FingerprintBankDir != "" {
		fail(c, 400, "fingerprint_bank_local", "当前使用本地参考库目录，替换目录中的文件即可生效，无需在线更新")
		return
	}
	if !s.bankRefreshing.CompareAndSwap(false, true) {
		fail(c, 409, "fingerprint_refresh_running", "参考库正在更新，请稍候")
		return
	}
	defer s.bankRefreshing.Store(false)
	ctx, cancel := context.WithTimeout(c.Request.Context(), fingerprintRefreshWait)
	defer cancel()
	_, changed, err := s.Fingerprint.Refresh(ctx)
	if err != nil {
		// Only administrators get here, and the cause (an unreachable host, a full disk) is what they need to act on.
		fail(c, 502, "fingerprint_refresh_failed", "参考库更新失败，原有参考库不受影响："+err.Error())
		return
	}
	bank, _ := s.Fingerprint.Catalog(ctx, "")
	s.auditEvent(c.Request.Context(), "fingerprint_bank_refreshed", "", bank.BuiltAt)
	c.JSON(200, domain.FingerprintBankRefresh{Bank: bank, Changed: changed})
}
