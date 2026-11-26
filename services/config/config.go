// Package config loads Pandora settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr          string
	DBPath        string
	WebhookSecret string // shared secret used to verify incoming webhooks (HMAC-SHA256)
	GitHubToken   string // token used to read PRs and post status/comments
	JWTSecret     string // signs login tokens (min 32 chars)
	JWTTTL        time.Duration
	SignupDomains map[string]bool // allowed signup email domains; empty = any
	AdminEmail    string          // if set, only this email becomes admin on signup; else the first user does
	AllowedRepos  map[string]bool

	Provider     string // anthropic | mock
	AnthropicKey string
	Model        string

	MaxDiffBytes  int
	ReviewTimeout time.Duration
}

func Load() (*Config, error) {
	c := &Config{
		Addr:          env("PANDORA_ADDR", ":8847"),
		DBPath:        env("PANDORA_DB_PATH", "./data/pandora.db"),
		WebhookSecret: os.Getenv("PANDORA_WEBHOOK_SECRET"),
		GitHubToken:   os.Getenv("PANDORA_GITHUB_TOKEN"),
		JWTSecret:     os.Getenv("PANDORA_JWT_SECRET"),
		JWTTTL:        time.Duration(envInt("PANDORA_JWT_TTL_HOURS", 24)) * time.Hour,
		AdminEmail:    strings.ToLower(strings.TrimSpace(os.Getenv("PANDORA_BOOTSTRAP_ADMIN_EMAIL"))),
		SignupDomains: map[string]bool{},
		Provider:      env("PANDORA_REVIEW_PROVIDER", "anthropic"),
		AnthropicKey:  os.Getenv("ANTHROPIC_API_KEY"),
		Model:         env("PANDORA_REVIEW_MODEL", "claude-sonnet-5-5"),
		MaxDiffBytes:  envInt("PANDORA_MAX_DIFF_BYTES", 200_000),
		ReviewTimeout: time.Duration(envInt("PANDORA_REVIEW_TIMEOUT_SEC", 300)) * time.Second,
		AllowedRepos:  map[string]bool{},
	}
	for _, r := range strings.Split(os.Getenv("PANDORA_ALLOWED_REPOS"), ",") {
		if r = strings.TrimSpace(r); r != "" {
			c.AllowedRepos[strings.ToLower(r)] = true
		}
	}

	for _, d := range strings.Split(os.Getenv("PANDORA_SIGNUP_DOMAINS"), ",") {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			c.SignupDomains[d] = true
		}
	}

	var errs []error
	if len(c.JWTSecret) < 32 {
		errs = append(errs, errors.New("PANDORA_JWT_SECRET is required (min 32 chars)"))
	}
	if c.WebhookSecret == "" {
		errs = append(errs, errors.New("PANDORA_WEBHOOK_SECRET is required"))
	}
	if c.GitHubToken == "" {
		errs = append(errs, errors.New("PANDORA_GITHUB_TOKEN is required"))
	}
	switch c.Provider {
	case "anthropic":
		if c.AnthropicKey == "" {
			errs = append(errs, errors.New("ANTHROPIC_API_KEY is required when provider is anthropic"))
		}
	case "mock":
	default:
		errs = append(errs, fmt.Errorf("unknown PANDORA_REVIEW_PROVIDER %q", c.Provider))
	}
	return c, errors.Join(errs...)
}

// RepoAllowed reports whether events from repo should be processed.
// An empty allowlist accepts every repo.
func (c *Config) RepoAllowed(repo string) bool {
	return len(c.AllowedRepos) == 0 || c.AllowedRepos[strings.ToLower(repo)]
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}
