package config

import (
	"errors"
	"fmt"
	"math"
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
	DataDir    string         `yaml:"data_dir" json:"data_dir"`
	Address    string         `yaml:"address" json:"address"`
	ConfigPath string         `yaml:"-" json:"-"`
	Update     UpdateConfig   `yaml:"update" json:"-"`
	Upstream   UpstreamConfig `yaml:"upstream" json:"-"`
}

type UpstreamConfig struct {
	DNSOverHTTPS string `yaml:"dns_over_https" json:"dns_over_https"`
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

// Decode scalars before interpolation so secret text cannot inject YAML fields.
type scalar string

func (s *scalar) UnmarshalYAML(decode func(interface{}) error) error {
	var value interface{}
	if err := decode(&value); err != nil {
		return err
	}
	switch value.(type) {
	case string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		*s = scalar(fmt.Sprint(value))
		return nil
	default:
		return errors.New("expected a scalar")
	}
}

type rawUpdate struct {
	Enabled       *scalar `yaml:"enabled"`
	GitHubOwner   *scalar `yaml:"github_owner"`
	GitHubRepo    *scalar `yaml:"github_repo"`
	GitHubToken   *scalar `yaml:"github_token"`
	CheckInterval *scalar `yaml:"check_interval"`
	Proxy         *scalar `yaml:"proxy"`
	AutoDownload  *scalar `yaml:"auto_download"`
	AutoRestart   *scalar `yaml:"auto_restart"`
	BackupDir     *scalar `yaml:"backup_dir"`
}
type rawConfig struct {
	Upstream struct {
		DNSOverHTTPS *scalar `yaml:"dns_over_https"`
	} `yaml:"upstream"`
	Server struct {
		Address *scalar `yaml:"address"`
		DataDir *scalar `yaml:"data_dir"`
	} `yaml:"server"`
	Address *scalar   `yaml:"address"`
	DataDir *scalar   `yaml:"data_dir"`
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
				return Config{}, errors.New("config: cannot inspect configuration file")
			}
		}
	}
	base := ""
	if path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return Config{}, errors.New("config: invalid configuration path")
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			return Config{}, errors.New("config: cannot read configuration file")
		}
		var raw rawConfig
		// Parser errors may quote literal credentials. Return a fixed diagnostic.
		if yaml.Unmarshal(b, &raw) != nil {
			return Config{}, errors.New("config: invalid YAML")
		}
		setString(&cfg.Address, raw.Server.Address, true)
		setString(&cfg.Address, raw.Address, true)
		setString(&cfg.DataDir, raw.Server.DataDir, false)
		setString(&cfg.DataDir, raw.DataDir, false)
		setString(&cfg.Upstream.DNSOverHTTPS, raw.Upstream.DNSOverHTTPS, true)
		if err := applyRawUpdate(&cfg.Update, raw.Update); err != nil {
			return Config{}, err
		}
		cfg.ConfigPath, base = abs, filepath.Dir(abs)
	}
	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}
	return normalize(cfg, base)
}

var envReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func interpolate(value string) string {
	return envReference.ReplaceAllStringFunc(value, func(match string) string { return os.Getenv(strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")) })
}
func setString(dst *string, value *scalar, trim bool) {
	if value == nil {
		return
	}
	v := interpolate(string(*value))
	if trim {
		v = strings.TrimSpace(v)
	}
	*dst = v
}
func parseBool(value, field string) (bool, error) {
	v, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("config: %s must be a boolean", field)
	}
	return v, nil
}
func parseInterval(value, field string) (time.Duration, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/int64(time.Second) {
		return 0, fmt.Errorf("config: %s must be a non-negative number of seconds within duration range", field)
	}
	return time.Duration(n) * time.Second, nil
}
func applyRawUpdate(dst *UpdateConfig, raw rawUpdate) error {
	for _, item := range []struct {
		value *scalar
		dst   *bool
		name  string
	}{
		{raw.Enabled, &dst.Enabled, "update.enabled"}, {raw.AutoDownload, &dst.AutoDownload, "update.auto_download"}, {raw.AutoRestart, &dst.AutoRestart, "update.auto_restart"},
	} {
		if item.value != nil {
			v, err := parseBool(interpolate(string(*item.value)), item.name)
			if err != nil {
				return err
			}
			*item.dst = v
		}
	}
	setString(&dst.GitHubOwner, raw.GitHubOwner, true)
	setString(&dst.GitHubRepo, raw.GitHubRepo, true)
	setString(&dst.GitHubToken, raw.GitHubToken, false)
	setString(&dst.Proxy, raw.Proxy, true)
	setString(&dst.BackupDir, raw.BackupDir, false)
	if raw.CheckInterval != nil {
		v, err := parseInterval(interpolate(string(*raw.CheckInterval)), "update.check_interval")
		if err != nil {
			return err
		}
		dst.CheckInterval = v
	}
	return nil
}
func applyEnv(cfg *Config) error {
	for _, item := range []struct {
		name string
		dst  *string
	}{
		{"RELAYLAB_UPSTREAM_DNS_OVER_HTTPS", &cfg.Upstream.DNSOverHTTPS},
		{"RELAYLAB_ADDR", &cfg.Address}, {"RELAYLAB_DATA_DIR", &cfg.DataDir}, {"RELAYLAB_UPDATE_GITHUB_OWNER", &cfg.Update.GitHubOwner}, {"RELAYLAB_UPDATE_GITHUB_REPO", &cfg.Update.GitHubRepo}, {"RELAYLAB_UPDATE_GITHUB_TOKEN", &cfg.Update.GitHubToken}, {"RELAYLAB_UPDATE_PROXY", &cfg.Update.Proxy}, {"RELAYLAB_UPDATE_BACKUP_DIR", &cfg.Update.BackupDir},
	} {
		if v := os.Getenv(item.name); v != "" {
			*item.dst = v
		}
	}
	for _, item := range []struct {
		name string
		dst  *bool
	}{
		{"RELAYLAB_UPDATE_ENABLED", &cfg.Update.Enabled}, {"RELAYLAB_UPDATE_AUTO_DOWNLOAD", &cfg.Update.AutoDownload}, {"RELAYLAB_UPDATE_AUTO_RESTART", &cfg.Update.AutoRestart},
	} {
		if value := os.Getenv(item.name); value != "" {
			v, err := parseBool(value, item.name)
			if err != nil {
				return err
			}
			*item.dst = v
		}
	}
	if value := os.Getenv("RELAYLAB_UPDATE_CHECK_INTERVAL"); value != "" {
		v, err := parseInterval(value, "RELAYLAB_UPDATE_CHECK_INTERVAL")
		if err != nil {
			return err
		}
		cfg.Update.CheckInterval = v
	}
	return nil
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
	var err error
	if cfg.DataDir, err = filepath.Abs(cfg.DataDir); err != nil {
		return Config{}, errors.New("config: invalid data directory")
	}
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
