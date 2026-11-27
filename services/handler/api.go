package handler

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"vocallabs.com/pandora/services/model"
	"vocallabs.com/pandora/services/store"
)

func serverError(c *gin.Context) { c.JSON(500, gin.H{"error": "internal error"}) }

func (s *Server) myPRs(c *gin.Context) {
	u := current(c)
	if u.GitHubLogin == "" {
		c.JSON(200, []model.PRWithRun{})
		return
	}
	prs, err := s.store.ListPRs(c.Request.Context(), store.PRFilter{
		State: c.Query("state"), Author: u.GitHubLogin, Limit: atoi(c.Query("limit")), Offset: atoi(c.Query("offset"))})
	if err != nil {
		serverError(c)
		return
	}
	c.JSON(200, prs)
}

// myReviews lists open PRs where the signed-in user is a requested reviewer.
func (s *Server) myReviews(c *gin.Context) {
	u := current(c)
	if u.GitHubLogin == "" {
		c.JSON(200, []model.PRWithRun{})
		return
	}
	prs, err := s.store.ListPRs(c.Request.Context(), store.PRFilter{
		State: "open", Reviewer: u.GitHubLogin, Limit: atoi(c.Query("limit")), Offset: atoi(c.Query("offset"))})
	if err != nil {
		serverError(c)
		return
	}
	c.JSON(200, prs)
}

func canView(u *model.User, p *model.PRWithRun) bool {
	if u.Role == model.RoleAdmin {
		return true
	}
	if u.GitHubLogin == "" {
		return false
	}
	if strings.EqualFold(p.Author, u.GitHubLogin) {
		return true
	}
	for _, r := range p.Reviewers {
		if strings.EqualFold(r, u.GitHubLogin) {
			return true
		}
	}
	return false
}

// getPR returns a PR with its run history. Non-admins can only open PRs they authored
// or were asked to review; anything else is reported as not found.
func (s *Server) getPR(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "bad id"})
		return
	}
	p, err := s.store.GetPR(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !canView(current(c), p)) {
		c.JSON(404, gin.H{"error": "pull request not found"})
		return
	}
	if err != nil {
		serverError(c)
		return
	}
	runs, err := s.store.ListRuns(c.Request.Context(), id)
	if err != nil {
		serverError(c)
		return
	}
	c.JSON(200, gin.H{"pr": p, "runs": runs})
}

func (s *Server) adminPRs(c *gin.Context) {
	prs, err := s.store.ListPRs(c.Request.Context(), store.PRFilter{
		State: c.Query("state"), Author: c.Query("author"), Reviewer: c.Query("reviewer"), Repo: c.Query("repo"),
		Limit: atoi(c.Query("limit")), Offset: atoi(c.Query("offset"))})
	if err != nil {
		serverError(c)
		return
	}
	c.JSON(200, prs)
}

func (s *Server) adminStats(c *gin.Context) {
	st, err := s.store.Stats(c.Request.Context())
	if err != nil {
		s.log.Error("stats", "err", err)
		serverError(c)
		return
	}
	c.JSON(200, st)
}

func (s *Server) listUsers(c *gin.Context) {
	us, err := s.store.ListUsers(c.Request.Context())
	if err != nil {
		serverError(c)
		return
	}
	c.JSON(200, us)
}

// updateUser is how pending accounts get approved: admins set role and active.
// Identity fields come from GitHub and cannot be edited.
func (s *Server) updateUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "bad id"})
		return
	}
	var in struct {
		Role   *string `json:"role"`
		Active *bool   `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": "bad request"})
		return
	}
	if in.Role != nil && !model.ValidRole(*in.Role) {
		c.JSON(400, gin.H{"error": "role must be pending, user or admin"})
		return
	}
	// An admin cannot demote or deactivate themselves (prevents locking everyone out).
	if current(c).ID == id && (in.Role != nil || in.Active != nil) {
		c.JSON(400, gin.H{"error": "cannot change your own role or active state"})
		return
	}
	u, err := s.store.UpdateUser(c.Request.Context(), id, in.Role, in.Active)
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.JSON(404, gin.H{"error": "user not found"})
	case err != nil:
		serverError(c)
	default:
		c.JSON(200, u)
	}
}

func (s *Server) syncStatus(c *gin.Context) { c.JSON(200, s.svc.SyncStatus()) }

func (s *Server) startSync(c *gin.Context) {
	if !s.svc.SyncStatus().Enabled {
		c.JSON(400, gin.H{"error": "sync needs a GitHub App (PANDORA_GITHUB_APP_ID); a personal token cannot list org PRs"})
		return
	}
	if !s.svc.StartSync() {
		c.JSON(409, gin.H{"error": "a sync is already running"})
		return
	}
	c.JSON(202, s.svc.SyncStatus())
}
