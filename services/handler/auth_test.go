package handler_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"vocallabs.com/pandora/services/auth"
)

func TestOAuthLoginFlow(t *testing.T) {
	a := newApp(t, nil, false)

	c := browser()
	r, _ := c.Get(a.url + "/auth/github/login")
	loc, _ := url.Parse(r.Header.Get("Location"))
	q := loc.Query()
	if r.StatusCode != 302 || loc.Path != "/login/oauth/authorize" || q.Get("client_id") != "cid" ||
		q.Get("redirect_uri") != "http://pandora.test/auth/github/callback" || len(q.Get("state")) < 32 {
		t.Fatalf("authorize redirect: %v", loc)
	}
	var stateCookie *http.Cookie
	for _, ck := range r.Cookies() {
		if ck.Name == "pandora_oauth_state" {
			stateCookie = ck
		}
	}
	if stateCookie == nil || !stateCookie.HttpOnly || stateCookie.Value != q.Get("state") {
		t.Fatalf("state cookie: %+v", stateCookie)
	}

	// wrong state: no session
	r, _ = c.Get(a.url + "/auth/github/callback?code=c-alice&state=forged")
	if r.Header.Get("Location") != "/dashboard/?error=invalid_state" {
		t.Fatalf("forged state: %v", r.Header.Get("Location"))
	}
	if code, _ := call(t, c, "GET", a.url+"/auth/me", nil); code != 401 {
		t.Fatalf("no session expected: %d", code)
	}
	// no state cookie at all (callback hit directly)
	r, _ = browser().Get(a.url + "/auth/github/callback?code=c-alice&state=x")
	if r.Header.Get("Location") != "/dashboard/?error=invalid_state" {
		t.Fatalf("no cookie: %v", r.Header.Get("Location"))
	}
	// user denied on GitHub
	r, _ = c.Get(a.url + "/auth/github/callback?error=access_denied")
	if r.Header.Get("Location") != "/dashboard/?error=access_denied" {
		t.Fatalf("denied: %v", r.Header.Get("Location"))
	}
	// bad code
	c2 := browser()
	r, _ = c2.Get(a.url + "/auth/github/login")
	st := mustState(r)
	r, _ = c2.Get(a.url + "/auth/github/callback?code=nope&state=" + st)
	if r.Header.Get("Location") != "/dashboard/?error=github_failed" {
		t.Fatalf("bad code: %v", r.Header.Get("Location"))
	}

	// happy path: new account is pending and carries GitHub metadata
	alice := signIn(t, a, "alice")
	code, body := call(t, alice, "GET", a.url+"/auth/me", nil)
	me := asMap(body)
	if code != 200 || me["role"] != "pending" || me["github_login"] != "alice" || me["name"] != "Alice A" || me["email"] != "alice@x.com" {
		t.Fatalf("me: %d %v", code, me)
	}
	if code, body := call(t, alice, "GET", a.url+"/api/v1/me/prs", nil); code != 403 || !strings.Contains(string(body), "awaiting approval") {
		t.Fatalf("pending must be blocked: %d %s", code, body)
	}
	// signing in again reuses the same account
	alice2 := signIn(t, a, "alice")
	_, body = call(t, alice2, "GET", a.url+"/auth/me", nil)
	if asMap(body)["id"] != me["id"] {
		t.Fatal("second login must not create a duplicate account")
	}
	// private email falls back to /user/emails
	_, body = call(t, signIn(t, a, "carol"), "GET", a.url+"/auth/me", nil)
	if asMap(body)["email"] != "carol@private.com" {
		t.Fatalf("carol email: %v", asMap(body))
	}
	// logout ends the session
	if code, _ := call(t, alice, "POST", a.url+"/auth/logout", nil); code != 200 {
		t.Fatalf("logout: %d", code)
	}
	if code, _ := call(t, alice, "GET", a.url+"/auth/me", nil); code != 401 {
		t.Fatalf("after logout: %d", code)
	}
}

func mustState(r *http.Response) string {
	loc, _ := url.Parse(r.Header.Get("Location"))
	return loc.Query().Get("state")
}

