package config

import (
	"strings"
	"testing"
)

func TestInlineCommentsAreIgnored(t *testing.T) {
	t.Setenv("PANDORA_WEBHOOK_SECRET", "abc   # same value as the workflow secret")
	t.Setenv("PANDORA_GITHUB_TOKEN", "tok")
	t.Setenv("PANDORA_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("PANDORA_REVIEW_PROVIDER", "mock")
	t.Setenv("PANDORA_SIGNUP_DOMAINS", "   # allowed signup email domains, comma list (empty = anyone)")
	t.Setenv("PANDORA_ALLOWED_REPOS", "# comma list")
	t.Setenv("PANDORA_JWT_TTL_HOURS", "12 # hours")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.WebhookSecret != "abc" {
		t.Fatalf("secret=%q", c.WebhookSecret)
	}
	if len(c.SignupDomains) != 0 || len(c.AllowedRepos) != 0 {
		t.Fatalf("blank settings must stay blank: %v %v", c.SignupDomains, c.AllowedRepos)
	}
	if c.JWTTTL.Hours() != 12 {
		t.Fatalf("ttl=%v", c.JWTTTL)
	}
}

func TestValuesWithHashKept(t *testing.T) {
	t.Setenv("PANDORA_WEBHOOK_SECRET", "ab#cd") // no whitespace before '#': part of the value
	t.Setenv("PANDORA_GITHUB_TOKEN", "tok")
	t.Setenv("PANDORA_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("PANDORA_REVIEW_PROVIDER", "mock")
	c, _ := Load()
	if c.WebhookSecret != "ab#cd" {
		t.Fatalf("secret=%q", c.WebhookSecret)
	}
}

func TestGitHubCredentialRules(t *testing.T) {
	base := func() {
		t.Setenv("PANDORA_WEBHOOK_SECRET", "s")
		t.Setenv("PANDORA_JWT_SECRET", "0123456789abcdef0123456789abcdef")
		t.Setenv("PANDORA_REVIEW_PROVIDER", "mock")
		t.Setenv("PANDORA_GITHUB_TOKEN", "")
		t.Setenv("PANDORA_GITHUB_APP_ID", "")
		t.Setenv("PANDORA_GITHUB_APP_PRIVATE_KEY", "")
	}
	base()
	if _, err := Load(); err == nil {
		t.Fatal("no credentials must fail")
	}
	base()
	t.Setenv("PANDORA_GITHUB_APP_ID", "123")
	if _, err := Load(); err == nil {
		t.Fatal("app id without key must fail")
	}
	base()
	t.Setenv("PANDORA_GITHUB_APP_ID", "123")
	t.Setenv("PANDORA_GITHUB_APP_PRIVATE_KEY", `-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----`)
	c, err := Load()
	if err != nil || c.GitHubAppID != "123" {
		t.Fatalf("app config should load: %v", err)
	}
	if !strings.Contains(c.GitHubAppKey, "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("key mangled by comment stripping: %q", c.GitHubAppKey)
	}
}

func TestOpenRouterKeyMisuseCaught(t *testing.T) {
	t.Setenv("PANDORA_WEBHOOK_SECRET", "s")
	t.Setenv("PANDORA_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("PANDORA_GITHUB_TOKEN", "t")
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
