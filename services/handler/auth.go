package handler

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"vocallabs.com/pandora/services/model"
)

const (
	sessionCookie = "pandora_session"
	stateCookie   = "pandora_oauth_state"
	userKey       = "user"
)

func (s *Server) setCookie(c *gin.Context, name, value, path string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: maxAge,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

// githubLogin starts the OAuth flow: a random state value is bound to this browser
// through a short-lived cookie and checked again on the callback (CSRF protection).
func (s *Server) githubLogin(c *gin.Context) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		c.JSON(500, gin.H{"error": "internal error"})
		return
	}
	state := hex.EncodeToString(b)
	s.setCookie(c, stateCookie, state, "/auth/github", 600)
	c.Redirect(http.StatusFound, s.oauth.AuthorizeURL(state))
}

func (s *Server) loginFailed(c *gin.Context, code string) {
	s.setCookie(c, stateCookie, "", "/auth/github", -1)
	c.Redirect(http.StatusFound, "/dashboard/?error="+code)
}

func (s *Server) githubCallback(c *gin.Context) {
	if c.Query("error") != "" {
		s.loginFailed(c, "access_denied")
		return
	}
	want, err := c.Cookie(stateCookie)
	if err != nil || want == "" || c.Query("state") != want || c.Query("code") == "" {
		s.loginFailed(c, "invalid_state")
		return
	}
	profile, err := s.oauth.Exchange(c.Request.Context(), c.Query("code"))
	if err != nil {
		s.log.Warn("github login failed", "err", err)
		s.loginFailed(c, "github_failed")
		return
	}
	u, created, err := s.store.UpsertGitHubUser(c.Request.Context(), *profile, s.cfg.BootstrapAdmin)
	if err != nil {
		s.log.Error("upsert user", "err", err)
		s.loginFailed(c, "server_error")
		return
	}
	if !u.Active {
		s.loginFailed(c, "account_disabled")
		return
	}
	tok, _, err := s.auth.Issue(u.ID)
	if err != nil {
		s.loginFailed(c, "server_error")
		return
	}
	if created {
		s.log.Info("new user awaiting approval", "login", u.GitHubLogin)
	}
	s.setCookie(c, stateCookie, "", "/auth/github", -1)
	s.setCookie(c, sessionCookie, tok, "/", int(s.auth.TTL().Seconds()))
	c.Redirect(http.StatusFound, "/dashboard/")
}

// requireAuth validates the session cookie and loads the user from the DB on every
// request, so deactivation and role changes take effect immediately.
func (s *Server) requireAuth(c *gin.Context) {
	tok, err := c.Cookie(sessionCookie)
	if err != nil || tok == "" {
		c.AbortWithStatusJSON(401, gin.H{"error": "not signed in"})
		return
	}
	id, err := s.auth.Parse(tok)
	if err != nil {
		c.AbortWithStatusJSON(401, gin.H{"error": "invalid or expired session"})
		return
	}
	u, err := s.store.GetUser(c.Request.Context(), id)
	if err != nil || !u.Active {
		c.AbortWithStatusJSON(401, gin.H{"error": "invalid or expired session"})
		return
	}
	c.Set(userKey, u)
	c.Next()
}

// csrf guards state-changing requests. Cookies alone are not enough proof of intent,
// so require a custom header: a cross-site page cannot set it without a CORS preflight,
// which Pandora never allows. (SameSite=Lax on the cookie is the first line of defence.)
func (s *Server) csrf(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		c.Next()
		return
	}
	if c.GetHeader("X-Pandora-CSRF") != "1" {
		c.AbortWithStatusJSON(403, gin.H{"error": "missing CSRF header"})
		return
	}
	c.Next()
}

func (s *Server) requireRole(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := c.MustGet(userKey).(*model.User)
		for _, r := range roles {
			if u.Role == r {
				c.Next()
				return
			}
		}
		msg := "forbidden"
		if u.Role == model.RolePending {
			msg = "your account is awaiting approval by an admin"
		}
		c.AbortWithStatusJSON(403, gin.H{"error": msg})
	}
}

func (s *Server) me(c *gin.Context) { c.JSON(200, c.MustGet(userKey)) }

func (s *Server) logout(c *gin.Context) {
	s.setCookie(c, sessionCookie, "", "/", -1)
	c.JSON(200, gin.H{"ok": true})
}

func current(c *gin.Context) *model.User { return c.MustGet(userKey).(*model.User) }

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }
