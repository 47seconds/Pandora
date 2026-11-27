package review

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenRouterParsesReply(t *testing.T) {
	var gotAuth, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b := make([]byte, 4096)
		n, _ := r.Body.Read(b)
		gotModel = string(b[:n])
		w.Write([]byte(`{"choices":[{"message":{"content":"Here:\n{\"matches_description\":true,\"verdict\":\"pass\",\"summary\":\"ok\"}"}}]}`))
	}))
	defer srv.Close()
	o := NewOpenRouter("sk-or-test", "anthropic/claude-sonnet-5.5")
	o.base = srv.URL
	res, err := o.Review(context.Background(), Input{Repo: "o/r", Number: 1, Title: "t", Body: "b", Diff: "d"})
	if err != nil || res.Verdict != VerdictPass || !res.MatchesDescription {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if gotAuth != "Bearer sk-or-test" || !strings.Contains(gotModel, "anthropic/claude-sonnet-5.5") {
		t.Fatalf("auth=%q body=%q", gotAuth, gotModel)
	}
}

func TestOpenRouterErrorInBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"error":{"message":"No credits"}}`)) // 200 with error body
	}))
	defer srv.Close()
	o := NewOpenRouter("k", "m")
	o.base = srv.URL
	if _, err := o.Review(context.Background(), Input{}); err == nil || !strings.Contains(err.Error(), "No credits") {
		t.Fatalf("err=%v", err)
	}
}
