package handler_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"vocallabs.com/pandora/services/model"
	"vocallabs.com/pandora/services/store"
)

// needsHuman opens PR #7 (empty description, so the AI sends it to a human) by alice with bob
// as the requested reviewer, and returns the app, the PR id and the run id.
func needsHuman(t *testing.T, author string, reviewers ...string) (*testApp, int, int) {
	t.Helper()
	a := newApp(t, nil, false)
	a.gh.mu.Lock()
	a.gh.body = ""
	a.gh.mu.Unlock()
	postEvent(t, a, "pull_request", pr{action: "opened", sha: "aaa111", author: author, reviewers: reviewers}.event(), true)
	a.svc.Wait()
	prs, _ := a.st.ListPRs(context.Background(), store.PRFilter{})
	if len(prs) != 1 || prs[0].LatestRun == nil || prs[0].LatestRun.Status != model.StatusNeedsHuman {
		t.Fatalf("setup: expected a needs_human run: %+v", prs)
	}
	return a, int(prs[0].ID), int(prs[0].LatestRun.ID)
}

func approveUser(t *testing.T, a *testApp, boss *http.Client, c *http.Client) {
	t.Helper()
	if patch(t, a, boss, userID(t, a, c), map[string]any{"role": "user"}) != 200 {
		t.Fatal("approve user")
	}
}

func decideCall(t *testing.T, a *testApp, c *http.Client, prID int, body map[string]any) (int, map[string]any) {
	t.Helper()
	code, b := call(t, c, "POST", a.url+"/api/v1/prs/"+itoa(prID)+"/decision", body)
	return code, asMap(b)
}

func TestDecisionPermissions(t *testing.T) {
	a, prID, runID := needsHuman(t, "alice", "bob")
	boss := signIn(t, a, "boss")
	alice, bob, carol := signIn(t, a, "alice"), signIn(t, a, "bob"), signIn(t, a, "carol")
	for _, c := range []*http.Client{alice, bob, carol} {
		approveUser(t, a, boss, c)
	}
	body := map[string]any{"run_id": runID, "decision": "approve"}

	// the author cannot review their own PR, and the UI is told so
	if code, m := decideCall(t, a, alice, prID, body); code != 403 || !strings.Contains(m["error"].(string), "own") {
		t.Fatalf("author: %d %v", code, m)
	}
	// an unrelated user cannot even see it
	if code, _ := decideCall(t, a, carol, prID, body); code != 404 {
		t.Fatalf("unrelated user: %d", code)
	}
	for who, c := range map[string]*http.Client{"author": alice, "unrelated": carol, "reviewer": bob, "admin": boss} {
		code, b := call(t, c, "GET", a.url+"/api/v1/prs/"+itoa(prID), nil)
		if who == "unrelated" {
			if code != 404 {
				t.Fatalf("%s GET: %d", who, code)
			}
			continue
		}
		want := who == "reviewer" || who == "admin"
		if got := asMap(b)["can_decide"]; got != want {
			t.Fatalf("can_decide for %s = %v, want %v", who, got, want)
		}
	}
	// nothing above changed the run
	if st := runStatus(t, a, runID); st != model.StatusNeedsHuman {
		t.Fatalf("refused attempts must not change anything: %s", st)
	}
}

func runStatus(t *testing.T, a *testApp, runID int) string {
	t.Helper()
	r, _, err := a.st.GetRunWithPR(context.Background(), int64(runID))
	if err != nil {
		t.Fatal(err)
	}
	return r.Status
}

