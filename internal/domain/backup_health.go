package domain

import "time"

type BackupStorageHealth struct {
	CheckedAt        *time.Time `json:"checked_at,omitempty"`
	Status           string     `json:"status"`
	Message          string     `json:"message"`
	Operation        string     `json:"operation,omitempty"`
	Stage            string     `json:"stage,omitempty"`
	ErrorCode        string     `json:"error_code,omitempty"`
	DurationMS       int64      `json:"duration_ms,omitempty"`
	HTTPStatus       int        `json:"http_status,omitempty"`
	CleanupErrorCode string     `json:"cleanup_error_code,omitempty"`
	CleanupMessage   string     `json:"cleanup_message,omitempty"`
}

// BackupStorageTestResult contains only controlled diagnostic text. Credentials,
// URLs, object names and upstream response bodies never belong in this result.
type BackupStorageTestResult struct {
	RequestID        string `json:"request_id,omitempty"`
	TestScope        string `json:"test_scope,omitempty"`
	StorageID        string `json:"storage_id,omitempty"`
	StorageRevision  int    `json:"storage_revision,omitempty"`
	Status           string `json:"status"`
	Stage            string `json:"stage"`
	ErrorCode        string `json:"error_code,omitempty"`
	Message          string `json:"message"`
	DurationMS       int64  `json:"duration_ms"`
	HTTPStatus       int    `json:"http_status,omitempty"`
	CleanupErrorCode string `json:"cleanup_error_code,omitempty"`
	CleanupMessage   string `json:"cleanup_message,omitempty"`
}
