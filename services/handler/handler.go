// Package handler exposes the HTTP surface: GitHub webhook intake and a read-only admin API.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
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

type Server struct {
	cfg   *config.Config
	store *store.Store
	svc   *service.Service
	auth  *auth.Manager
	log   *slog.Logger
}

func New(cfg *config.Config, st *store.Store, o *service.Service, am *auth.Manager, log *slog.Logger) *gin.Engine {
	s := &Server{cfg: cfg, store: st, svc: o, auth: am, log: log}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	r.POST("/webhook/github", s.webhook)

	r.POST("/auth/signup", s.signup)
	r.POST("/auth/login", s.login)

	authed := r.Group("", s.requireAuth)
	authed.GET("/auth/me", s.me)

	v1 := authed.Group("/api/v1")
	v1.GET("/prs", s.listPRs)
	v1.GET("/prs/:id/runs", s.listRuns)
	admin := v1.Group("", s.requireRole(model.RoleAdmin))
	admin.GET("/users", s.listUsers)
	admin.PATCH("/users/:id", s.updateUser)
	return r
}

// webhook accepts events straight from a GitHub webhook, or forwarded by a
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

func (s *Server) listPRs(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	prs, err := s.store.ListPRs(c.Request.Context(), c.Query("state"), limit)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, prs)
}

func (s *Server) listRuns(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "bad id"})
		return
	}
	runs, err := s.store.ListRuns(c.Request.Context(), id)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, runs)
}