func TestApproveByReviewer(t *testing.T) {
	a, prID, runID := needsHuman(t, "alice", "bob")
	boss := signIn(t, a, "boss")
	bob := signIn(t, a, "bob")
	approveUser(t, a, boss, bob)

	// stale page: a different run id than the one that is current
	if code, _ := decideCall(t, a, bob, prID, map[string]any{"run_id": runID + 99, "decision": "approve"}); code != 409 {
		t.Fatalf("stale run id: %d", code)
	}
	if code, _ := decideCall(t, a, bob, prID, map[string]any{"run_id": runID, "decision": "maybe"}); code != 400 {
		t.Fatalf("bad decision value: %d", code)
	}
	if code, m := decideCall(t, a, bob, prID, map[string]any{"run_id": runID, "decision": "approve", "reason": "looks fine to me"}); code != 200 {
		t.Fatalf("approve: %d %v", code, m)
	}
	if st := runStatus(t, a, runID); st != model.StatusAIPassed {
		t.Fatalf("approve must move the run to ai_passed, got %s", st)
	}
	// audit trail and what the dashboard receives
	_, b := call(t, bob, "GET", a.url+"/api/v1/prs/"+itoa(prID), nil)
	m := asMap(b)
	ds := m["decisions"].([]any)
	d := ds[0].(map[string]any)
	if len(ds) != 1 || d["decision"] != "approved" || d["actor_login"] != "bob" || d["reason"] != "looks fine to me" || m["can_decide"] != false {
		t.Fatalf("decisions: %v can_decide=%v", ds, m["can_decide"])
	}
	// the PR stays open, and the author is told on GitHub
	if len(a.gh.closed) != 0 {
		t.Fatal("approve must not close the PR")
	}
	last := a.gh.bodies[len(a.gh.bodies)-1]
	if !strings.Contains(last, "Approved by @bob") || !strings.Contains(last, "looks fine to me") {
		t.Fatalf("comment: %q", last)
	}
	// a second decision on the same run is refused
	if code, _ := decideCall(t, a, bob, prID, map[string]any{"run_id": runID, "decision": "approve"}); code != 409 {
		t.Fatalf("double decision: %d", code)
	}
}

func TestRejectClosesPR(t *testing.T) {
	a, prID, runID := needsHuman(t, "alice", "bob")
	boss := signIn(t, a, "boss")

	// a reason is mandatory
	for _, r := range []string{"", "   \n "} {
		if code, _ := decideCall(t, a, boss, prID, map[string]any{"run_id": runID, "decision": "reject", "reason": r}); code != 400 {
			t.Fatalf("reject without reason (%q): %d", r, code)
		}
	}
	if st := runStatus(t, a, runID); st != model.StatusNeedsHuman {
		t.Fatalf("rejected-without-reason must not change the run: %s", st)
	}
	reason := "PR description and code changes do not match.\nThe diff touches billing."
	if code, m := decideCall(t, a, boss, prID, map[string]any{"run_id": runID, "decision": "reject", "reason": reason}); code != 200 {
		t.Fatalf("reject: %d %v", code, m)
	}
	if len(a.gh.closed) != 1 || !strings.HasSuffix(a.gh.closed[0], "/pulls/7") {
		t.Fatalf("reject must close the PR on GitHub: %v", a.gh.closed)
	}
	last := a.gh.bodies[len(a.gh.bodies)-1]
	if !strings.Contains(last, "Rejected by @Boss") || !strings.Contains(last, "> The diff touches billing.") {
		t.Fatalf("comment: %q", last)
	}
	got, _ := a.st.GetPR(context.Background(), int64(prID))
	if got.State != "closed" || got.LatestRun.Status != model.StatusRejected {
		t.Fatalf("pr=%s run=%s", got.State, got.LatestRun.Status)
	}
	// GitHub's own "closed" webhook arrives afterwards: the rejection must survive it
	postEvent(t, a, "pull_request", pr{action: "closed", sha: "aaa111", author: "alice", state: "closed"}.event(), true)
	if st := runStatus(t, a, runID); st != model.StatusRejected {
		t.Fatalf("closed webhook rewrote the decision: %s", st)
	}
	ds, _ := a.st.ListDecisions(context.Background(), int64(prID))
	if len(ds) != 1 || ds[0].Decision != "rejected" || ds[0].Reason != reason {
		t.Fatalf("audit: %+v", ds)
	}
	if code, _ := decideCall(t, a, boss, prID, map[string]any{"run_id": runID, "decision": "approve"}); code != 409 {
		t.Fatalf("a closed PR cannot be decided again: %d", code)
	}
}

