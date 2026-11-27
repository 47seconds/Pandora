package handler_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"vocallabs.com/pandora/services/auth"
	"vocallabs.com/pandora/services/config"
	"vocallabs.com/pandora/services/github"
	"vocallabs.com/pandora/services/handler"
	"vocallabs.com/pandora/services/review"
	"vocallabs.com/pandora/services/service"
	"vocallabs.com/pandora/services/store"
)

const (
	whSecret  = "s3cret"
	jwtSecret = "0123456789abcdef0123456789abcdef"
)

// fakeGH plays both api.github.com and github.com (OAuth) for the tests.
type fakeGH struct {
	mu       sync.Mutex
	head     string
	body     string
	statuses []string
	comments int
}

var ghUsers = map[string]map[string]any{
	"alice": {"id": 101, "login": "alice", "name": "Alice A", "email": "alice@x.com", "avatar_url": "https://avatars.githubusercontent.com/u/101"},
	"bob":   {"id": 102, "login": "bob", "name": "Bob B", "email": "bob@x.com", "avatar_url": "https://avatars.githubusercontent.com/u/102"},
	"boss":  {"id": 103, "login": "Boss", "name": "The Boss", "email": "boss@x.com", "avatar_url": ""},
	"carol": {"id": 104, "login": "carol", "name": "Carol C", "email": "", "avatar_url": ""}, // private email
}

