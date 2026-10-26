package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"relaylab/internal/domain"
)

var ErrInvalidIntelligenceSchedule = errors.New("分组定时测试配置不正确")

// SetIntelligenceSchedule changes plans without changing request credentials or
// the channel request revision. Running and manually queued jobs keep their
// configuration; unsent scheduled jobs are checked and canceled atomically.
func (s *Store) SetIntelligenceSchedule(ctx context.Context, id string, plan domain.IntelligenceSchedule) (*domain.Channel, error) {
	var saved *domain.Channel
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		c, err := channel(ctx, tx, id)
		if err != nil {
			return err
		}
		if !plan.Enabled && plan.ModelID == "" && len(plan.TemplateIDs) == 0 && plan.IntervalSeconds == 0 {
			if c.IntelligenceSchedule != nil {
				plan = *c.IntelligenceSchedule
				plan.Enabled = false
			} else {
				plan.IntervalSeconds = 1800
			}
		}
		invalid := func(message string) error { return fmt.Errorf("%w：%s", ErrInvalidIntelligenceSchedule, message) }
		if plan.IntervalSeconds < 60 || plan.IntervalSeconds > 604800 {
			return invalid("间隔须为 60 至 604800 秒")
		}
		if len(plan.TemplateIDs) > 2 || (plan.Enabled && len(plan.TemplateIDs) == 0) {
			return invalid("请选择中文 HTML 或英文 SVG 模板")
		}
		seen := map[string]bool{}
		for _, template := range plan.TemplateIDs {
			if (template != "cn_html" && template != "en_svg") || seen[template] {
				return invalid("模板不支持或重复")
			}
			seen[template] = true
		}
		if plan.ModelID != "" {
			m, err := load[domain.Model](ctx, tx, "SELECT data FROM models WHERE id=?", plan.ModelID)
			if err != nil || m.ChannelID != c.ID {
				return invalid("模型必须属于当前分组")
			}
			if plan.Enabled && (m.Archived || m.Missing) {
				return invalid("请选择未归档且未缺失的模型")
			}
		} else if plan.Enabled {
			return invalid("请选择定时测试模型")
		}
		if plan.Enabled {
			p, err := load[domain.Platform](ctx, tx, "SELECT data FROM platforms WHERE id=?", c.PlatformID)
			if err != nil {
				return err
			}
			if !c.Enabled || c.Archived || p.Archived {
				return invalid("请先启用分组及所属平台")
			}
		}
		c.IntelligenceSchedule = &plan
		if err = saveChannel(ctx, tx, *c); err != nil {
			return err
		}
		models, err := list[domain.Model](ctx, tx, "SELECT data FROM models WHERE channel_id=? ORDER BY id", c.ID)
		if err != nil {
			return err
		}
		for _, m := range models {
			if err = reconcileSchedules(ctx, tx, m); err != nil {
				return err
			}
		}
		saved = c
		return nil
	})
	return saved, err
}
