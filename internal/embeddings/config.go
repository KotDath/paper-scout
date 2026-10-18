package embeddings

import (
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BaseURL        string
	Model          string
	Dimensions     int
	ExpectedDigest string        // optional deployment pin; otherwise New pins the installed digest
	Timeout        time.Duration // per complete operation; defaults to 120s, maximum 120s
}

// ConfigFromEnv runs after the application's optional config.Load(envFile).
// It neither reads dotenv files nor logs configuration values.
func ConfigFromEnv() (Config, error) {
	c := Config{BaseURL: os.Getenv("OLLAMA_BASE_URL"), Model: os.Getenv("EMBEDDING_MODEL"), ExpectedDigest: os.Getenv("EMBEDDING_MODEL_DIGEST")}
	if value, set := os.LookupEnv("EMBEDDING_DIMENSIONS"); set {
		n, err := strconv.Atoi(value)
		if err != nil || n != Dimensions {
			return c, errors.New("EMBEDDING_DIMENSIONS must be 1024")
		}
		c.Dimensions = n
	}
	return c, nil
}

func (c Config) validated() (Config, error) {
	if c.BaseURL == "" {
		c.BaseURL = "http://127.0.0.1:11434"
	}
	if c.Model == "" {
		c.Model = Model
	}
	if c.Dimensions == 0 {
		c.Dimensions = Dimensions
	}
	if c.Timeout == 0 {
		c.Timeout = 120 * time.Second
	}
	if c.Model != Model || c.Dimensions != Dimensions {
		return c, errors.New("embeddings require qwen3-embedding:0.6b and 1024 dimensions")
	}
	if c.Timeout < 0 || c.Timeout > 120*time.Second {
		return c, errors.New("embedding timeout must be positive and at most 120s")
	}
	if c.ExpectedDigest != "" {
		digest, ok := validDigest(c.ExpectedDigest)
		if !ok {
			return c, errors.New("EMBEDDING_MODEL_DIGEST must be a SHA-256 digest")
		}
		c.ExpectedDigest = digest
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return c, errors.New("OLLAMA_BASE_URL must be a loopback HTTP origin")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return c, errors.New("OLLAMA_BASE_URL must be a loopback HTTP origin")
	}
	c.BaseURL = strings.TrimRight(u.String(), "/")
	return c, nil
}

func validDigest(value string) (string, bool) {
	value = strings.TrimPrefix(value, "sha256:")
	decoded, err := hex.DecodeString(value)
	return strings.ToLower(value), err == nil && len(decoded) == 32
}
