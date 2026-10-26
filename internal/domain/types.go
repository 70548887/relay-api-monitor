package domain

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

const ChinesePrompt = "创建一个HTML，内容是SVG绘一个鹈鹕骑自行车的2D动画"
const EnglishPrompt = "Create code for an SVG of a pelican riding a bicycle as nicely as you can."

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

type Platform struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Public      bool      `json:"public"`
	Archived    bool      `json:"archived"`
	Sort        int       `json:"sort"`
	CreatedAt   time.Time `json:"created_at"`
}

type Channel struct {
	ID                        string     `json:"id"`
	PlatformID                string     `json:"platform_id"`
	Name                      string     `json:"name"`
	BaseURL                   string     `json:"base_url"`
	EncryptedKey              string     `json:"-"`
	KeyMask                   string     `json:"key_mask"`
	DiscoveryProtocol         string     `json:"discovery_protocol"`
	Protocol                  string     `json:"protocol"`
	ModelsPath                string     `json:"models_path"`
	GeneratePath              string     `json:"generate_path"`
	AnthropicVersion          string     `json:"anthropic_version"`
	ChatTokenField            string     `json:"chat_token_field"`
	ReasoningEffort           string     `json:"reasoning_effort"`
	ThinkingMode              string     `json:"thinking_mode"`
	ThinkingBudget            int        `json:"thinking_budget"`
	IntelligenceModelID       string     `json:"intelligence_model_id"`
	GroupProvider             string     `json:"group_provider"`
	GroupPath                 string     `json:"group_path"`
	GroupField                string     `json:"group_field"`
	VerifiedGroup             string     `json:"verified_group"`
	GroupVerificationStatus   string     `json:"group_verification_status"`
	GroupVerifiedAt           *time.Time `json:"group_verified_at"`
	GroupVerificationError    string     `json:"group_verification_error"`
	GroupVerificationRevision int        `json:"group_verification_revision"`
	Enabled                   bool       `json:"enabled"`
	Public                    bool       `json:"public"`
	Archived                  bool       `json:"archived"`
	Sort                      int        `json:"sort"`
	Revision                  int        `json:"revision"`
	LastSyncAt                *time.Time `json:"last_sync_at"`
	SyncError                 string     `json:"sync_error"`
	CreatedAt                 time.Time  `json:"created_at"`
}

type Model struct {
	ID                      string    `json:"id"`
	ChannelID               string    `json:"channel_id"`
	UpstreamID              string    `json:"upstream_id"`
	DisplayName             string    `json:"display_name"`
	Source                  string    `json:"source"`
	Public                  bool      `json:"public"`
	Archived                bool      `json:"archived"`
	Missing                 bool      `json:"missing"`
	Protocol                string    `json:"protocol"`
	GeneratePath            string    `json:"generate_path"`
	MaxTokens               int       `json:"max_tokens"`
	ReasoningEffort         string    `json:"reasoning_effort"`
	ThinkingMode            string    `json:"thinking_mode"`
	ThinkingBudget          int       `json:"thinking_budget"`
	ProbeEnabled            bool      `json:"probe_enabled"`
	ArtifactChinese         bool      `json:"artifact_chinese"`
	ArtifactEnglish         bool      `json:"artifact_english"`
	ProbeIntervalSeconds    int       `json:"probe_interval_seconds"`
	ArtifactIntervalSeconds int       `json:"artifact_interval_seconds"`
	TimeoutSeconds          int       `json:"timeout_seconds"`
	Sort                    int       `json:"sort"`
	CreatedAt               time.Time `json:"created_at"`
}

type PromptTemplate struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Prompt  string `json:"prompt"`
	Version int    `json:"version"`
}

type Settings struct {
	ProbeConcurrency            int    `json:"probe_concurrency"`
	ArtifactConcurrency         int    `json:"artifact_concurrency"`
	ChannelConcurrency          int    `json:"channel_concurrency"`
	ProbeIntervalSeconds        int    `json:"probe_interval_seconds"`
	ArtifactIntervalSeconds     int    `json:"artifact_interval_seconds"`
	ProbeTimeoutSeconds         int    `json:"probe_timeout_seconds"`
	ArtifactTimeoutSeconds      int    `json:"artifact_timeout_seconds"`
	ArtifactHistoryScope        string `json:"artifact_history_scope"`
	DiscoveryTimeoutSeconds     int    `json:"discovery_timeout_seconds"`
	SlowThresholdMS             int64  `json:"slow_threshold_ms"`
	RetentionDays               int    `json:"retention_days"`
	VisitorConcurrency          int    `json:"visitor_concurrency"`
	VisitorIPConcurrency        int    `json:"visitor_ip_concurrency"`
	VisitorModelsPerMinute      int    `json:"visitor_models_per_minute"`
	VisitorGenerationsPerMinute int    `json:"visitor_generations_per_minute"`
}