func TestBootstrapAdminAndPromotion(t *testing.T) {
	a := newApp(t, nil, false) // BootstrapAdmin = "boss" (GitHub login is "Boss": case-insensitive)
	boss := signIn(t, a, "boss")
	_, body := call(t, boss, "GET", a.url+"/auth/me", nil)
	if asMap(body)["role"] != "admin" {
		t.Fatalf("bootstrap admin: %v", asMap(body))
	}

	bob := signIn(t, a, "bob")
	_, body = call(t, bob, "GET", a.url+"/auth/me", nil)
	bobID := int(asMap(body)["id"].(float64))
	if asMap(body)["role"] != "pending" {
		t.Fatalf("others start pending: %v", asMap(body))
	}
	if code, _ := call(t, bob, "GET", a.url+"/api/v1/admin/users", nil); code != 403 {
		t.Fatalf("pending must not list users: %d", code)
	}

	// pending users appear first for the admin, who approves them
	_, body = call(t, boss, "GET", a.url+"/api/v1/admin/users", nil)
	us := asList(body)
	if len(us) != 2 || us[0]["role"] != "pending" {
		t.Fatalf("users: %v", us)
	}
	if code, _ := call(t, boss, "PATCH", a.url+"/api/v1/admin/users/"+itoa(bobID), map[string]any{"role": "user"}); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	if code, _ := call(t, bob, "GET", a.url+"/api/v1/me/prs", nil); code != 200 {
		t.Fatalf("approved user: %d", code)
	}
	// but a user still cannot reach admin features or promote themselves
	if code, _ := call(t, bob, "PATCH", a.url+"/api/v1/admin/users/"+itoa(bobID), map[string]any{"role": "admin"}); code != 403 {
		t.Fatalf("privilege escalation: %d", code)
	}
	if code, _ := call(t, bob, "GET", a.url+"/api/v1/admin/stats", nil); code != 403 {
		t.Fatalf("stats for user: %d", code)
	}
	// promote to admin, and validation
	call(t, boss, "PATCH", a.url+"/api/v1/admin/users/"+itoa(bobID), map[string]any{"role": "admin"})
	if code, _ := call(t, bob, "GET", a.url+"/api/v1/admin/stats", nil); code != 200 {
		t.Fatalf("promoted admin: %d", code)
	}
	if code, _ := call(t, boss, "PATCH", a.url+"/api/v1/admin/users/"+itoa(bobID), map[string]any{"role": "superuser"}); code != 400 {
		t.Fatalf("invalid role: %d", code)
	}
	bossID := int(asMap(mustBody(call(t, boss, "GET", a.url+"/auth/me", nil)))["id"].(float64))
	if code, _ := call(t, boss, "PATCH", a.url+"/api/v1/admin/users/"+itoa(bossID), map[string]any{"role": "user"}); code != 400 {
		t.Fatalf("self-demote: %d", code)
	}
}

func mustBody(_ int, b []byte) []byte { return b }

func TestSessionSecurity(t *testing.T) {
	a := newApp(t, nil, false)
	boss := signIn(t, a, "boss")
	alice := signIn(t, a, "alice")
	_, body := call(t, alice, "GET", a.url+"/auth/me", nil)
	aliceID := int(asMap(body)["id"].(float64))

	// state-changing calls need the CSRF header even with a valid cookie
	req, _ := http.NewRequest("PATCH", a.url+"/api/v1/admin/users/"+itoa(aliceID), strings.NewReader(`{"role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := boss.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("csrf: %d", resp.StatusCode)
	}
	resp, _ = boss.Post(a.url+"/api/v1/admin/sync", "application/json", nil)
	if resp.StatusCode != 403 {
		t.Fatalf("csrf on sync: %d", resp.StatusCode)
	}

	// forged and expired cookies
	forged, _, _ := auth.NewManager("another-secret-another-secret-12345", time.Hour).Issue(1)
	expired, _, _ := auth.NewManager(jwtSecret, -time.Minute).Issue(1)
	for name, tok := range map[string]string{"forged": forged, "expired": expired, "garbage": "x.y.z"} {
		req, _ := http.NewRequest("GET", a.url+"/auth/me", nil)
		req.AddCookie(&http.Cookie{Name: "pandora_session", Value: tok})
		if r, _ := http.DefaultClient.Do(req); r.StatusCode != 401 {
			t.Fatalf("%s token accepted: %d", name, r.StatusCode)
		}
	}

	// deactivation applies immediately, and blocks signing in again
	if code, _ := call(t, boss, "PATCH", a.url+"/api/v1/admin/users/"+itoa(aliceID), map[string]any{"active": false}); code != 200 {
		t.Fatalf("deactivate: %d", code)
	}
	if code, _ := call(t, alice, "GET", a.url+"/auth/me", nil); code != 401 {
		t.Fatalf("deactivated session must die: %d", code)
	}
	c := browser()
	r, _ := c.Get(a.url + "/auth/github/login")
	r, _ = c.Get(a.url + "/auth/github/callback?code=c-alice&state=" + mustState(r))
	if r.Header.Get("Location") != "/dashboard/?error=account_disabled" {
		t.Fatalf("deactivated login: %v", r.Header.Get("Location"))
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
