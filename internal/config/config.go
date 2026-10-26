package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

type UpdateConfig struct {
	Enabled       bool          `yaml:"enabled" json:"enabled"`
	GitHubOwner   string        `yaml:"github_owner" json:"github_owner"`
	GitHubRepo    string        `yaml:"github_repo" json:"github_repo"`
	GitHubToken   string        `yaml:"-" json:"-"`
	CheckInterval time.Duration `yaml:"-" json:"-"`
	Proxy         string        `yaml:"-" json:"-"`
	AutoDownload  bool          `yaml:"auto_download" json:"auto_download"`
	AutoRestart   bool          `yaml:"auto_restart" json:"auto_restart"`
	BackupDir     string        `yaml:"backup_dir" json:"backup_dir"`
}

type Config struct {
	DataDir    string       `yaml:"data_dir" json:"data_dir"`
	Address    string       `yaml:"address" json:"address"`
	ConfigPath string       `yaml:"-" json:"-"`
	Update     UpdateConfig `yaml:"update" json:"-"`
}

func defaultUpdate() UpdateConfig {
	return UpdateConfig{Enabled: true, GitHubOwner: "70548887", GitHubRepo: "relay-api-monitor", AutoRestart: true, BackupDir: filepath.Join("data", "backups")}
}

func Default() Config {
	d := os.Getenv("RELAYLAB_DATA_DIR")
	if d == "" {
		d = "data"
	}
	a := os.Getenv("RELAYLAB_ADDR")
	if a == "" {
		a = "127.0.0.1:42783"
	}
	abs, _ := filepath.Abs(d)
	return Config{DataDir: abs, Address: a, Update: defaultUpdate()}
}

type rawUpdate struct {
	Enabled       *bool   `yaml:"enabled"`
	GitHubOwner   *string `yaml:"github_owner"`
	GitHubRepo    *string `yaml:"github_repo"`
	GitHubToken   *string `yaml:"github_token"`
	CheckInterval *int64  `yaml:"check_interval"`
	Proxy         *string `yaml:"proxy"`
	AutoDownload  *bool   `yaml:"auto_download"`
	AutoRestart   *bool   `yaml:"auto_restart"`
	BackupDir     *string `yaml:"backup_dir"`
}
type rawConfig struct {
	Server struct {
		Address *string `yaml:"address"`
		DataDir *string `yaml:"data_dir"`
	} `yaml:"server"`
	Address *string   `yaml:"address"`
	DataDir *string   `yaml:"data_dir"`
	Update  rawUpdate `yaml:"update"`
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = os.Getenv("RELAYLAB_CONFIG")
	}
	if path == "" {
		candidates := []string{"config.yaml"}
		if executable, err := os.Executable(); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(executable), "config.yaml"))
		}
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			} else if !os.IsNotExist(err) {
				return Config{}, err
			}
		}
	}
	if path == "" {
		candidates := []string{"config.yaml"}
		if exe, exeErr := os.Executable(); exeErr == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(exe), "config.yaml"))
		}
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			} else if !os.IsNotExist(err) {
				return Config{}, err
			}
		}
	}
	if path == "" {
		applyEnv(&cfg)
		return normalize(cfg, "")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return Config{}, err
	}
	var raw rawConfig
	if err = yaml.Unmarshal([]byte(interpolate(string(b))), &raw); err != nil {
		return Config{}, fmt.Errorf("config: invalid YAML: %w", err)
	}
	if raw.Server.Address != nil {
		cfg.Address = strings.TrimSpace(*raw.Server.Address)
	}
	if raw.Address != nil {
		cfg.Address = strings.TrimSpace(*raw.Address)
	}
	if raw.Server.DataDir != nil {
		cfg.DataDir = *raw.Server.DataDir
	}
	if raw.DataDir != nil {
		cfg.DataDir = *raw.DataDir
	}
	applyRawUpdate(&cfg.Update, raw.Update)
	applyEnv(&cfg)
	cfg.ConfigPath = abs
	return normalize(cfg, filepath.Dir(abs))
}

var envReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func interpolate(value string) string {
	return envReference.ReplaceAllStringFunc(value, func(match string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")
		return os.Getenv(name)
	})
}

func applyRawUpdate(dst *UpdateConfig, raw rawUpdate) {
	if raw.Enabled != nil {
		dst.Enabled = *raw.Enabled
	}
	if raw.GitHubOwner != nil {
		dst.GitHubOwner = strings.TrimSpace(*raw.GitHubOwner)
	}
	if raw.GitHubRepo != nil {
		dst.GitHubRepo = strings.TrimSpace(*raw.GitHubRepo)
	}
	if raw.GitHubToken != nil {
		dst.GitHubToken = *raw.GitHubToken
	}
	if raw.CheckInterval != nil {
		dst.CheckInterval = time.Duration(*raw.CheckInterval) * time.Second
	}
	if raw.Proxy != nil {
		dst.Proxy = strings.TrimSpace(*raw.Proxy)
	}
	if raw.AutoDownload != nil {
		dst.AutoDownload = *raw.AutoDownload
	}
	if raw.AutoRestart != nil {
		dst.AutoRestart = *raw.AutoRestart
	}
	if raw.BackupDir != nil {
		dst.BackupDir = *raw.BackupDir
	}
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("RELAYLAB_ADDR"); v != "" {
		cfg.Address = v
	}
	if v := os.Getenv("RELAYLAB_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	u := &cfg.Update
	if v := os.Getenv("RELAYLAB_UPDATE_ENABLED"); v != "" {
		if b, e := strconv.ParseBool(v); e == nil {
			u.Enabled = b
		}
	}
	if v := os.Getenv("RELAYLAB_UPDATE_GITHUB_OWNER"); v != "" {
		u.GitHubOwner = v
	}
	if v := os.Getenv("RELAYLAB_UPDATE_GITHUB_REPO"); v != "" {
		u.GitHubRepo = v
	}
	if v := os.Getenv("RELAYLAB_UPDATE_GITHUB_TOKEN"); v != "" {
		u.GitHubToken = v
	}
	if v := os.Getenv("RELAYLAB_UPDATE_CHECK_INTERVAL"); v != "" {
		if n, e := strconv.ParseInt(v, 10, 64); e == nil && n >= 0 {
			u.CheckInterval = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("RELAYLAB_UPDATE_PROXY"); v != "" {
		u.Proxy = v
	}
	if v := os.Getenv("RELAYLAB_UPDATE_AUTO_DOWNLOAD"); v != "" {
		if b, e := strconv.ParseBool(v); e == nil {
			u.AutoDownload = b
		}
	}
	if v := os.Getenv("RELAYLAB_UPDATE_AUTO_RESTART"); v != "" {
		if b, e := strconv.ParseBool(v); e == nil {
			u.AutoRestart = b
		}
	}
	if v := os.Getenv("RELAYLAB_UPDATE_BACKUP_DIR"); v != "" {
		u.BackupDir = v
	}
}

func normalize(cfg Config, base string) (Config, error) {
	if strings.TrimSpace(cfg.Address) == "" {
		return Config{}, errors.New("config: server address is empty")
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	if base != "" && !filepath.IsAbs(cfg.DataDir) {
		cfg.DataDir = filepath.Join(base, cfg.DataDir)
	}
	cfg.DataDir, _ = filepath.Abs(cfg.DataDir)
	if cfg.Update.BackupDir == "" {
		cfg.Update.BackupDir = filepath.Join(cfg.DataDir, "backups")
	} else if !filepath.IsAbs(cfg.Update.BackupDir) {
		root := base
		if root == "" {
			root = filepath.Dir(cfg.DataDir)
		}
		cfg.Update.BackupDir = filepath.Join(root, cfg.Update.BackupDir)
	}
	if cfg.Update.CheckInterval < 0 {
		return Config{}, errors.New("config: update.check_interval must be non-negative")
	}
	return cfg, nil
}
