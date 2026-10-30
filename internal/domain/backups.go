package domain

import "time"

type BackupStorage struct {
	ID               string              `json:"id"`
	Revision         int                 `json:"revision"`
	Endpoint         string              `json:"endpoint"`
	Region           string              `json:"region"`
	Bucket           string              `json:"bucket"`
	Prefix           string              `json:"prefix"`
	ForcePathStyle   bool                `json:"force_path_style"`
	AccessKeyMask    string              `json:"access_key_mask,omitempty"`
	SecretConfigured bool                `json:"secret_configured"`
	Health           BackupStorageHealth `json:"health"`
	CreatedAt        time.Time           `json:"created_at"`
}

type BackupSchedule struct {
	ActiveStorageID        string     `json:"active_storage_id,omitempty"`
	StorageSelectionSet    bool       `json:"storage_selection_set,omitempty"`
	ReconciliationWarnings []string   `json:"reconciliation_warnings,omitempty"`
	Enabled                bool       `json:"enabled"`
	Cron                   string     `json:"cron"`
	Timezone               string     `json:"timezone"`
	Mode                   string     `json:"mode"`
	RetainDays             int        `json:"retain_days"`
	RetainCount            int        `json:"retain_count"`
	MonthlyEnabled         bool       `json:"monthly_enabled"`
	MonthlyDays            []int      `json:"monthly_days"`
	MonthlyMonthEnd        bool       `json:"monthly_month_end"`
	MonthlyRetainCount     int        `json:"monthly_retain_count"`
	Recipient              string     `json:"recipient"`
	RecipientID            string     `json:"recipient_id"`
	RecipientVerified      bool       `json:"recipient_verified"`
	NextRunAt              *time.Time `json:"next_run_at,omitempty"`
	ReconciliationRequired bool       `json:"reconciliation_required"`
}

type BackupRun struct {
	CloudImported      bool            `json:"cloud_imported,omitempty"`
	ID                 string          `json:"id"`
	RequestID          string          `json:"request_id"`
	State              string          `json:"state"`
	Trigger            string          `json:"trigger"`
	ScheduledFor       *time.Time      `json:"scheduled_for,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	StartedAt          time.Time       `json:"started_at"`
	FinishedAt         *time.Time      `json:"finished_at,omitempty"`
	HeartbeatAt        time.Time       `json:"heartbeat_at"`
	Version            string          `json:"version"`
	DatabaseSchema     int             `json:"database_schema"`
	RecipientID        string          `json:"recipient_id"`
	FileName           string          `json:"file_name"`
	LocalPath          string          `json:"-"`
	SHA256             string          `json:"sha256,omitempty"`
	SizeBytes          int64           `json:"size_bytes"`
	TransferredBytes   int64           `json:"transferred_bytes"`
	LocalState         string          `json:"local_state"`
	RemoteState        string          `json:"remote_state"`
	StorageID          string          `json:"storage_id,omitempty"`
	ObjectKey          string          `json:"-"`
	Protected          bool            `json:"protected"`
	MonthlyArchive     bool            `json:"monthly_archive"`
	ArchiveDates       []string        `json:"archive_dates,omitempty"`
	ArchiveRetainCount int             `json:"archive_retain_count"`
	ArchiveReleased    bool            `json:"archive_released,omitempty"`
	PolicySnapshot     *BackupSchedule `json:"policy_snapshot,omitempty"`
	ExpiresAt          *time.Time      `json:"expires_at,omitempty"`
	ErrorCode          string          `json:"error_code,omitempty"`
	Message            string          `json:"message,omitempty"`
}

func DefaultBackupSchedule() BackupSchedule {
	return BackupSchedule{Cron: "0 2 * * *", Timezone: "Asia/Shanghai", Mode: "local", RetainDays: 14, RetainCount: 10, MonthlyDays: []int{1}}
}
