package handler_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"vocallabs.com/pandora/services/model"
)

func reviewerCall(t *testing.T, a *testApp, c *http.Client, method string, prID int, login string) (int, map[string]any) {
	t.Helper()
	url := a.url + "/api/v1/admin/prs/" + itoa(prID) + "/reviewers"
	var body any
	if method == "POST" {
		body = map[string]any{"login": login}
	} else {
		url += "/" + login
	}
	code, b := call(t, c, method, url, body)
	return code, asMap(b)
}

func reviewersOf(t *testing.T, a *testApp, prID int) string {
	t.Helper()
	p, err := a.st.GetPR(context.Background(), int64(prID))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(p.Reviewers, ",")
}

func TestOnlyAdminsManageReviewers(t *testing.T) {
	a, prID, _ := needsHuman(t, "alice", "bob")
	boss := signIn(t, a, "boss")
	alice, bob, carol := signIn(t, a, "alice"), signIn(t, a, "bob"), signIn(t, a, "carol")
	for _, c := range []*http.Client{alice, bob} {
		approveUser(t, a, boss, c)
	}
	// a reviewer cannot hand the review to someone else, the author cannot pick their reviewer,
	// and a pending account is not allowed in at all
	for who, c := range map[string]*http.Client{"reviewer": bob, "author": alice, "pending": carol} {
		if code, _ := reviewerCall(t, a, c, "POST", prID, "carol"); code != 403 {
			t.Fatalf("%s must not add reviewers: %d", who, code)
		}
		if code, _ := reviewerCall(t, a, c, "DELETE", prID, "bob"); code != 403 {
			t.Fatalf("%s must not remove reviewers: %d", who, code)
		}
	}
	if got := reviewersOf(t, a, prID); got != "bob" {
		t.Fatalf("refused changes must not apply: %s", got)
	}
	// writes need the CSRF header like everything else
	req, _ := http.NewRequest("POST", a.url+"/api/v1/admin/prs/"+itoa(prID)+"/reviewers", strings.NewReader(`{"login":"carol"}`))
	if resp, _ := boss.Do(req); resp.StatusCode != 403 {
		t.Fatalf("csrf: %d", resp.StatusCode)
	}
}

func TestAdminReassignsAbsentReviewer(t *testing.T) {
	a, prID, runID := needsHuman(t, "alice", "bob") // bob is the only reviewer, and he is away
	boss := signIn(t, a, "boss")
	bob, carol := signIn(t, a, "bob"), signIn(t, a, "carol")
	approveUser(t, a, boss, bob)
	approveUser(t, a, boss, carol)

	// carol cannot see or decide it yet
	if code, _ := decideCall(t, a, carol, prID, map[string]any{"run_id": runID, "decision": "approve"}); code != 404 {
		t.Fatalf("carol before assignment: %d", code)
	}

	// the admin adds carol (and the request is mirrored to GitHub so she is notified)
	code, m := reviewerCall(t, a, boss, "POST", prID, "@carol")
	if code != 200 || m["warning"] != "" || reviewersOf(t, a, prID) != "bob,carol" {
		t.Fatalf("add: %d %v / %s", code, m, reviewersOf(t, a, prID))
	}
	if len(a.gh.reviewerCalls) != 1 || a.gh.reviewerCalls[0] != "POST carol" {
		t.Fatalf("github mirror: %v", a.gh.reviewerCalls)
	}
	// adding the same person again is harmless
	if code, _ := reviewerCall(t, a, boss, "POST", prID, "carol"); code != 200 || len(a.gh.reviewerCalls) != 1 {
		t.Fatalf("idempotent add: %d %v", code, a.gh.reviewerCalls)
	}
	// ...and the admin removes bob, so he can no longer block or decide it
	if code, _ := reviewerCall(t, a, boss, "DELETE", prID, "bob"); code != 200 || reviewersOf(t, a, prID) != "carol" {
		t.Fatalf("remove: %s", reviewersOf(t, a, prID))
	}
	if got := a.gh.reviewerCalls[len(a.gh.reviewerCalls)-1]; got != "DELETE bob" {
		t.Fatalf("github withdrawal: %v", a.gh.reviewerCalls)
	}
	if code, _ := decideCall(t, a, bob, prID, map[string]any{"run_id": runID, "decision": "approve"}); code != 404 {
		t.Fatalf("removed reviewer must lose access: %d", code)
	}
	if code, _ := decideCall(t, a, carol, prID, map[string]any{"run_id": runID, "decision": "approve"}); code != 200 {
		t.Fatalf("new reviewer decides: %d", code)
	}
	if st := runStatus(t, a, runID); st != model.StatusAIPassed {
		t.Fatalf("status: %s", st)
	}
	// who did what is on the record
	if n := countEvents(t, a, "reviewer_added"); n != 1 {
		t.Fatalf("reviewer_added events: %d", n)
	}
	if n := countEvents(t, a, "reviewer_removed"); n != 1 {
		t.Fatalf("reviewer_removed events: %d", n)
	}
}

func TestReviewerChangeValidationAndEdgeCases(t *testing.T) {
	a, prID, _ := needsHuman(t, "alice", "bob")
	boss := signIn(t, a, "boss")

	for _, bad := range []string{"", "  ", "not a user", "-leading", strings.Repeat("a", 40), "a/b", "<script>"} {
		if code, _ := reviewerCall(t, a, boss, "POST", prID, bad); code != 400 {
			t.Fatalf("login %q should be rejected: %d", bad, code)
		}
	}
	if code, _ := reviewerCall(t, a, boss, "POST", prID, "ALICE"); code != 400 { // the author, any case
		t.Fatalf("author: %d", code)
	}
	if code, _ := reviewerCall(t, a, boss, "DELETE", prID, "nobody"); code != 404 {
		t.Fatalf("removing a non-reviewer: %d", code)
	}
	if code, _ := reviewerCall(t, a, boss, "POST", 99999, "carol"); code != 404 {
		t.Fatalf("unknown PR: %d", code)
	}

	// GitHub refuses the request (not a collaborator): still assigned here, with a warning
	a.gh.mu.Lock()
	a.gh.failReviewers = true
	a.gh.mu.Unlock()
	code, m := reviewerCall(t, a, boss, "POST", prID, "outsider")
	if code != 200 || !strings.Contains(m["warning"].(string), "GitHub did not request") || reviewersOf(t, a, prID) != "bob,outsider" {
		t.Fatalf("github refusal: %d %v %s", code, m, reviewersOf(t, a, prID))
	}
	// removal succeeds locally even if GitHub is unhappy, and it sticks
	if code, _ := reviewerCall(t, a, boss, "DELETE", prID, "bob"); code != 200 || reviewersOf(t, a, prID) != "outsider" {
		t.Fatalf("local removal: %s", reviewersOf(t, a, prID))
	}
	postEvent(t, a, "pull_request", pr{action: "edited", sha: "aaa111", author: "alice", reviewers: []string{"bob"}}.event(), true)
	a.svc.Wait()
	if got := reviewersOf(t, a, prID); got != "outsider" {
		t.Fatalf("a later webhook must not bring a removed reviewer back: %s", got)
	}

	// closed PRs cannot be reassigned
	postEvent(t, a, "pull_request", pr{action: "closed", sha: "aaa111", author: "alice", state: "closed"}.event(), true)
	if code, _ := reviewerCall(t, a, boss, "POST", prID, "carol"); code != 409 {
		t.Fatalf("closed PR: %d", code)
	}
}

func countEvents(t *testing.T, a *testApp, kind string) int {
	t.Helper()
	n, err := a.st.CountEvents(context.Background(), kind)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