func TestRejectRollsBackWhenGitHubRefuses(t *testing.T) {
	a, prID, runID := needsHuman(t, "alice", "bob")
	boss := signIn(t, a, "boss")
	a.gh.mu.Lock()
	a.gh.failClose = true
	a.gh.mu.Unlock()

	code, m := decideCall(t, a, boss, prID, map[string]any{"run_id": runID, "decision": "reject", "reason": "no"})
	if code != 502 || !strings.Contains(m["error"].(string), "close") {
		t.Fatalf("expected 502 explaining the close failed: %d %v", code, m)
	}
	if st := runStatus(t, a, runID); st != model.StatusNeedsHuman {
		t.Fatalf("a failed close must roll the rejection back: %s", st)
	}
	if ds, _ := a.st.ListDecisions(context.Background(), int64(prID)); len(ds) != 0 {
		t.Fatalf("no audit row for a rolled-back rejection: %+v", ds)
	}
	// once GitHub works again the reviewer can simply retry
	a.gh.mu.Lock()
	a.gh.failClose = false
	a.gh.mu.Unlock()
	if code, _ := decideCall(t, a, boss, prID, map[string]any{"run_id": runID, "decision": "reject", "reason": "no"}); code != 200 {
		t.Fatalf("retry: %d", code)
	}
}

func TestDecisionRefusedForStaleCommit(t *testing.T) {
	a, prID, runID := needsHuman(t, "alice", "bob")
	boss := signIn(t, a, "boss")
	// the author pushes again (new empty-description run); the reviewer's page still shows the old run
	a.gh.mu.Lock()
	a.gh.head = "bbb222"
	a.gh.mu.Unlock()
	postEvent(t, a, "pull_request", pr{action: "synchronize", sha: "bbb222", author: "alice", reviewers: []string{"bob"}}.event(), true)
	a.svc.Wait()

	if code, _ := decideCall(t, a, boss, prID, map[string]any{"run_id": runID, "decision": "approve"}); code != 409 {
		t.Fatalf("approving the old commit's run must be refused: %d", code)
	}
	// the store enforces it independently of the handler
	if _, err := a.st.DecideRun(context.Background(), int64(runID), &model.User{ID: 1, GitHubLogin: "x"}, "approved", ""); err != store.ErrStale {
		t.Fatalf("store must refuse a superseded run: %v", err)
	}
	// the fresh run can be decided
	prs, _ := a.st.ListPRs(context.Background(), store.PRFilter{})
	if code, _ := decideCall(t, a, boss, prID, map[string]any{"run_id": prs[0].LatestRun.ID, "decision": "approve"}); code != 200 {
		t.Fatalf("current run: %d", code)
	}
}

func TestAdminCannotReviewOwnPR(t *testing.T) {
	a, prID, runID := needsHuman(t, "Boss", "bob") // opened by the super-admin
	boss := signIn(t, a, "boss")
	if code, m := decideCall(t, a, boss, prID, map[string]any{"run_id": runID, "decision": "approve"}); code != 403 {
		t.Fatalf("self-approval by an admin: %d %v", code, m)
	}
	_, b := call(t, boss, "GET", a.url+"/api/v1/prs/"+itoa(prID), nil)
	if asMap(b)["can_decide"] != false {
		t.Fatal("UI must not offer self-approval")
	}
}

func TestNeedsHumanOnlyAndPendingBlocked(t *testing.T) {
	a := newApp(t, nil, false)
	boss := signIn(t, a, "boss")
	// a PR whose AI review passed has nothing to decide
	postEvent(t, a, "pull_request", pr{action: "opened", sha: "aaa111", body: "add hello", author: "alice"}.event(), true)
	a.svc.Wait()
	prs, _ := a.st.ListPRs(context.Background(), store.PRFilter{})
	if code, _ := decideCall(t, a, boss, int(prs[0].ID), map[string]any{"run_id": prs[0].LatestRun.ID, "decision": "approve"}); code != 409 {
		t.Fatalf("ai_passed run: %d", code)
	}
	// pending accounts are rejected by the role gate before anything else
	bob := signIn(t, a, "bob")
	if code, _ := decideCall(t, a, bob, int(prs[0].ID), map[string]any{"run_id": 1, "decision": "approve"}); code != 403 {
		t.Fatalf("pending account: %d", code)
	}
	// the write needs the CSRF header like every other mutation
	req, _ := http.NewRequest("POST", a.url+"/api/v1/prs/1/decision", strings.NewReader(`{"run_id":1,"decision":"approve"}`))
	req.Header.Set("Content-Type", "application/json")
	if resp, _ := boss.Do(req); resp.StatusCode != 403 {
		t.Fatalf("csrf: %d", resp.StatusCode)
	}
}
