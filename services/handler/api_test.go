package handler_test

import (
	"context"
	"testing"

	"vocallabs.com/pandora/services/store"
)

func TestUserVisibility(t *testing.T) {
	a := newApp(t, nil, false)
	boss := signIn(t, a, "boss")
	alice, bob := signIn(t, a, "alice"), signIn(t, a, "bob")
	for _, c := range []string{"alice", "bob"} {
		var id int
		users, _ := a.st.ListUsers(context.Background())
		for _, u := range users {
			if u.GitHubLogin == c {
				id = int(u.ID)
			}
		}
		call(t, boss, "PATCH", a.url+"/api/v1/admin/users/"+itoa(id), map[string]any{"role": "user"})
	}

	// alice authored #11 (reviewer: bob); bob authored #12 (no reviewers); #13 is carol's
	post := func(n int, author string, reviewers ...string) {
		postEvent(t, a, "pull_request", pr{action: "review_requested", number: n, sha: "s", body: "b", author: author, draft: true, reviewers: reviewers}.event(), true)
	}
	post(11, "alice", "bob")
	post(12, "bob")
	post(13, "carol")

	_, b := call(t, alice, "GET", a.url+"/api/v1/me/prs", nil)
	mine := asList(b)
	if len(mine) != 1 || mine[0]["number"] != float64(11) {
		t.Fatalf("alice's PRs: %v", mine)
	}
	_, b = call(t, bob, "GET", a.url+"/api/v1/me/prs", nil)
	if l := asList(b); len(l) != 1 || l[0]["number"] != float64(12) {
		t.Fatalf("bob's PRs: %v", l)
	}
	_, b = call(t, bob, "GET", a.url+"/api/v1/me/reviews", nil)
	if l := asList(b); len(l) != 1 || l[0]["number"] != float64(11) {
		t.Fatalf("bob's review queue: %v", l)
	}
	_, b = call(t, alice, "GET", a.url+"/api/v1/me/reviews", nil)
	if l := asList(b); len(l) != 0 {
		t.Fatalf("alice has nothing to review: %v", l)
	}

	idOf := func(n int) string {
		prs, _ := a.st.ListPRs(context.Background(), store.PRFilter{})
		for _, p := range prs {
			if p.Number == n {
				return itoa(int(p.ID))
			}
		}
		t.Fatalf("no PR %d", n)
		return ""
	}
	// detail: author and assigned reviewer can open it; others get 404 (not 403: no leaking)
	if code, _ := call(t, alice, "GET", a.url+"/api/v1/prs/"+idOf(11), nil); code != 200 {
		t.Fatalf("author detail: %d", code)
	}
	if code, _ := call(t, bob, "GET", a.url+"/api/v1/prs/"+idOf(11), nil); code != 200 {
		t.Fatalf("reviewer detail: %d", code)
	}
	if code, _ := call(t, alice, "GET", a.url+"/api/v1/prs/"+idOf(12), nil); code != 404 {
		t.Fatalf("other's PR must be hidden: %d", code)
	}
	if code, _ := call(t, boss, "GET", a.url+"/api/v1/prs/"+idOf(13), nil); code != 200 {
		t.Fatalf("admin sees all: %d", code)
	}
	// the all-PRs list is admin only, with filters
	if code, _ := call(t, alice, "GET", a.url+"/api/v1/admin/prs", nil); code != 403 {
		t.Fatalf("all PRs for user: %d", code)
	}
	_, b = call(t, boss, "GET", a.url+"/api/v1/admin/prs", nil)
	if l := asList(b); len(l) != 3 {
		t.Fatalf("admin list: %d", len(l))
	}
	_, b = call(t, boss, "GET", a.url+"/api/v1/admin/prs?author=ALICE&reviewer=bob", nil)
	if l := asList(b); len(l) != 1 {
		t.Fatalf("admin filter: %v", l)
	}
}

func TestAdminStatsAndSync(t *testing.T) {
	a := newApp(t, nil, true)
	boss := signIn(t, a, "boss")

	if code, _ := call(t, boss, "POST", a.url+"/api/v1/admin/sync", nil); code != 202 {
		t.Fatalf("sync start: %d", code)
	}
	a.svc.Wait()
	st := a.svc.SyncStatus()
	if st.Running || st.PRs != 1 || st.Error != "" || !st.Enabled || st.Days != 90 {
		t.Fatalf("sync status: %+v", st)
	}
	// backfilled PR is recorded but never AI-reviewed
	prs, _ := a.st.ListPRs(context.Background(), store.PRFilter{State: "merged"})
	if len(prs) != 1 || prs[0].LatestRun != nil || prs[0].Author != "alice" || len(prs[0].Reviewers) != 1 {
		t.Fatalf("backfilled: %+v", prs)
	}

	postEvent(t, a, "pull_request", pr{action: "review_requested", number: 20, sha: "s", body: "b", author: "bob", draft: true, reviewers: []string{"alice"}}.event(), true)
	_, b := call(t, boss, "GET", a.url+"/api/v1/admin/stats", nil)
	s := asMap(b)
	if s["open"] != float64(1) || s["merged"] != float64(1) || s["drafts"] != float64(1) {
		t.Fatalf("stats: %v", s)
	}
	if len(s["weekly"].([]any)) != 12 || len(s["top_authors"].([]any)) != 2 || len(s["awaiting_review"].([]any)) != 1 {
		t.Fatalf("stats shape: %v", s)
	}
	if s["avg_hours_to_merge"].(float64) <= 0 {
		t.Fatalf("avg hours: %v", s["avg_hours_to_merge"])
	}

	// without a GitHub App there is nothing to sync from
	a2 := newApp(t, nil, false)
	if code, _ := call(t, signIn(t, a2, "boss"), "POST", a2.url+"/api/v1/admin/sync", nil); code != 400 {
		t.Fatalf("sync without app: %d", code)
	}
}
