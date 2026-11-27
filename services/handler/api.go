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
	if model.IsAdmin(u.Role) {
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

type userView struct {
	model.User
	Perms Perms `json:"perms"` // what the signed-in admin may do to this account
}

func (s *Server) listUsers(c *gin.Context) {
	us, err := s.store.ListUsers(c.Request.Context())
	if err != nil {
		serverError(c)
		return
	}
	actor := current(c)
	out := make([]userView, len(us))
	for i := range us {
		out[i] = userView{User: us[i], Perms: permsFor(actor, &us[i])}
	}
	c.JSON(200, out)
}

// loadTarget fetches the account named in the URL, replying 400/404 itself on failure.
func (s *Server) loadTarget(c *gin.Context) (*model.User, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "bad id"})
		return nil, false
	}
	u, err := s.store.GetUser(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(404, gin.H{"error": "user not found"})
		return nil, false
	}
	if err != nil {
		serverError(c)
		return nil, false
	}
	return u, true
}

// denyUserChange explains why permsFor said no.
func denyUserChange(c *gin.Context, actor, target *model.User, what string) {
	switch {
	case target.Role == model.RoleSuperAdmin:
		c.JSON(403, gin.H{"error": "the super-admin account cannot be changed"})
	case actor.ID == target.ID:
		c.JSON(400, gin.H{"error": "you cannot change your own account"})
	case actor.Role == model.RoleAdmin:
		c.JSON(403, gin.H{"error": "only the super-admin can " + what + " (admins can approve pending accounts and promote users to admin)"})
	default:
		c.JSON(403, gin.H{"error": "forbidden"})
	}
}

// updateUser changes a role and/or the active flag. Identity fields come from GitHub
// and cannot be edited. What is allowed depends on who is asking: see permsFor.
func (s *Server) updateUser(c *gin.Context) {
	target, ok := s.loadTarget(c)
	if !ok {
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
	if in.Role != nil && !model.AssignableRole(*in.Role) {
		c.JSON(400, gin.H{"error": "role must be pending, user or admin"})
		return
	}
	actor := current(c)
	perms := permsFor(actor, target)
	if in.Role != nil && *in.Role != target.Role && !perms.canSetRole(*in.Role) {
		what := "demote or change the role of an admin"
		if target.Role != model.RoleAdmin {
			what = "make that role change"
		}
		denyUserChange(c, actor, target, what)
		return
	}
	if in.Active != nil && *in.Active != target.Active && !perms.Active {
		denyUserChange(c, actor, target, "deactivate or reactivate accounts")
		return
	}
	u, err := s.store.UpdateUser(c.Request.Context(), target.ID, in.Role, in.Active)
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.JSON(404, gin.H{"error": "user not found"})
	case err != nil:
		serverError(c)
	default:
		c.JSON(200, userView{User: *u, Perms: permsFor(actor, u)})
	}
}

// deleteUser removes an account; super-admin only.
func (s *Server) deleteUser(c *gin.Context) {
	target, ok := s.loadTarget(c)
	if !ok {
		return
	}
	actor := current(c)
	if !permsFor(actor, target).Delete {
		denyUserChange(c, actor, target, "delete accounts")
		return
	}
	if err := s.store.DeleteUser(c.Request.Context(), target.ID); err != nil {
		serverError(c)
		return
	}
	s.log.Info("user deleted", "by", actor.GitHubLogin, "login", target.GitHubLogin)
	c.JSON(200, gin.H{"ok": true})
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
