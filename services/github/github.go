// Package github holds webhook verification and a minimal GitHub REST client.
package github

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// VerifySignature checks the X-Hub-Signature-256 header against the raw body.
func VerifySignature(secret string, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// PRPayload is a pull request as GitHub sends it, in webhook events and in list calls.
type PRPayload struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Draft     bool   `json:"draft"`
	State     string `json:"state"`
	Merged    bool   `json:"merged"` // absent in list responses; use MergedAt there
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	ClosedAt  string `json:"closed_at"`
	MergedAt  string `json:"merged_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	RequestedReviewers []struct {
		Login string `json:"login"`
	} `json:"requested_reviewers"`
}

// ReviewerLogins lists the individually requested reviewers (teams are not included).
func (p PRPayload) ReviewerLogins() []string {
	out := make([]string, 0, len(p.RequestedReviewers))
	for _, r := range p.RequestedReviewers {
		out = append(out, r.Login)
	}
	return out
}

// PREvent is the pull_request webhook payload.
type PREvent struct {
	Action      string    `json:"action"`
	Number      int       `json:"number"`
	PullRequest PRPayload `json:"pull_request"`
	Repository  struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type PR struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	State string `json:"state"`
	Draft bool   `json:"draft"`
	User  struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// TokenSource supplies the credential used for API calls about a repo ("owner/name").
type TokenSource interface {
	Token(ctx context.Context, repo string) (string, error)
}

// StaticToken is a fixed credential (personal access token); dev fallback only.
type StaticToken string

func (t StaticToken) Token(context.Context, string) (string, error) { return string(t), nil }

type Client struct {
	src  TokenSource
	base string
	http *http.Client
}

func NewClient(src TokenSource) *Client {
	return &Client{src: src, base: DefaultBase, http: &http.Client{Timeout: 30 * time.Second}}
}

const DefaultBase = "https://api.github.com"

func (c *Client) do(ctx context.Context, repo, method, path, accept string, body any) (*http.Response, error) {
	token, err := c.src.Token(ctx, repo)
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("github %s %s: %s: %s", method, path, resp.Status, b)
	}
	return resp, nil
}

const jsonAccept = "application/vnd.github+json"

func (c *Client) GetPR(ctx context.Context, repo string, n int) (*PR, error) {
	resp, err := c.do(ctx, repo, "GET", fmt.Sprintf("/repos/%s/pulls/%d", repo, n), jsonAccept, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var pr PR
	return &pr, json.NewDecoder(resp.Body).Decode(&pr)
}

// GetDiff returns the PR's unified diff, cut at maxBytes (truncated=true if cut).
func (c *Client) GetDiff(ctx context.Context, repo string, n, maxBytes int) (diff string, truncated bool, err error) {
	resp, err := c.do(ctx, repo, "GET", fmt.Sprintf("/repos/%s/pulls/%d", repo, n), "application/vnd.github.v3.diff", nil)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return "", false, err
	}
	if len(b) > maxBytes {
		return string(b[:maxBytes]), true, nil
	}
	return string(b), false, nil
}

// SetStatus sets a commit status (state: pending|success|failure|error).
func (c *Client) SetStatus(ctx context.Context, repo, sha, state, contextName, desc string) error {
	if len(desc) > 140 {
		desc = desc[:137] + "..."
	}
	resp, err := c.do(ctx, repo, "POST", fmt.Sprintf("/repos/%s/statuses/%s", repo, sha), jsonAccept,
		map[string]string{"state": state, "context": contextName, "description": desc})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// UpsertComment edits the PR comment containing marker, or creates it.
func (c *Client) UpsertComment(ctx context.Context, repo string, n int, marker, body string) error {
	resp, err := c.do(ctx, repo, "GET", fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100", repo, n), jsonAccept, nil)
	if err != nil {
		return err
	}
	var comments []struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	err = json.NewDecoder(resp.Body).Decode(&comments)
	resp.Body.Close()
	if err != nil {
		return err
	}
	for _, cm := range comments {
		if strings.Contains(cm.Body, marker) {
			r, err := c.do(ctx, repo, "PATCH", fmt.Sprintf("/repos/%s/issues/comments/%d", repo, cm.ID), jsonAccept, map[string]string{"body": body})
			if err != nil {
				return err
			}
			r.Body.Close()
			return nil
		}
	}
	r, err := c.do(ctx, repo, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", repo, n), jsonAccept, map[string]string{"body": body})
	if err != nil {
		return err
	}
	r.Body.Close()
	return nil
}

// NewClientWithBase points the client at another API host (tests, GitHub Enterprise).
func NewClientWithBase(src TokenSource, base string) *Client {
	c := NewClient(src)
	c.base = base
	return c
}
