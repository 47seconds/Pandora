// Package handler exposes the HTTP surface: GitHub webhook intake, GitHub login,
// the dashboard's JSON API and the static dashboard itself.
package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/gin-gonic/gin"

	"vocallabs.com/pandora/services/auth"
	"vocallabs.com/pandora/services/config"
	"vocallabs.com/pandora/services/github"
	"vocallabs.com/pandora/services/model"
	"vocallabs.com/pandora/services/service"
	"vocallabs.com/pandora/services/store"
)

const maxWebhookBody = 5 << 20

type Deps struct {
	Config *config.Config
	Store  *store.Store
	Svc    *service.Service
	Auth   *auth.Manager
	OAuth  *github.OAuth
	Log    *slog.Logger
}

type Server struct {
	cfg   *config.Config
	store *store.Store
	svc   *service.Service
	auth  *auth.Manager
	oauth *github.OAuth
	log   *slog.Logger
}

func New(d Deps) *gin.Engine {
	s := &Server{cfg: d.Config, store: d.Store, svc: d.Svc, auth: d.Auth, oauth: d.OAuth, log: d.Log}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	r.POST("/webhook/github", s.webhook)
	r.POST("/github/webhook", s.webhook) // alias: the URL configured on the GitHub App
	s.mountDashboard(r)

	r.GET("/auth/github/login", s.githubLogin)
	r.GET("/auth/github/callback", s.githubCallback)

	authed := r.Group("", s.requireAuth, s.csrf)
	authed.GET("/auth/me", s.me)
	authed.POST("/auth/logout", s.logout)

	// Approved accounts (user, admin). Pending accounts only get /auth/me.
	member := authed.Group("/api/v1", s.requireRole(model.RoleUser, model.RoleAdmin))
	member.GET("/me/prs", s.myPRs)
	member.GET("/me/reviews", s.myReviews)
	member.GET("/prs/:id", s.getPR)

	admin := authed.Group("/api/v1/admin", s.requireRole(model.RoleAdmin))
	admin.GET("/prs", s.adminPRs)
	admin.GET("/stats", s.adminStats)
	admin.GET("/users", s.listUsers)
	admin.PATCH("/users/:id", s.updateUser)
	admin.GET("/sync", s.syncStatus)
	admin.POST("/sync", s.startSync)
	return r
}

// webhook accepts events straight from the GitHub App's webhook, or forwarded by a
// workflow that signs the payload with the same shared secret.
func (s *Server) webhook(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBody)
	body, err := c.GetRawData()
	if err != nil {
		c.JSON(413, gin.H{"error": "body too large or unreadable"})
		return
	}
	if !github.VerifySignature(s.cfg.WebhookSecret, body, c.GetHeader("X-Hub-Signature-256")) {
		c.JSON(401, gin.H{"error": "invalid signature"})
		return
	}
	switch c.GetHeader("X-GitHub-Event") {
	case "ping":
		c.JSON(200, gin.H{"msg": "pong"})
	case "pull_request":
		var ev github.PREvent
		if err := json.Unmarshal(body, &ev); err != nil {
			c.JSON(400, gin.H{"error": "bad payload"})
			return
		}
		out, err := s.svc.HandlePullRequest(c.Request.Context(), ev)
		if err != nil {
			s.log.Error("handle pull_request", "err", err)
			c.JSON(500, gin.H{"error": "internal error"}) // GitHub/workflow may redeliver; handling is idempotent
			return
		}
		code := 200
		if out.Action == "started" {
			code = 202
		}
		c.JSON(code, out)
	default:
		c.JSON(200, gin.H{"action": "ignored"})
	}
}

// mountDashboard serves the static dashboard build at /dashboard/. Drop any built
// frontend into the static dir (it needs an index.html); no Go changes required.
//
// Caching: CDNs (Cloudflare caches .js/.css for hours by default) would otherwise keep serving
// an old app.js next to a new index.html. So index.html is rewritten on the fly to reference
// each local asset as name?v=<modtime-size>, which changes whenever the file does, and
// every response asks caches to revalidate.
func (s *Server) mountDashboard(r *gin.Engine) {
	dir := s.cfg.StaticDir
	if dir == "" {
		return
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		s.log.Warn("dashboard disabled: static dir not found", "dir", dir)
		return
	}
	r.GET("/", func(c *gin.Context) { c.Redirect(http.StatusFound, "/dashboard/") })
	fs := gin.Dir(dir, false) // false: no directory listings
	r.GET("/dashboard/*filepath", func(c *gin.Context) {
		c.Header("Content-Security-Policy", "default-src 'self'; img-src 'self' https://avatars.githubusercontent.com; frame-ancestors 'none'")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Cache-Control", "no-cache")
		p := c.Param("filepath")
		if p == "/" || p == "/index.html" {
			html, err := os.ReadFile(filepath.Join(dir, "index.html"))
			if err != nil {
				c.String(http.StatusNotFound, "dashboard not installed")
				return
			}
			c.Data(http.StatusOK, "text/html; charset=utf-8", versionAssets(dir, html))
			return
		}
		if f, err := fs.Open(p); err != nil {
			c.String(http.StatusNotFound, "not found")
			return
		} else {
			st, err := f.Stat()
			f.Close()
			if err != nil || st.IsDir() { // never serve directories, not even an empty listing
				c.String(http.StatusNotFound, "not found")
				return
			}
		}
		c.FileFromFS(p, fs)
	})
}

var assetRef = regexp.MustCompile(`(src|href)="([A-Za-z0-9_.\-]+\.(?:js|css))"`)

// versionAssets appends ?v=<modtime-size> to local .js/.css references in the page.
func versionAssets(dir string, html []byte) []byte {
	return assetRef.ReplaceAllFunc(html, func(m []byte) []byte {
		sub := assetRef.FindSubmatch(m)
		fi, err := os.Stat(filepath.Join(dir, string(sub[2])))
		if err != nil {
			return m
		}
		return []byte(fmt.Sprintf(`%s="%s?v=%s-%s"`, sub[1], sub[2],
			strconv.FormatInt(fi.ModTime().Unix(), 36), strconv.FormatInt(fi.Size(), 36)))
	})
}
