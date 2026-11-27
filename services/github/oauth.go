package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"vocallabs.com/pandora/services/model"
)

// OAuth implements "Sign in with GitHub" using the GitHub App's user authorization.
// The user's GitHub token is used once to read their profile and is then discarded.
type OAuth struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	WebBase      string // https://github.com
	APIBase      string // https://api.github.com
	http         *http.Client
}

func NewOAuth(clientID, secret, redirectURL string) *OAuth {
	return &OAuth{ClientID: clientID, ClientSecret: secret, RedirectURL: redirectURL,
		WebBase: "https://github.com", APIBase: DefaultBase, http: &http.Client{Timeout: 20 * time.Second}}
}

func (o *OAuth) AuthorizeURL(state string) string {
	q := url.Values{"client_id": {o.ClientID}, "redirect_uri": {o.RedirectURL}, "state": {state}}
	return o.WebBase + "/login/oauth/authorize?" + q.Encode()
}

func (o *OAuth) getJSON(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", o.APIBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", jsonAccept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := o.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("github GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// Exchange trades the callback code for the user's profile.
func (o *OAuth) Exchange(ctx context.Context, code string) (*model.GitHubProfile, error) {
	form := url.Values{"client_id": {o.ClientID}, "client_secret": {o.ClientSecret}, "code": {code}, "redirect_uri": {o.RedirectURL}}
	req, err := http.NewRequestWithContext(ctx, "POST", o.WebBase+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&tok); err != nil {
		return nil, err
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("github token exchange failed: %s %s", tok.Error, tok.Description)
	}

	var u struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := o.getJSON(ctx, tok.AccessToken, "/user", &u); err != nil {
		return nil, err
	}
	if u.ID == 0 || u.Login == "" {
		return nil, errors.New("github returned no user")
	}
	p := &model.GitHubProfile{ID: u.ID, Login: u.Login, Name: u.Name, Email: u.Email, AvatarURL: u.AvatarURL}
	if p.Email == "" { // private email: needs the app's "Email addresses: read" permission; optional
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if err := o.getJSON(ctx, tok.AccessToken, "/user/emails", &emails); err == nil {
			for _, e := range emails {
				if e.Primary && e.Verified {
					p.Email = e.Email
					break
				}
			}
		}
	}
	return p, nil
}