func (f *fakeGH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/login/oauth/access_token":
		r.ParseForm()
		code := r.PostForm.Get("code")
		if r.PostForm.Get("client_secret") != "csecret" || !strings.HasPrefix(code, "c-") {
			json.NewEncoder(w).Encode(map[string]any{"error": "bad_verification_code"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "t-" + strings.TrimPrefix(code, "c-")})
	case r.URL.Path == "/user":
		u, ok := ghUsers[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer t-")]
		if !ok {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(u)
	case r.URL.Path == "/user/emails":
		json.NewEncoder(w).Encode([]map[string]any{{"email": "carol@private.com", "primary": true, "verified": true}})
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls/7"):
		if strings.Contains(r.Header.Get("Accept"), "diff") {
			io.WriteString(w, "diff --git a/x b/x\n+hello")
			return
		}
		fmt.Fprintf(w, `{"title":"t","body":%q,"state":"open","head":{"sha":%q,"ref":"f"},"base":{"ref":"main"},"user":{"login":"dev"}}`, f.body, f.head)
	case strings.Contains(r.URL.Path, "/statuses/"):
		b, _ := io.ReadAll(r.Body)
		var m map[string]string
		json.Unmarshal(b, &m)
		f.statuses = append(f.statuses, m["state"])
		w.WriteHeader(201)
		io.WriteString(w, "{}")
	case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == "GET":
		io.WriteString(w, "[]")
	case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == "POST":
		f.comments++
		w.WriteHeader(201)
		io.WriteString(w, "{}")
	default:
		w.WriteHeader(404)
	}
}

type testApp struct {
	url string
	st  *store.Store
	svc *service.Service
	gh  *fakeGH
	cfg *config.Config
}

type fakeSource struct{ calls *int }

func (f fakeSource) ListRecentPRs(_ context.Context, _ time.Time, fn func(string, github.PRPayload) error) error {
	*f.calls++
	var p github.PRPayload
	p.Number, p.Title, p.State = 50, "old merged work", "closed"
	p.CreatedAt, p.UpdatedAt, p.ClosedAt, p.MergedAt = "2026-11-01T10:00:00Z", "2026-11-02T10:00:00Z", "2026-11-02T10:00:00Z", "2026-11-02T10:00:00Z"
	p.User.Login = "alice"
	p.RequestedReviewers = []struct {
		Login string `json:"login"`
	}{{Login: "bob"}}
	return fn("org/repo", p)
}

func newApp(t *testing.T, mut func(*config.Config), withSource bool) *testApp {
	t.Helper()
	gh := &fakeGH{head: "aaa111", body: "add hello"}
	ghSrv := httptest.NewServer(gh)
	t.Cleanup(ghSrv.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := &config.Config{WebhookSecret: whSecret, JWTSecret: jwtSecret, JWTTTL: time.Hour, AllowedRepos: map[string]bool{},
		MaxDiffBytes: 1000, ReviewTimeout: 10 * time.Second, BootstrapAdmin: "boss", BackfillDays: 90}
	if mut != nil {
		mut(cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	var src service.PRSource
	if withSource {
		src = fakeSource{calls: new(int)}
	}
	svc := service.New(ctx, cfg, st, github.NewClientWithBase(github.StaticToken("tok"), ghSrv.URL), review.Mock{}, src, log)

	oa := github.NewOAuth("cid", "csecret", "http://pandora.test/auth/github/callback")
	oa.WebBase, oa.APIBase = ghSrv.URL, ghSrv.URL
	srv := httptest.NewServer(handler.New(handler.Deps{Config: cfg, Store: st, Svc: svc,
		Auth: auth.NewManager(jwtSecret, time.Hour), OAuth: oa, Log: log}))
	t.Cleanup(srv.Close)
	return &testApp{url: srv.URL, st: st, svc: svc, gh: gh, cfg: cfg}
}

// browser is an HTTP client with a cookie jar that does not follow redirects.
func browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// signIn runs the whole OAuth dance for a fake GitHub user and returns their browser.
func signIn(t *testing.T, a *testApp, name string) *http.Client {
	t.Helper()
	c := browser()
	r, err := c.Get(a.url + "/auth/github/login")
	if err != nil || r.StatusCode != 302 {
		t.Fatalf("login start: %v %v", err, r)
	}
	loc, _ := url.Parse(r.Header.Get("Location"))
	state := loc.Query().Get("state")
	r, err = c.Get(a.url + "/auth/github/callback?code=c-" + name + "&state=" + state)
	if err != nil || r.StatusCode != 302 || r.Header.Get("Location") != "/dashboard/" {
		t.Fatalf("callback for %s: %v %v loc=%v", name, err, r.StatusCode, r.Header.Get("Location"))
	}
	return c
}

func call(t *testing.T, c *http.Client, method, u string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, u, rd)
	if method != "GET" {
		req.Header.Set("X-Pandora-CSRF", "1")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func asMap(b []byte) map[string]any { var m map[string]any; json.Unmarshal(b, &m); return m }
func asList(b []byte) []map[string]any {
	var l []map[string]any
	json.Unmarshal(b, &l)
	return l
}

func postEvent(t *testing.T, a *testApp, event string, payload any, sign bool) *http.Response {
	t.Helper()
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", a.url+"/webhook/github", bytes.NewReader(b))
	req.Header.Set("X-GitHub-Event", event)
	if sign {
		m := hmac.New(sha256.New, []byte(whSecret))
		m.Write(b)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(m.Sum(nil)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

type pr struct {
	action, sha, body, author, state string
	number                           int
	draft, merged                    bool
	reviewers                        []string
}

func (p pr) event() map[string]any {
	n, state, author := p.number, p.state, p.author
	if n == 0 {
		n = 7
	}
	if state == "" {
		state = "open"
	}
	if author == "" {
		author = "dev"
	}
	rs := []map[string]string{}
	for _, r := range p.reviewers {
		rs = append(rs, map[string]string{"login": r})
	}
	return map[string]any{"action": p.action, "number": n,
		"pull_request": map[string]any{"number": n, "title": "t", "body": p.body, "state": state, "draft": p.draft, "merged": p.merged,
			"created_at": "2026-11-20T10:00:00Z", "updated_at": "2026-11-21T10:00:00Z",
			"head": map[string]any{"sha": p.sha}, "base": map[string]any{"ref": "main"},
			"user": map[string]any{"login": author}, "requested_reviewers": rs},
		"repository": map[string]any{"full_name": "org/repo"}}
}
