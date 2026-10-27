package domain

import "time"

type UserConnection struct {
	ID                    string     `json:"id"`
	UserID                string     `json:"user_id"`
	Username              string     `json:"username,omitempty"`
	Name                  string     `json:"name"`
	BaseURL               string     `json:"base_url"`
	Protocol              string     `json:"protocol"`
	DiscoveryProtocol     string     `json:"discovery_protocol"`
	ModelsPath            string     `json:"models_path"`
	GeneratePath          string     `json:"generate_path"`
	AnthropicVersion      string     `json:"anthropic_version"`
	ChatTokenField        string     `json:"chat_token_field"`
	EncryptedKey          string     `json:"-"`
	CredentialFingerprint string     `json:"-"`
	KeyMask               string     `json:"key_mask"`
	Revision              int        `json:"revision"`
	Disabled              bool       `json:"disabled"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	LastModelsAt          *time.Time `json:"last_models_at"`
	LastModelsCount       int        `json:"last_models_count"`
	LastModelsError       string     `json:"last_models_error"`
}

type OnlineTestRun struct {
	ID                 string     `json:"id"`
	UserID             string     `json:"user_id"`
	Username           string     `json:"username,omitempty"`
	ConnectionID       string     `json:"connection_id"`
	ConnectionRevision int        `json:"connection_revision"`
	ClientRequestID    string     `json:"client_request_id"`
	RequestHash        string     `json:"-"`
	ConnectionName     string     `json:"connection_name"`
	BaseURL            string     `json:"base_url"`
	Protocol           string     `json:"protocol"`
	ModelsPath         string     `json:"models_path"`
	GeneratePath       string     `json:"generate_path"`
	AnthropicVersion   string     `json:"anthropic_version"`
	ChatTokenField     string     `json:"chat_token_field"`
	Model              string     `json:"model"`
	ReturnedModel      string     `json:"returned_model"`
	Prompt             string     `json:"prompt,omitempty"`
	TemplateID         string     `json:"template_id"`
	TemplateVersion    int        `json:"template_version"`
	MaxTokens          int        `json:"max_tokens"`
	TimeoutSeconds     int        `json:"timeout_seconds"`
	ReasoningEffort    string     `json:"reasoning_effort"`
	ThinkingMode       string     `json:"thinking_mode"`
	ThinkingBudget     int        `json:"thinking_budget"`
	State              string     `json:"state"`
	Text               string     `json:"text,omitempty"`
	Code               string     `json:"code,omitempty"`
	Kind               string     `json:"kind"`
	PreviewStatus      string     `json:"preview_status"`
	Usage              Usage      `json:"usage"`
	HTTPStatus         int        `json:"http_status"`
	FirstTextMS        *int64     `json:"first_text_ms"`
	DurationMS         int64      `json:"duration_ms"`
	FinishReason       string     `json:"finish_reason"`
	ErrorCode          string     `json:"error_code"`
	ErrorMessage       string     `json:"error_message"`
	ProcessID          string     `json:"-"`
	HeartbeatAt        time.Time  `json:"heartbeat_at"`
	Deadline           time.Time  `json:"deadline"`
	CreatedAt          time.Time  `json:"created_at"`
	StartedAt          time.Time  `json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at"`
}

type OnlineTestFilter struct {
	UserID, ConnectionID, Model, TemplateID, State, From, To string
	Page, PageSize                                           int
}
type OnlineTestPage struct {
	Items    []OnlineTestRun `json:"items"`
	Total    int             `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
}
