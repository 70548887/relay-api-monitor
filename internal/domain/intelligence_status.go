package domain

import "time"

type IntelligencePlanStatus struct {
	ModelID           string     `json:"model_id"`
	RequestedModel    string     `json:"requested_model"`
	TemplateID        string     `json:"template_id"`
	Source            string     `json:"source"`
	Enabled           bool       `json:"enabled"`
	IntervalSeconds   int        `json:"interval_seconds"`
	NextRunAt         *time.Time `json:"next_run_at"`
	State             string     `json:"state"`
	BlockingReason    string     `json:"blocking_reason"`
	VisibilityReasons []string   `json:"visibility_reasons"`
}
type IntelligenceGroupStatus struct {
	ChannelID         string                   `json:"channel_id"`
	ChannelName       string                   `json:"channel_name"`
	PlatformID        string                   `json:"platform_id"`
	PlatformName      string                   `json:"platform_name"`
	VisibilityReasons []string                 `json:"visibility_reasons"`
	Plans             []IntelligencePlanStatus `json:"plans"`
}
type IntelligenceStatusResponse struct {
	UpdatedAt    time.Time                 `json:"updated_at"`
	WorkerOnline bool                      `json:"worker_online"`
	Maintenance  bool                      `json:"maintenance"`
	Groups       []IntelligenceGroupStatus `json:"groups"`
}

type AdminJobView struct {
	Job
	RequestedModel    string   `json:"requested_model"`
	ChannelName       string   `json:"channel_name"`
	ContextPolicy     string   `json:"context_policy"`
	VisibilityReasons []string `json:"visibility_reasons"`
}
