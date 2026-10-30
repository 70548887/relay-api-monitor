// Package backups owns administrator-only encrypted backup orchestration.
package backups

import (
	"errors"
	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/store"
	"time"
)

type Schedule = domain.BackupSchedule
type Storage = domain.BackupStorage
type Run = domain.BackupRun
type StorageInput struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	ForcePathStyle  bool   `json:"force_path_style"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}
type Snapshot struct {
	Warnings    []string    `json:"reconciliation_warnings,omitempty"`
	Schedule    Schedule    `json:"schedule"`
	Storage     *Storage    `json:"storage,omitempty"`
	Active      *Run        `json:"active,omitempty"`
	LastSuccess *Run        `json:"last_success,omitempty"`
	Records     []Run       `json:"records"`
	NextRuns    []time.Time `json:"next_runs"`
	BackupDir   string      `json:"backup_dir"`
}
type Options struct {
	Store                                    *store.Store
	Vault                                    *security.Vault
	DataDir, BackupDir, BuildVersion, Commit string
	Enter                                    func() (func(), bool)
	AllowedStorageHosts                      []string
}

var ErrBusy = errors.New("已有备份、恢复或升级操作正在执行")
var ErrFrozen = errors.New("恢复后需先核对备份索引")
var ErrKeyNotVerified = errors.New("请先设置恢复公钥并校验恢复私钥")
var ErrProtected = errors.New("备份受保护，请先明确解除保护")

func terminal(state string) bool {
	switch state {
	case "succeeded", "failed", "interrupted", "canceled", "skipped", "deleted", "unavailable":
		return true
	}
	return false
}
