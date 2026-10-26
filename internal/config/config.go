package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	DataDir string
	Address string
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
	return Config{abs, a}
}
