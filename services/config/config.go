// Package config loads Pandora settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr      string
	DBPath    string
	StaticDir string // dashboard build files, served at /dashboard/ ("" disables)

	PublicURL    string // external base URL, e.g. https://pandora-dev.vocallabs.ai (no trailing slash)
	CookieSecure bool   // derived: PublicURL is https

	WebhookSecret string // shared secret used to verify incoming webhooks (HMAC-SHA256)

	GitHubAppID  string // GitHub App id (preferred API auth)
	GitHubAppKey string // App private key, PEM (single-line with \n is fine)
	GitHubToken  string // personal access token: optional dev fallback when no App is configured

	OAuthClientID     string // GitHub App client id, for "Sign in with GitHub"
	OAuthClientSecret string
	BootstrapAdmin    string // GitHub login that is always an admin (so the first admin can exist)

	JWTSecret string // signs session tokens (min 32 chars)
	JWTTTL    time.Duration

	AllowedRepos map[string]bool

	Provider      string // anthropic | openrouter | mock
	AnthropicKey  string
	OpenRouterKey string
	Model         string

	MaxDiffBytes  int
	ReviewTimeout time.Duration
	BackfillDays  int // how far back the PR backfill reaches
}

func Load() (*Config, error) {
	c := &Config{
		Addr:              env("PANDORA_ADDR", ":8847"),
		DBPath:            env("PANDORA_DB_PATH", "./data/pandora.db"),
		StaticDir:         env("PANDORA_STATIC_DIR", "./static"),
		PublicURL:         strings.TrimRight(get("PANDORA_PUBLIC_URL"), "/"),
		WebhookSecret:     get("PANDORA_WEBHOOK_SECRET"),
		GitHubAppID:       get("PANDORA_GITHUB_APP_ID"),
		GitHubAppKey:      get("PANDORA_GITHUB_APP_PRIVATE_KEY"),
		GitHubToken:       get("PANDORA_GITHUB_TOKEN"),
		OAuthClientID:     get("PANDORA_GITHUB_CLIENT_ID"),
		OAuthClientSecret: get("PANDORA_GITHUB_CLIENT_SECRET"),
		BootstrapAdmin:    strings.TrimSpace(get("PANDORA_BOOTSTRAP_ADMIN_GITHUB")),
		JWTSecret:         get("PANDORA_JWT_SECRET"),
		JWTTTL:            time.Duration(envInt("PANDORA_JWT_TTL_HOURS", 24)) * time.Hour,
		Provider:          env("PANDORA_REVIEW_PROVIDER", "anthropic"),
		AnthropicKey:      get("ANTHROPIC_API_KEY"),
		OpenRouterKey:     get("OPENROUTER_API_KEY"),
		Model:             get("PANDORA_REVIEW_MODEL"),
		MaxDiffBytes:      envInt("PANDORA_MAX_DIFF_BYTES", 200_000),
		ReviewTimeout:     time.Duration(envInt("PANDORA_REVIEW_TIMEOUT_SEC", 300)) * time.Second,
		BackfillDays:      envInt("PANDORA_BACKFILL_DAYS", 90),
		AllowedRepos:      map[string]bool{},
	}
	for _, r := range strings.Split(get("PANDORA_ALLOWED_REPOS"), ",") {
		if r = strings.TrimSpace(r); r != "" {
			c.AllowedRepos[strings.ToLower(r)] = true
		}
	}

	var errs []error
	if u, err := url.Parse(c.PublicURL); err != nil || c.PublicURL == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, errors.New("PANDORA_PUBLIC_URL is required, e.g. https://pandora-dev.vocallabs.ai"))
	} else {
		c.CookieSecure = u.Scheme == "https"
	}
	if c.OAuthClientID == "" || c.OAuthClientSecret == "" {
		errs = append(errs, errors.New("PANDORA_GITHUB_CLIENT_ID and PANDORA_GITHUB_CLIENT_SECRET are required (GitHub login)"))
	}
	if len(c.JWTSecret) < 32 {
		errs = append(errs, errors.New("PANDORA_JWT_SECRET is required (min 32 chars)"))
	}
	if c.WebhookSecret == "" {
		errs = append(errs, errors.New("PANDORA_WEBHOOK_SECRET is required"))
	}
	if kf := get("PANDORA_GITHUB_APP_PRIVATE_KEY_FILE"); kf != "" && c.GitHubAppKey == "" {
		b, err := os.ReadFile(kf)
		if err != nil {
			errs = append(errs, fmt.Errorf("read PANDORA_GITHUB_APP_PRIVATE_KEY_FILE: %w", err))
		}
		c.GitHubAppKey = string(b)
	}
	if (c.GitHubAppID == "") != (c.GitHubAppKey == "") {
		errs = append(errs, errors.New("set both PANDORA_GITHUB_APP_ID and the App private key (PANDORA_GITHUB_APP_PRIVATE_KEY or _FILE)"))
	}
	if c.GitHubAppID == "" && c.GitHubToken == "" {
		errs = append(errs, errors.New("GitHub credentials required: configure the GitHub App (PANDORA_GITHUB_APP_ID + private key) or PANDORA_GITHUB_TOKEN"))
	}
	switch c.Provider {
	case "anthropic":
		if strings.HasPrefix(c.AnthropicKey, "sk-or-") {
			errs = append(errs, errors.New("ANTHROPIC_API_KEY looks like an OpenRouter key (sk-or-...); set PANDORA_REVIEW_PROVIDER=openrouter and OPENROUTER_API_KEY"))
		} else if c.AnthropicKey == "" {
			errs = append(errs, errors.New("ANTHROPIC_API_KEY is required when provider is anthropic"))
		}
		if c.Model == "" {
			c.Model = "claude-sonnet-5-5"
		}
	case "openrouter":
		if c.OpenRouterKey == "" {
			errs = append(errs, errors.New("OPENROUTER_API_KEY is required when provider is openrouter"))
		}
		if c.Model == "" {
			c.Model = "anthropic/claude-sonnet-5.5"
		}
	case "mock":
	default:
		errs = append(errs, fmt.Errorf("unknown PANDORA_REVIEW_PROVIDER %q", c.Provider))
	}
	return c, errors.Join(errs...)
}

// RedirectURL is the OAuth callback registered on the GitHub App.
func (c *Config) RedirectURL() string { return c.PublicURL + "/auth/github/callback" }

// RepoAllowed reports whether events from repo should be processed.
// An empty allowlist accepts every repo.
func (c *Config) RepoAllowed(repo string) bool {
	return len(c.AllowedRepos) == 0 || c.AllowedRepos[strings.ToLower(repo)]
}

// get reads an env var, ignoring a trailing "# comment". Some env-file parsers
// (docker run --env-file, older compose) keep inline comments as part of the value,
// which silently turns a blank setting into a non-empty one.
func get(k string) string {
	v := os.Getenv(k)
	if i := strings.IndexAny(v, " \t"); i >= 0 {
		if j := strings.Index(v[i:], "#"); j >= 0 && strings.TrimSpace(v[i:i+j]) == "" {
			v = v[:i]
		}
	}
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "#") {
		return ""
	}
	return v
}

func env(k, def string) string {
	if v := get(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(get(k)); err == nil && v > 0 {
		return v
	}
	return def
}
