package handler_test

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"vocallabs.com/pandora/services/auth"
	"vocallabs.com/pandora/services/config"
	"vocallabs.com/pandora/services/store"
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

func userID(t *testing.T, a *testApp, c *http.Client) int {
	t.Helper()
	_, body := call(t, c, "GET", a.url+"/auth/me", nil)
	return int(asMap(body)["id"].(float64))
}

func patch(t *testing.T, a *testApp, c *http.Client, id int, body map[string]any) int {
	t.Helper()
	code, _ := call(t, c, "PATCH", a.url+"/api/v1/admin/users/"+itoa(id), body)
	return code
}

func roleOf(t *testing.T, a *testApp, id int) string {
	t.Helper()
	u, err := a.st.GetUser(context.Background(), int64(id))
	if err != nil {
		return "(deleted)"
	}
	return u.Role
}

func TestSuperAdminAndPromotion(t *testing.T) {
	a := newApp(t, nil, false) // BootstrapAdmin = "boss" (GitHub login is "Boss": case-insensitive)
	boss := signIn(t, a, "boss")
	_, body := call(t, boss, "GET", a.url+"/auth/me", nil)
	if asMap(body)["role"] != "superadmin" {
		t.Fatalf("bootstrap login must be super-admin: %v", asMap(body))
	}
	bossID := userID(t, a, boss)

	alice, bob, carol := signIn(t, a, "alice"), signIn(t, a, "bob"), signIn(t, a, "carol")
	aliceID, bobID, carolID := userID(t, a, alice), userID(t, a, bob), userID(t, a, carol)
	if roleOf(t, a, bobID) != "pending" {
		t.Fatal("others start pending")
	}
	if code, _ := call(t, bob, "GET", a.url+"/api/v1/admin/users", nil); code != 403 {
		t.Fatalf("pending must not list users: %d", code)
	}

	// the superadmin approves bob, then makes him admin; a plain user still has no admin access
	if patch(t, a, boss, bobID, map[string]any{"role": "user"}) != 200 || roleOf(t, a, bobID) != "user" {
		t.Fatal("approve")
	}
	if code := patch(t, a, bob, bobID, map[string]any{"role": "admin"}); code != 403 {
		t.Fatalf("a user must not promote themselves: %d", code)
	}
	if patch(t, a, boss, bobID, map[string]any{"role": "admin"}) != 200 {
		t.Fatal("promote bob")
	}

	// bob is a plain admin now. He CAN: approve pending -> user, promote user -> admin.
	if patch(t, a, bob, aliceID, map[string]any{"role": "user"}) != 200 || roleOf(t, a, aliceID) != "user" {
		t.Fatal("admin approves pending -> user")
	}
	if patch(t, a, bob, aliceID, map[string]any{"role": "admin"}) != 200 || roleOf(t, a, aliceID) != "admin" {
		t.Fatal("admin promotes user -> admin")
	}
	// He CANNOT: demote admins, push anyone back to pending, skip steps, touch active/delete, or touch the superadmin.
	for name, tc := range map[string]struct {
		target int
		body   map[string]any
	}{
		"demote admin to user":       {aliceID, map[string]any{"role": "user"}},
		"demote admin to pending":    {aliceID, map[string]any{"role": "pending"}},
		"deactivate admin":           {aliceID, map[string]any{"active": false}},
		"pending straight to admin":  {carolID, map[string]any{"role": "admin"}},
		"deactivate pending account": {carolID, map[string]any{"active": false}},
		"demote the superadmin":      {bossID, map[string]any{"role": "user"}},
		"deactivate the superadmin":  {bossID, map[string]any{"active": false}},
		"edit himself":               {bobID, map[string]any{"role": "user"}},
	} {
		if code := patch(t, a, bob, tc.target, tc.body); code != 403 && code != 400 {
			t.Fatalf("admin must be refused (%s): %d", name, code)
		}
	}
	if roleOf(t, a, aliceID) != "admin" || roleOf(t, a, carolID) != "pending" || roleOf(t, a, bossID) != "superadmin" {
		t.Fatal("refused changes must not have been applied")
	}
	// moving user -> pending is a demotion too
	patch(t, a, bob, carolID, map[string]any{"role": "user"})
	if code := patch(t, a, bob, carolID, map[string]any{"role": "pending"}); code != 403 {
		t.Fatalf("admin cannot send a user back to pending: %d", code)
	}
	if code, _ := call(t, bob, "DELETE", a.url+"/api/v1/admin/users/"+itoa(carolID), nil); code != 403 || roleOf(t, a, carolID) == "(deleted)" {
		t.Fatalf("admin must not delete accounts: %d", code)
	}
	if code, _ := call(t, bob, "DELETE", a.url+"/api/v1/admin/users/"+itoa(bossID), nil); code != 403 {
		t.Fatalf("nobody deletes the superadmin: %d", code)
	}

	// The permissions the dashboard receives match what the API enforces.
	_, lb := call(t, bob, "GET", a.url+"/api/v1/admin/users", nil)
	for _, u := range asList(lb) {
		p := u["perms"].(map[string]any)
		switch u["github_login"] {
		case "alice": // another admin: nothing
			if len(p["roles"].([]any)) != 0 || p["active"] != false || p["delete"] != false {
				t.Fatalf("perms for an admin target: %v", p)
			}
		case "carol": // user: may be promoted
			if r := p["roles"].([]any); len(r) != 1 || r[0] != "admin" || p["delete"] != false {
				t.Fatalf("perms for a user target: %v", p)
			}
		case "Boss":
			if len(p["roles"].([]any)) != 0 || p["delete"] != false {
				t.Fatalf("perms for the superadmin: %v", p)
			}
		}
	}

	// The superadmin CAN do all of it.
	if patch(t, a, boss, aliceID, map[string]any{"role": "user"}) != 200 || roleOf(t, a, aliceID) != "user" {
		t.Fatal("superadmin demotes admin -> user")
	}
	patch(t, a, boss, aliceID, map[string]any{"role": "admin"})
	if patch(t, a, boss, aliceID, map[string]any{"role": "pending"}) != 200 || roleOf(t, a, aliceID) != "pending" {
		t.Fatal("superadmin sends an admin back to pending")
	}
	if patch(t, a, boss, aliceID, map[string]any{"active": false}) != 200 {
		t.Fatal("superadmin deactivates")
	}
	if code := patch(t, a, boss, aliceID, map[string]any{"role": "superadmin"}); code != 400 {
		t.Fatalf("superadmin cannot be assigned: %d", code)
	}
	if code := patch(t, a, boss, bossID, map[string]any{"role": "user"}); code != 400 && code != 403 {
		t.Fatalf("superadmin cannot demote self: %d", code)
	}
	if code, _ := call(t, boss, "DELETE", a.url+"/api/v1/admin/users/"+itoa(bossID), nil); code != 400 && code != 403 {
		t.Fatalf("superadmin cannot delete self: %d", code)
	}

	// Delete: the account row goes, its session stops working, and PR data is untouched.
	postEvent(t, a, "pull_request", pr{action: "review_requested", number: 30, sha: "s", body: "b", author: "bob", draft: true}.event(), true)
	if code, _ := call(t, boss, "DELETE", a.url+"/api/v1/admin/users/"+itoa(bobID), nil); code != 200 {
		t.Fatalf("superadmin deletes: %d", code)
	}
	if roleOf(t, a, bobID) != "(deleted)" {
		t.Fatal("row should be gone")
	}
	if code, _ := call(t, bob, "GET", a.url+"/auth/me", nil); code != 401 {
		t.Fatalf("deleted user's session must die: %d", code)
	}
	if prs, _ := a.st.ListPRs(context.Background(), store.PRFilter{Author: "bob"}); len(prs) != 1 {
		t.Fatal("deleting a login must not delete PR history")
	}
	if code, _ := call(t, boss, "DELETE", a.url+"/api/v1/admin/users/99999", nil); code != 404 {
		t.Fatalf("unknown user: %d", code)
	}
	// signing in again simply starts a fresh pending account
	bob2 := signIn(t, a, "bob")
	if _, b := call(t, bob2, "GET", a.url+"/auth/me", nil); asMap(b)["role"] != "pending" {
		t.Fatalf("re-signup: %v", asMap(b))
	}
}

func TestFirstSignInIsSuperAdminWhenUnconfigured(t *testing.T) {
	a := newApp(t, func(c *config.Config) { c.BootstrapAdmin = "" }, false)
	first, second := signIn(t, a, "alice"), signIn(t, a, "bob")
	_, b1 := call(t, first, "GET", a.url+"/auth/me", nil)
	_, b2 := call(t, second, "GET", a.url+"/auth/me", nil)
	if asMap(b1)["role"] != "superadmin" || asMap(b2)["role"] != "pending" {
		t.Fatalf("first=%v second=%v", asMap(b1)["role"], asMap(b2)["role"])
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
