package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestOAuthExchange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			r.ParseForm()
			if r.PostForm.Get("code") != "good" || r.PostForm.Get("client_secret") != "sec" || r.PostForm.Get("redirect_uri") != "http://x/cb" {
				json.NewEncoder(w).Encode(map[string]any{"error": "bad_verification_code", "error_description": "nope"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "utok"})
		case "/user":
			if r.Header.Get("Authorization") != "Bearer utok" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 42, "login": "octo", "name": "Octo Cat", "email": "", "avatar_url": "https://a/b.png"})
		case "/user/emails":
			json.NewEncoder(w).Encode([]map[string]any{
				{"email": "no@x.com", "primary": false, "verified": true},
				{"email": "unverified@x.com", "primary": true, "verified": false},
			})
		}
	}))
	defer srv.Close()

	o := NewOAuth("cid", "sec", "http://x/cb")
	o.WebBase, o.APIBase = srv.URL, srv.URL

	p, err := o.Exchange(context.Background(), "good")
	if err != nil || p.ID != 42 || p.Login != "octo" || p.Name != "Octo Cat" || p.AvatarURL != "https://a/b.png" {
		t.Fatalf("profile=%+v err=%v", p, err)
	}
	if p.Email != "" {
		t.Fatalf("must not use unverified/non-primary emails: %q", p.Email)
	}
	if _, err := o.Exchange(context.Background(), "bad"); err == nil || !strings.Contains(err.Error(), "bad_verification_code") {
		t.Fatalf("bad code: %v", err)
	}

	u, _ := url.Parse(o.AuthorizeURL("st8"))
	q := u.Query()
	if u.Path != "/login/oauth/authorize" || q.Get("client_id") != "cid" || q.Get("state") != "st8" || q.Get("redirect_uri") != "http://x/cb" {
		t.Fatalf("authorize url: %v", u)
	}
}