func DefaultSettings() Settings {
	return Settings{ProbeConcurrency: 4, ArtifactConcurrency: 2, ChannelConcurrency: 2, ProbeIntervalSeconds: 300, ArtifactIntervalSeconds: 1800, ProbeTimeoutSeconds: 60, ArtifactTimeoutSeconds: 600, ArtifactHistoryScope: "group", DiscoveryTimeoutSeconds: 20, SlowThresholdMS: 10000, RetentionDays: 30, VisitorConcurrency: 8, VisitorIPConcurrency: 2, VisitorModelsPerMinute: 5, VisitorGenerationsPerMinute: 10}
}

type Schedule struct {
	ID              string    `json:"id"`
	ModelID         string    `json:"model_id"`
	Kind            string    `json:"kind"`
	TemplateID      string    `json:"template_id"`
	IntervalSeconds int       `json:"interval_seconds"`
	NextRunAt       time.Time `json:"next_run_at"`
	Enabled         bool      `json:"enabled"`
}

type Job struct {
	ID              string     `json:"id"`
	ScheduleID      string     `json:"schedule_id"`
	ModelID         string     `json:"model_id"`
	ChannelID       string     `json:"channel_id"`
	Kind            string     `json:"kind"`
	TemplateID      string     `json:"template_id"`
	State           string     `json:"state"`
	ChannelRevision int        `json:"channel_revision"`
	Owner           string     `json:"owner"`
	LeaseToken      string     `json:"-"`
	LeaseUntil      *time.Time `json:"lease_until"`
	ScheduledAt     time.Time  `json:"scheduled_at"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	Deadline        *time.Time `json:"deadline"`
	CancelRequested bool       `json:"cancel_requested"`
	ErrorCode       string     `json:"error_code"`
	ErrorMessage    string     `json:"error_message"`
	Snapshot        string     `json:"snapshot"`
	ProgressChars   int64      `json:"progress_chars"`
	ProgressAt      *time.Time `json:"progress_at"`
	ReturnedModel   string     `json:"returned_model"`
}

type Usage struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	TotalTokens  *int64 `json:"total_tokens"`
}

type ProbeResult struct {
	ID            string    `json:"id"`
	JobID         string    `json:"job_id"`
	ModelID       string    `json:"model_id"`
	Status        string    `json:"status"`
	Protocol      string    `json:"protocol"`
	ReturnedModel string    `json:"returned_model"`
	HTTPStatus    int       `json:"http_status"`
	FirstTextMS   *int64    `json:"first_text_ms"`
	DurationMS    int64     `json:"duration_ms"`
	Usage         Usage     `json:"usage"`
	FinishReason  string    `json:"finish_reason"`
	ErrorCode     string    `json:"error_code"`
	ErrorMessage  string    `json:"error_message"`
	CreatedAt     time.Time `json:"created_at"`
}

type Artifact struct {
	ID                      string    `json:"id"`
	JobID                   string    `json:"job_id"`
	ModelID                 string    `json:"model_id"`
	ChannelID               string    `json:"channel_id"`
	TemplateID              string    `json:"template_id"`
	TemplateVersion         int       `json:"template_version"`
	Prompt                  string    `json:"prompt"`
	Protocol                string    `json:"protocol"`
	RequestedModel          string    `json:"requested_model"`
	ReturnedModel           string    `json:"returned_model"`
	Parameters              string    `json:"parameters"`
	ReasoningEffort         string    `json:"reasoning_effort"`
	ThinkingMode            string    `json:"thinking_mode"`
	ThinkingBudget          int       `json:"thinking_budget"`
	VerifiedGroup           string    `json:"verified_group"`
	GroupVerificationStatus string    `json:"group_verification_status"`
	RawResponse             string    `json:"raw_response"`
	Code                    string    `json:"code"`
	Kind                    string    `json:"kind"`
	Hidden                  bool      `json:"hidden"`
	FirstTextMS             *int64    `json:"first_text_ms"`
	DurationMS              int64     `json:"duration_ms"`
	Usage                   Usage     `json:"usage"`
	FinishReason            string    `json:"finish_reason"`
	StartedAt               time.Time `json:"started_at"`
	CreatedAt               time.Time `json:"created_at"`
}

type Admin struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	Disabled     bool   `json:"disabled"`
	CreatedAt    string `json:"created_at"`
}
type Session struct {
	TokenHash string
	AdminID   string
	CSRFToken string
	ExpiresAt time.Time
}
type Member struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	Disabled     bool   `json:"disabled"`
	CreatedAt    string `json:"created_at"`
}
type MemberSession struct {
	TokenHash string
	MemberID  string
	CSRFToken string
	ExpiresAt time.Time
}
type AuditEvent struct {
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	EntityID  string    `json:"entity_id"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}
type WorkerHealth struct {
	Owner       string    `json:"owner"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
	LeaseUntil  time.Time `json:"lease_until"`
	Online      bool      `json:"online"`
}
