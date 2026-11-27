package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func newKey(t *testing.T) (*rsa.PrivateKey, string) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

func TestAppAuthMintsScopedCachedTokens(t *testing.T) {
	key, pemText := newKey(t)
	var lookups, mints atomic.Int32
	var gotRepos []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// every app call must carry a valid RS256 JWT for our app id
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		claims := &jwt.RegisteredClaims{}
		if _, err := jwt.ParseWithClaims(tok, claims, func(*jwt.Token) (any, error) { return &key.PublicKey, nil },
			jwt.WithValidMethods([]string{"RS256"})); err != nil || claims.Issuer != "12345" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/org/eva/installation":
			lookups.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"id": 777})
		case r.Method == "GET" && r.URL.Path == "/repos/org/nope/installation":
			w.WriteHeader(404)
		case r.Method == "POST" && r.URL.Path == "/app/installations/777/access_tokens":
			mints.Add(1)
			var b struct {
				Repositories []string `json:"repositories"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			gotRepos = b.Repositories
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"token": "ghs_installtoken", "expires_at": time.Now().Add(time.Hour)})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	a, err := NewAppAuth("12345", strings.ReplaceAll(pemText, "\n", `\n`), srv.URL) // single-line form
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		tok, err := a.Token(ctx, "org/eva")
		if err != nil || tok != "ghs_installtoken" {
			t.Fatalf("token=%q err=%v", tok, err)
		}
	}
	if lookups.Load() != 1 || mints.Load() != 1 {
		t.Fatalf("should cache: lookups=%d mints=%d", lookups.Load(), mints.Load())
	}
	if len(gotRepos) != 1 || gotRepos[0] != "eva" {
		t.Fatalf("token must be scoped to the one repo: %v", gotRepos)
	}
	if _, err := a.Token(ctx, "org/nope"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("uninstalled repo: %v", err)
	}
	if _, err := a.Token(ctx, "badrepo"); err == nil {
		t.Fatal("bad repo name must error")
	}
}

func TestAppAuthRefreshesNearExpiry(t *testing.T) {
	key, pemText := newKey(t)
	_ = key
	var mints atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/installation") {
			json.NewEncoder(w).Encode(map[string]any{"id": 1})
			return
		}
		mints.Add(1)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"token": "t", "expires_at": time.Now().Add(2 * time.Minute)}) // inside the 5m refresh window
	}))
	defer srv.Close()
	a, _ := NewAppAuth("1", pemText, srv.URL)
	a.Token(context.Background(), "o/r")
	a.Token(context.Background(), "o/r")
	if mints.Load() != 2 {
		t.Fatalf("near-expiry token must be re-minted, mints=%d", mints.Load())
	}
}

func TestBadKey(t *testing.T) {
	if _, err := NewAppAuth("1", "not a key", ""); err == nil {
		t.Fatal("expected error")
	}
}
