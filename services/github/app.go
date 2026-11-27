package github

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AppAuth authenticates as a GitHub App. For each repo it finds the app's
// installation and mints a short-lived installation token limited to that one repo.
// Tokens and installation ids are cached; nothing long-lived is ever used for API calls.
type AppAuth struct {
	appID string
	key   *rsa.PrivateKey
	base  string
	http  *http.Client

	mu     sync.Mutex
	tokens map[string]cachedToken // repo -> token
	insts  map[string]int64       // repo -> installation id
}

type cachedToken struct {
	value   string
	expires time.Time
}

// NewAppAuth parses the app's private key (PEM, PKCS#1 or PKCS#8). A literal "\n"
// sequence is accepted so the key can live in a single-line env var.
func NewAppAuth(appID, privateKeyPEM, base string) (*AppAuth, error) {
	if base == "" {
		base = DefaultBase
	}
	pemText := strings.ReplaceAll(privateKeyPEM, `\n`, "\n")
	block, _ := pem.Decode([]byte(strings.TrimSpace(pemText)))
	if block == nil {
		return nil, errors.New("github app private key is not valid PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k8, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k8.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("github app private key is not RSA")
		}
		key = rk
	} else {
		return nil, errors.New("cannot parse github app private key")
	}
	return &AppAuth{appID: appID, key: key, base: base, http: &http.Client{Timeout: 30 * time.Second},
		tokens: map[string]cachedToken{}, insts: map[string]int64{}}, nil
}

func (a *AppAuth) appJWT() (string, error) {
	now := time.Now()
	return jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer:    a.appID,
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)), // allow for clock drift
		ExpiresAt: jwt.NewNumericDate(now.Add(9 * time.Minute)),   // GitHub max is 10
	}).SignedString(a.key)
}

func (a *AppAuth) call(ctx context.Context, method, path string, body any, out any) error {
	j, err := a.appJWT()
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+j)
	req.Header.Set("Accept", jsonAccept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if resp.StatusCode == http.StatusNotFound && strings.Contains(path, "/installation") {
			return fmt.Errorf("github app is not installed on this repo (%s)", path)
		}
		return fmt.Errorf("github app %s %s: %s: %s", method, path, resp.Status, b)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Token returns a valid installation token for repo ("owner/name").
func (a *AppAuth) Token(ctx context.Context, repo string) (string, error) {
	a.mu.Lock()
	if t, ok := a.tokens[repo]; ok && time.Until(t.expires) > 5*time.Minute {
		a.mu.Unlock()
		return t.value, nil
	}
	id, known := a.insts[repo]
	a.mu.Unlock()

	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", fmt.Errorf("bad repo %q", repo)
	}
	if !known {
		var inst struct {
			ID int64 `json:"id"`
		}
		if err := a.call(ctx, "GET", fmt.Sprintf("/repos/%s/%s/installation", owner, name), nil, &inst); err != nil {
			return "", err
		}
		id = inst.ID
	}
	var tok struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	err := a.call(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", id),
		map[string]any{"repositories": []string{name}}, &tok) // least privilege: this repo only
	if err != nil {
		a.mu.Lock()
		delete(a.insts, repo) // installation may have changed; look it up again next time
		a.mu.Unlock()
		return "", err
	}
	a.mu.Lock()
	a.insts[repo] = id
	a.tokens[repo] = cachedToken{value: tok.Token, expires: tok.ExpiresAt}
	a.mu.Unlock()
	return tok.Token, nil
}

func (a *AppAuth) installationToken(ctx context.Context, id int64) (string, error) {
	var tok struct {
		Token string `json:"token"`
	}
	if err := a.call(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", id), map[string]any{}, &tok); err != nil {
		return "", err
	}
	return tok.Token, nil
}

// getWith performs a GET using an installation token (not the app JWT).
func (a *AppAuth) getWith(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", a.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", jsonAccept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("github GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ListRecentPRs walks every installation and repo the app can see and calls fn for each
// PR updated at or after since. Per-repo failures do not stop the walk; they are returned together.
func (a *AppAuth) ListRecentPRs(ctx context.Context, since time.Time, fn func(repo string, p PRPayload) error) error {
	var errs []error
	for page := 1; ; page++ {
		var insts []struct {
			ID int64 `json:"id"`
		}
		if err := a.call(ctx, "GET", fmt.Sprintf("/app/installations?per_page=100&page=%d", page), nil, &insts); err != nil {
			return err
		}
		for _, in := range insts {
			tok, err := a.installationToken(ctx, in.ID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if err := a.walkInstallation(ctx, tok, since, fn); err != nil {
				errs = append(errs, err)
			}
		}
		if len(insts) < 100 {
			break
		}
	}
	return errors.Join(errs...)
}

func (a *AppAuth) walkInstallation(ctx context.Context, token string, since time.Time, fn func(string, PRPayload) error) error {
	var errs []error
	for page := 1; ; page++ {
		var repos struct {
			Repositories []struct {
				FullName string `json:"full_name"`
			} `json:"repositories"`
		}
		if err := a.getWith(ctx, token, fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), &repos); err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, r := range repos.Repositories {
			if err := a.walkRepo(ctx, token, r.FullName, since, fn); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", r.FullName, err))
			}
		}
		if len(repos.Repositories) < 100 {
			break
		}
	}
	return errors.Join(errs...)
}

func (a *AppAuth) walkRepo(ctx context.Context, token, repo string, since time.Time, fn func(string, PRPayload) error) error {
	for page := 1; ; page++ {
		var prs []PRPayload
		if err := a.getWith(ctx, token,
			fmt.Sprintf("/repos/%s/pulls?state=all&sort=updated&direction=desc&per_page=100&page=%d", repo, page), &prs); err != nil {
			return err
		}
		for _, p := range prs {
			if t, err := time.Parse(time.RFC3339, p.UpdatedAt); err == nil && t.Before(since) {
				return nil // sorted newest-first: everything after this is older
			}
			if err := fn(repo, p); err != nil {
				return err
			}
		}
		if len(prs) < 100 {
			return nil
		}
	}
}
