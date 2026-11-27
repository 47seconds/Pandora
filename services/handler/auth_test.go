package handler_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"context"

	"vocallabs.com/pandora/services/auth"
	"vocallabs.com/pandora/services/config"
	"vocallabs.com/pandora/services/github"
	"vocallabs.com/pandora/services/handler"
	"vocallabs.com/pandora/services/review"
	"vocallabs.com/pandora/services/service"
	"vocallabs.com/pandora/services/store"
)

func authServer(t *testing.T, cfg *config.Config) string {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg.JWTSecret, cfg.JWTTTL = jwtSecret, time.Hour
	if cfg.SignupDomains == nil {
		cfg.SignupDomains = map[string]bool{}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(context.Background(), cfg, st, github.NewClient(github.StaticToken("x")), review.Mock{}, log)
	srv := httptest.NewServer(handler.New(cfg, st, svc, auth.NewManager(jwtSecret, time.Hour), log))
	t.Cleanup(srv.Close)
	return srv.URL
}

func call(t *testing.T, method, url, token string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAuthFlow(t *testing.T) {
	base := authServer(t, &config.Config{})

	code, admin := call(t, "POST", base+"/auth/signup", "", map[string]string{"email": "Boss@Example.com", "password": "password123"})
	if code != 201 || admin["user"].(map[string]any)["role"] != "admin" {
		t.Fatalf("first user should be admin: %d %v", code, admin)
	}
	adminTok := admin["token"].(string)
	if _, leak := admin["user"].(map[string]any)["password_hash"]; leak {
		t.Fatal("password hash leaked")
	}

	code, dev := call(t, "POST", base+"/auth/signup", "", map[string]string{"email": "dev@example.com", "password": "password123"})
	if code != 201 || dev["user"].(map[string]any)["role"] != "developer" {
		t.Fatalf("second user should be developer: %d %v", code, dev)
	}
	devTok := dev["token"].(string)
	devID := int(dev["user"].(map[string]any)["id"].(float64))

	if code, _ := call(t, "POST", base+"/auth/signup", "", map[string]string{"email": "boss@example.com", "password": "password123"}); code != 409 {
		t.Fatalf("duplicate email (case-insensitive): %d", code)
	}
	if code, _ := call(t, "POST", base+"/auth/signup", "", map[string]string{"email": "x@example.com", "password": "short"}); code != 400 {
		t.Fatalf("short password: %d", code)
	}
	if code, _ := call(t, "POST", base+"/auth/login", "", map[string]string{"email": "boss@example.com", "password": "wrong-password"}); code != 401 {
		t.Fatalf("bad password: %d", code)
	}
	if code, _ := call(t, "POST", base+"/auth/login", "", map[string]string{"email": "ghost@example.com", "password": "password123"}); code != 401 {
		t.Fatalf("unknown user: %d", code)
	}
	code, login := call(t, "POST", base+"/auth/login", "", map[string]string{"email": "dev@example.com", "password": "password123"})
	if code != 200 || login["token"] == "" {
		t.Fatalf("login: %d", code)
	}

	if code, _ := call(t, "GET", base+"/api/v1/prs", "", nil); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := call(t, "GET", base+"/api/v1/prs", "garbage", nil); code != 401 {
		t.Fatalf("bad token: %d", code)
	}
	if code, _ := call(t, "GET", base+"/api/v1/prs", devTok, nil); code != 200 {
		t.Fatalf("developer reads prs: %d", code)
	}
	if code, me := call(t, "GET", base+"/auth/me", devTok, nil); code != 200 || me["email"] != "dev@example.com" {
		t.Fatalf("me: %d %v", code, me)
	}

	// admin-only
	if code, _ := call(t, "GET", base+"/api/v1/users", devTok, nil); code != 403 {
		t.Fatalf("developer must not list users: %d", code)
	}
	if code, _ := call(t, "PATCH", base+"/api/v1/users/"+itoa(devID), devTok, map[string]any{"role": "admin"}); code != 403 {
		t.Fatalf("privilege escalation: %d", code)
	}
	if code, _ := call(t, "PATCH", base+"/api/v1/users/"+itoa(devID), adminTok, map[string]any{"role": "reviewer", "github_login": "devgh"}); code != 200 {
		t.Fatalf("admin update: %d", code)
	}
	if code, _ := call(t, "PATCH", base+"/api/v1/users/"+itoa(devID), adminTok, map[string]any{"role": "superuser"}); code != 400 {
		t.Fatalf("invalid role: %d", code)
	}
	if code, _ := call(t, "PATCH", base+"/api/v1/users/1", adminTok, map[string]any{"active": false}); code != 400 {
		t.Fatalf("self-deactivate: %d", code)
	}

	// deactivation takes effect immediately for existing tokens
	call(t, "PATCH", base+"/api/v1/users/"+itoa(devID), adminTok, map[string]any{"active": false})
	if code, _ := call(t, "GET", base+"/auth/me", devTok, nil); code != 401 {
		t.Fatalf("deactivated user's token must stop working: %d", code)
	}
	if code, _ := call(t, "POST", base+"/auth/login", "", map[string]string{"email": "dev@example.com", "password": "password123"}); code != 401 {
		t.Fatalf("deactivated login: %d", code)
	}
}

func TestSignupRestrictions(t *testing.T) {
	base := authServer(t, &config.Config{SignupDomains: map[string]bool{"vocallabs.ai": true}, AdminEmail: "cto@vocallabs.ai"})
	if code, _ := call(t, "POST", base+"/auth/signup", "", map[string]string{"email": "a@gmail.com", "password": "password123"}); code != 403 {
		t.Fatalf("domain not allowed: %d", code)
	}
	// first signup is NOT admin when a bootstrap admin email is configured
	_, first := call(t, "POST", base+"/auth/signup", "", map[string]string{"email": "a@vocallabs.ai", "password": "password123"})
	if first["user"].(map[string]any)["role"] != "developer" {
		t.Fatalf("expected developer: %v", first)
	}
	_, boss := call(t, "POST", base+"/auth/signup", "", map[string]string{"email": "cto@vocallabs.ai", "password": "password123"})
	if boss["user"].(map[string]any)["role"] != "admin" {
		t.Fatalf("bootstrap admin: %v", boss)
	}
}

func TestExpiredAndForgedTokens(t *testing.T) {
	base := authServer(t, &config.Config{})
	call(t, "POST", base+"/auth/signup", "", map[string]string{"email": "a@example.com", "password": "password123"})
	expired, _, _ := auth.NewManager(jwtSecret, -time.Minute).Issue(1)
	if code, _ := call(t, "GET", base+"/auth/me", expired, nil); code != 401 {
		t.Fatalf("expired: %d", code)
	}
	forged, _, _ := auth.NewManager("another-secret-another-secret-12345", time.Hour).Issue(1)
	if code, _ := call(t, "GET", base+"/auth/me", forged, nil); code != 401 {
		t.Fatalf("forged: %d", code)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestDashboardServed(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>pandora</html>"), 0o644)
	base := authServer(t, &config.Config{StaticDir: dir})

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := noRedirect.Get(base + "/")
	if err != nil || r.StatusCode != 302 || r.Header.Get("Location") != "/dashboard/" {
		t.Fatalf("root redirect: %v %v", err, r)
	}
	r, err = http.Get(base + "/dashboard/")
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("dashboard: %v %v", err, r)
	}
	if !strings.Contains(r.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatal("missing CSP")
	}
	b, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(b), "pandora") {
		t.Fatalf("body: %s", b)
	}
	// data stays protected even though the shell is public
	if code, _ := call(t, "GET", base+"/api/v1/prs", "", nil); code != 401 {
		t.Fatalf("api must stay protected: %d", code)
	}
}

func TestDashboardDisabledWhenMissing(t *testing.T) {
	base := authServer(t, &config.Config{StaticDir: filepath.Join(t.TempDir(), "nope")})
	if r, _ := http.Get(base + "/dashboard/"); r.StatusCode != 404 {
		t.Fatalf("want 404, got %d", r.StatusCode)
	}
}
