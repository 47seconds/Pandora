package handler_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vocallabs.com/pandora/services/config"
	"vocallabs.com/pandora/services/model"
	"vocallabs.com/pandora/services/store"
)

func TestWebhookPipeline(t *testing.T) {
	a := newApp(t, nil, false)
	ctx := context.Background()

	if r := postEvent(t, a, "pull_request", pr{action: "opened", sha: "aaa111", body: "add hello"}.event(), false); r.StatusCode != 401 {
		t.Fatalf("unsigned: want 401 got %d", r.StatusCode)
	}
	if r := postEvent(t, a, "pull_request", pr{action: "opened", sha: "aaa111", body: "add hello"}.event(), true); r.StatusCode != 202 {
		t.Fatalf("opened: got %d", r.StatusCode)
	}
	if r := postEvent(t, a, "pull_request", pr{action: "opened", sha: "aaa111", body: "add hello"}.event(), true); r.StatusCode != 200 {
		t.Fatalf("duplicate should be 200, got %d", r.StatusCode)
	}
	a.svc.Wait()
	prs, _ := a.st.ListPRs(ctx, store.PRFilter{})
	if len(prs) != 1 || prs[0].LatestRun.Status != model.StatusAIPassed {
		t.Fatalf("after first run: %+v", prs)
	}

	// new commit -> new run, old one superseded
	a.gh.mu.Lock()
	a.gh.head = "bbb222"
	a.gh.mu.Unlock()
	postEvent(t, a, "pull_request", pr{action: "synchronize", sha: "bbb222", body: "add hello"}.event(), true)
	a.svc.Wait()
	runs, _ := a.st.ListRuns(ctx, prs[0].ID)
	if len(runs) != 2 || runs[0].SHA != "bbb222" || runs[0].Status != model.StatusAIPassed || runs[1].Status != model.StatusSuperseded {
		t.Fatalf("after push: %+v", runs)
	}

	// empty description -> needs human, no AI call
	a.gh.mu.Lock()
	a.gh.head, a.gh.body = "ccc333", ""
	a.gh.mu.Unlock()
	postEvent(t, a, "pull_request", pr{action: "synchronize", sha: "ccc333"}.event(), true)
	a.svc.Wait()
	runs, _ = a.st.ListRuns(ctx, prs[0].ID)
	if runs[0].Status != model.StatusNeedsHuman {
		t.Fatalf("empty body: %+v", runs[0])
	}

	// merge-close supersedes everything and records the outcome
	postEvent(t, a, "pull_request", pr{action: "closed", sha: "ccc333", state: "closed", merged: true}.event(), true)
	runs, _ = a.st.ListRuns(ctx, prs[0].ID)
	for _, x := range runs {
		if x.Status != model.StatusSuperseded {
			t.Fatalf("close should supersede: %+v", runs)
		}
	}
	got, _ := a.st.GetPR(ctx, prs[0].ID)
	if got.State != "closed" || !got.Merged {
		t.Fatalf("closed/merged not recorded: %+v", got.PR)
	}
}

func TestPRTrackingWithoutReview(t *testing.T) {
	a := newApp(t, nil, false)
	ctx := context.Background()

	// draft: tracked (so it shows under "My PRs") but never reviewed
	r := postEvent(t, a, "pull_request", pr{action: "opened", number: 11, sha: "d1", body: "wip", author: "alice", draft: true}.event(), true)
	if r.StatusCode != 200 {
		t.Fatalf("draft: %d", r.StatusCode)
	}
	// review request recorded from the payload
	postEvent(t, a, "pull_request", pr{action: "review_requested", number: 11, sha: "d1", body: "wip", author: "alice", draft: true, reviewers: []string{"Bob", "carol"}}.event(), true)
	prs, _ := a.st.ListPRs(ctx, store.PRFilter{})
	if len(prs) != 1 || prs[0].LatestRun != nil || !prs[0].Draft || len(prs[0].Reviewers) != 2 {
		t.Fatalf("draft tracking: %+v", prs)
	}
	if got, _ := a.st.ListPRs(ctx, store.PRFilter{Reviewer: "bob"}); len(got) != 1 { // case-insensitive
		t.Fatalf("reviewer filter: %d", len(got))
	}
	// removing a reviewer replaces the set
	postEvent(t, a, "pull_request", pr{action: "review_request_removed", number: 11, sha: "d1", body: "wip", author: "alice", draft: true, reviewers: []string{"carol"}}.event(), true)
	if got, _ := a.st.ListPRs(ctx, store.PRFilter{Reviewer: "bob"}); len(got) != 0 {
		t.Fatalf("bob should be removed: %d", len(got))
	}
}

func TestAllowedReposAndSignature(t *testing.T) {
	a := newApp(t, func(c *config.Config) { c.AllowedRepos = map[string]bool{"org/other": true} }, false)
	r := postEvent(t, a, "pull_request", pr{action: "opened", sha: "x", body: "b"}.event(), true)
	if r.StatusCode != 200 {
		t.Fatalf("ignored repo: %d", r.StatusCode)
	}
	if prs, _ := a.st.ListPRs(context.Background(), store.PRFilter{}); len(prs) != 0 {
		t.Fatal("disallowed repo must not be stored")
	}
	if r := postEvent(t, a, "ping", map[string]any{}, true); r.StatusCode != 200 {
		t.Fatalf("ping: %d", r.StatusCode)
	}
	// the alias route the GitHub App is configured with
	a2 := newApp(t, nil, false)
	b := strings.NewReader("{}")
	resp, err := browser().Post(a2.url+"/github/webhook", "application/json", b)
	if err != nil || resp.StatusCode != 401 {
		t.Fatalf("alias route should exist and verify signatures: %v %v", err, resp)
	}
}

func TestDashboardServed(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>pandora</html>"), 0o644)
	a := newApp(t, func(c *config.Config) { c.StaticDir = dir }, false)
	c := browser()
	if r, err := c.Get(a.url + "/"); err != nil || r.StatusCode != 302 || r.Header.Get("Location") != "/dashboard/" {
		t.Fatalf("root redirect: %v %v", err, r)
	}
	r, err := c.Get(a.url + "/dashboard/")
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("dashboard: %v %v", err, r)
	}
	csp := r.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "avatars.githubusercontent.com") {
		t.Fatalf("CSP: %q", csp)
	}
	if code, _ := call(t, c, "GET", a.url+"/api/v1/me/prs", nil); code != 401 {
		t.Fatalf("api must stay protected: %d", code)
	}
	a2 := newApp(t, func(c *config.Config) { c.StaticDir = filepath.Join(t.TempDir(), "nope") }, false)
	if r, _ := c.Get(a2.url + "/dashboard/"); r.StatusCode != 404 {
		t.Fatalf("missing static dir should disable dashboard: %d", r.StatusCode)
	}
}
