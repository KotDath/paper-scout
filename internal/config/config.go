// Package config loads server configuration without executing dotenv as shell code.
package config

import (
	"errors"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	ResearchMemoryEnabled bool
	HTTPAddr              string
	SQLitePath            string
	CacheDir              string
}

// Load reads only an explicitly selected dotenv file. Existing environment
// variables take precedence. Parser errors are sanitized because they may
// contain a secret-bearing input line.
func Load(envFile string) (Config, error) {
	if envFile != "" {
		if err := godotenv.Load(envFile); err != nil {
			return Config{}, errors.New("cannot load env file")
		}
	}
	memoryEnabled, err := strconv.ParseBool(value("RESEARCH_MEMORY_ENABLED", "true"))
	if err != nil {
		return Config{}, errors.New("RESEARCH_MEMORY_ENABLED must be boolean")
	}
	c := Config{
		ResearchMemoryEnabled: memoryEnabled,
		HTTPAddr:              value("HTTP_ADDR", "127.0.0.1:8080"),
		SQLitePath:            value("SQLITE_PATH", "data/paper-scout.sqlite"),
		CacheDir:              value("CACHE_DIR", "data/cache"),
	}
	host, port, err := net.SplitHostPort(c.HTTPAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return Config{}, errors.New("HTTP_ADDR must use a loopback IP and port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return Config{}, errors.New("HTTP_ADDR port must be between 0 and 65535")
	}
	if c.SQLitePath == "" || c.SQLitePath == ":memory:" || strings.HasPrefix(c.SQLitePath, "file:") {
		return Config{}, errors.New("SQLITE_PATH must be a filesystem path")
	}
	if c.CacheDir == "" {
		return Config{}, errors.New("CACHE_DIR must be a filesystem path")
	}
	return c, nil
}

func value(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
