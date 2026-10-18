package deepseek

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	BaseURL  string
	Model    string
	APIKey   string
	Thinking string
	Timeout  time.Duration // zero defaults to 120s; maximum 120s
}

// ConfigFromEnv runs after config.Load has optionally loaded the explicit
// dotenv path. It does not read files, log values or choose a historical model.
func ConfigFromEnv() Config {
	return Config{
		BaseURL: os.Getenv("DEEPSEEK_BASE_URL"), Model: os.Getenv("DEEPSEEK_MODEL"),
		APIKey: os.Getenv("DEEPSEEK_API_KEY"), Thinking: os.Getenv("DEEPSEEK_THINKING"),
	}
}

func (c Config) validated() (Config, string, error) {
	if c.BaseURL == "" {
		c.BaseURL = "https://api.deepseek.com"
	}
	if c.Thinking == "" {
		c.Thinking = "disabled"
	}
	if c.Timeout == 0 {
		c.Timeout = 120 * time.Second
	}
	if c.Timeout < 0 || c.Timeout > 120*time.Second {
		return c, "", errors.New("DEEPSEEK timeout must be positive and at most 120s")
	}
	if c.Thinking != "disabled" && c.Thinking != "enabled" {
		return c, "", errors.New("DEEPSEEK_THINKING must be disabled or enabled")
	}
	if strings.TrimSpace(c.Model) == "" || strings.ContainsAny(c.Model, "\r\n") {
		return c, "", errors.New("DEEPSEEK_MODEL is required")
	}
	if strings.TrimSpace(c.APIKey) == "" || strings.ContainsAny(c.APIKey, " \t\r\n") {
		return c, "", errors.New("DEEPSEEK_API_KEY is missing or invalid")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return c, "", errors.New("DEEPSEEK_BASE_URL must be an absolute URL without credentials, query or fragment")
	}
	// Plain HTTP is only for local integration endpoints. Never send the key
	// over plain HTTP to a remote host. Do not follow provider redirects.
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return c, "", errors.New("DEEPSEEK_BASE_URL requires HTTPS or literal loopback HTTP")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	u.RawPath = ""
	return c, u.String(), nil
}
