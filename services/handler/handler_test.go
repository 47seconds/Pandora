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
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"vocallabs.com/pandora/services/auth"
	"vocallabs.com/pandora/services/config"
	"vocallabs.com/pandora/services/github"
	"vocallabs.com/pandora/services/handler"
	"vocallabs.com/pandora/services/model"
	"vocallabs.com/pandora/services/review"
	"vocallabs.com/pandora/services/service"
	"vocallabs.com/pandora/services/store"
)

const (
	secret    = "s3cret"
	jwtSecret = "0123456789abcdef0123456789abcdef"
)

type fakeGH struct {
	mu       sync.Mutex
	head     string
	body     string
	statuses []string
	comments int
}

func (f *fakeGH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
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
		f.statuses = append(f.statuses, strings.TrimPrefix(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], "")+":"+m["state"])
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

func post(t *testing.T, url, event string, payload any, sign bool) *http.Response {
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url+"/webhook/github", bytes.NewReader(b))
	req.Header.Set("X-GitHub-Event", event)
	if sign {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write(b)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(m.Sum(nil)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func prEvent(action, sha, body string) map[string]any {
	return map[string]any{"action": action, "number": 7,
		"pull_request": map[string]any{"title": "t", "body": body, "head": map[string]any{"sha": sha}, "user": map[string]any{"login": "dev"}},
		"repository":   map[string]any{"full_name": "org/repo"}}
}

func TestPipeline(t *testing.T) {
	gh := &fakeGH{head: "aaa111", body: "add hello"}
	ghSrv := httptest.NewServer(gh)
	defer ghSrv.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{WebhookSecret: secret, JWTSecret: jwtSecret, JWTTTL: time.Hour, SignupDomains: map[string]bool{}, AllowedRepos: map[string]bool{}, MaxDiffBytes: 1000, ReviewTimeout: 10 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(ctx, cfg, st, github.NewClientWithBase("tok", ghSrv.URL), review.Mock{}, log)
	srv := httptest.NewServer(handler.New(cfg, st, svc, auth.NewManager(cfg.JWTSecret, cfg.JWTTTL), log))
	defer srv.Close()

	if r := post(t, srv.URL, "pull_request", prEvent("opened", "aaa111", "add hello"), false); r.StatusCode != 401 {
		t.Fatalf("unsigned: want 401 got %d", r.StatusCode)
	}
	r := post(t, srv.URL, "pull_request", prEvent("opened", "aaa111", "add hello"), true)
	if r.StatusCode != 202 {
		t.Fatalf("opened: got %d", r.StatusCode)
	}
	if r := post(t, srv.URL, "pull_request", prEvent("opened", "aaa111", "add hello"), true); r.StatusCode != 200 {
		t.Fatalf("duplicate should be 200 (ignored), got %d", r.StatusCode)
	}
	svc.Wait()
	prs, _ := st.ListPRs(context.Background(), "", 10)
	if len(prs) != 1 || prs[0].LatestRun.Status != model.StatusAIPassed {
		t.Fatalf("after first run: %+v", prs)
	}

	// new commit -> new run, old one superseded
	gh.mu.Lock()
	gh.head = "bbb222"
	gh.mu.Unlock()
	post(t, srv.URL, "pull_request", prEvent("synchronize", "bbb222", "add hello"), true)
	svc.Wait()
	runs, _ := st.ListRuns(context.Background(), prs[0].ID)
	if len(runs) != 2 || runs[0].SHA != "bbb222" || runs[0].Status != model.StatusAIPassed || runs[1].Status != model.StatusSuperseded {
		t.Fatalf("after push: %+v", runs)
	}

	// empty description -> needs human, no AI call
	gh.mu.Lock()
	gh.head, gh.body = "ccc333", ""
	gh.mu.Unlock()
	post(t, srv.URL, "pull_request", prEvent("synchronize", "ccc333", ""), true)
	svc.Wait()
	runs, _ = st.ListRuns(context.Background(), prs[0].ID)
	if runs[0].Status != model.StatusNeedsHuman {
		t.Fatalf("empty body: %+v", runs[0])
	}

	// close supersedes everything
	post(t, srv.URL, "pull_request", prEvent("closed", "ccc333", ""), true)
	runs, _ = st.ListRuns(context.Background(), prs[0].ID)
	for _, x := range runs {
		if x.Status != model.StatusSuperseded {
			t.Fatalf("close should supersede: %+v", runs)
		}
	}
}

func TestPolicy(t *testing.T) {
	r := &review.Result{Verdict: review.VerdictPass, MatchesDescription: true, Issues: []review.Issue{{Severity: "critical"}}}
	review.ApplyPolicy(review.Input{}, r)
	if r.Verdict != review.VerdictBlock {
		t.Fatal("critical must block")
	}
	r = &review.Result{Verdict: review.VerdictPass, MatchesDescription: false}
	review.ApplyPolicy(review.Input{}, r)
	if r.Verdict != review.VerdictNeedsHuman {
		t.Fatal("mismatch must need human")
	}
}
