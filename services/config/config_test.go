package config

import (
	"strings"
	"testing"
)

// setValid sets a complete, valid environment; tests then break one thing at a time.
func setValid(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"PANDORA_PUBLIC_URL":             "https://pandora-dev.example.com/",
		"PANDORA_WEBHOOK_SECRET":         "s",
		"PANDORA_JWT_SECRET":             "0123456789abcdef0123456789abcdef",
		"PANDORA_GITHUB_CLIENT_ID":       "cid",
		"PANDORA_GITHUB_CLIENT_SECRET":   "csecret",
		"PANDORA_GITHUB_TOKEN":           "tok",
		"PANDORA_GITHUB_APP_ID":          "",
		"PANDORA_GITHUB_APP_PRIVATE_KEY": "",
		"PANDORA_REVIEW_PROVIDER":        "mock",
		"PANDORA_BOOTSTRAP_ADMIN_GITHUB": "",
		"PANDORA_BACKFILL_DAYS":          "",
		"PANDORA_ALLOWED_REPOS":          "",
		"PANDORA_REVIEW_MODEL":           "",
		"ANTHROPIC_API_KEY":              "",
		"OPENROUTER_API_KEY":             "",
	} {
		t.Setenv(k, v)
	}
}

func TestDefaultsAndDerivedValues(t *testing.T) {
	setValid(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicURL != "https://pandora-dev.example.com" || !c.CookieSecure {
		t.Fatalf("public url=%q secure=%v", c.PublicURL, c.CookieSecure)
	}
	if c.RedirectURL() != "https://pandora-dev.example.com/auth/github/callback" {
		t.Fatalf("redirect=%q", c.RedirectURL())
	}
	if c.BackfillDays != 90 {
		t.Fatalf("backfill default=%d", c.BackfillDays)
	}
	t.Setenv("PANDORA_BACKFILL_DAYS", "30")
	t.Setenv("PANDORA_PUBLIC_URL", "http://localhost:8847")
	c, _ = Load()
	if c.BackfillDays != 30 || c.CookieSecure {
		t.Fatalf("backfill=%d secure=%v", c.BackfillDays, c.CookieSecure)
	}
}

func TestRequiredSettings(t *testing.T) {
	for _, k := range []string{"PANDORA_PUBLIC_URL", "PANDORA_GITHUB_CLIENT_ID", "PANDORA_GITHUB_CLIENT_SECRET", "PANDORA_JWT_SECRET", "PANDORA_WEBHOOK_SECRET"} {
		setValid(t)
		t.Setenv(k, "")
		if _, err := Load(); err == nil {
			t.Fatalf("%s is required", k)
		}
	}
	setValid(t)
	t.Setenv("PANDORA_PUBLIC_URL", "not a url")
	if _, err := Load(); err == nil {
		t.Fatal("bad public url must fail")
	}
}

func TestInlineCommentsAreIgnored(t *testing.T) {
	setValid(t)
	t.Setenv("PANDORA_WEBHOOK_SECRET", "abc   # same value as the workflow secret")
	t.Setenv("PANDORA_BOOTSTRAP_ADMIN_GITHUB", "   # your github username")
	t.Setenv("PANDORA_ALLOWED_REPOS", "# comma list")
	t.Setenv("PANDORA_JWT_TTL_HOURS", "12 # hours")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.WebhookSecret != "abc" || c.BootstrapAdmin != "" || len(c.AllowedRepos) != 0 || c.JWTTTL.Hours() != 12 {
		t.Fatalf("%+v", c)
	}
	t.Setenv("PANDORA_WEBHOOK_SECRET", "ab#cd") // no whitespace before '#': part of the value
	if c, _ := Load(); c.WebhookSecret != "ab#cd" {
		t.Fatalf("secret=%q", c.WebhookSecret)
	}
}

func TestGitHubCredentialRules(t *testing.T) {
	setValid(t)
	t.Setenv("PANDORA_GITHUB_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("no credentials must fail")
	}
	t.Setenv("PANDORA_GITHUB_APP_ID", "123")
	if _, err := Load(); err == nil {
		t.Fatal("app id without key must fail")
	}
	t.Setenv("PANDORA_GITHUB_APP_PRIVATE_KEY", `-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----`)
	c, err := Load()
	if err != nil || c.GitHubAppID != "123" || !strings.Contains(c.GitHubAppKey, "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("app config: %v %q", err, c.GitHubAppKey)
	}
}

func TestOpenRouterKeyMisuseCaught(t *testing.T) {
	setValid(t)
	t.Setenv("PANDORA_REVIEW_PROVIDER", "anthropic")
	t.Setenv("ANTHROPIC_API_KEY", "sk-or-v1-abc")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OpenRouter") {
		t.Fatalf("want OpenRouter hint, got %v", err)
	}
	t.Setenv("PANDORA_REVIEW_PROVIDER", "openrouter")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-abc")
	c, err := Load()
	if err != nil || c.Model != "anthropic/claude-sonnet-5.5" {
		t.Fatalf("model=%q err=%v", c.Model, err)
	}
}
