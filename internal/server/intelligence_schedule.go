package server

import (
	"errors"

	"github.com/gin-gonic/gin"
	"relaylab/internal/domain"
	"relaylab/internal/store"
)

func (s *Server) saveIntelligenceSchedule(c *gin.Context) {
	var body struct {
		Enabled         *bool    `json:"enabled"`
		ModelID         string   `json:"model_id"`
		TemplateIDs     []string `json:"template_ids"`
		IntervalSeconds int      `json:"interval_seconds"`
	}
	if !bind(c, &body) {
		return
	}
	if body.Enabled == nil {
		fail(c, 400, "invalid_schedule", "请指定是否开启分组定时测试")
		return
	}
	plan := domain.IntelligenceSchedule{Enabled: *body.Enabled, ModelID: body.ModelID, TemplateIDs: body.TemplateIDs, IntervalSeconds: body.IntervalSeconds}
	saved, err := s.Store.SetIntelligenceSchedule(c.Request.Context(), c.Param("id"), plan)
	if errors.Is(err, store.ErrInvalidIntelligenceSchedule) {
		fail(c, 400, "invalid_schedule", err.Error())
		return
	}
	if err != nil {
		dbError(c, err)
		return
	}
	s.auditEvent(c.Request.Context(), "channel_intelligence_schedule_saved", saved.ID, "更新分组定时测试计划")
	c.JSON(200, saved)
}
