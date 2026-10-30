package domain

import "time"

type BackupStorageHealth struct {
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	Status    string     `json:"status"`
	Message   string     `json:"message"`
	Operation string     `json:"operation,omitempty"`
}
