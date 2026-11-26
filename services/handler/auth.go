package handler

import (
	"errors"
	"net/mail"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"vocallabs.com/pandora/services/auth"
	"vocallabs.com/pandora/services/model"
	"vocallabs.com/pandora/services/store"
)

const userKey = "user"

// requireAuth validates the bearer JWT and loads the user from the DB on every
// request, so deactivation and role changes take effect immediately.
func (s *Server) requireAuth(c *gin.Context) {
	h := c.GetHeader("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || tok == "" {
		c.AbortWithStatusJSON(401, gin.H{"error": "missing bearer token"})
		return
	}
	id, err := s.auth.Parse(tok)
	if err != nil {
		c.AbortWithStatusJSON(401, gin.H{"error": "invalid or expired token"})
		return
	}
	u, err := s.store.GetUser(c.Request.Context(), id)
	if err != nil || !u.Active {
		c.AbortWithStatusJSON(401, gin.H{"error": "invalid or expired token"})
		return
	}
	c.Set(userKey, u)
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
		c.AbortWithStatusJSON(403, gin.H{"error": "forbidden"})
	}
}

type credentials struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (s *Server) signup(c *gin.Context) {
	var in credentials
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": "bad request"})
		return
	}
	email, err := normalizeEmail(in.Email)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid email"})
		return
	}
	if len(s.cfg.SignupDomains) > 0 && !s.cfg.SignupDomains[email[strings.LastIndex(email, "@")+1:]] {
		c.JSON(403, gin.H{"error": "signup is not open for this email domain"})
		return
	}
	if n := len(in.Password); n < auth.MinPassword || n > auth.MaxPassword {
		c.JSON(400, gin.H{"error": "password must be " + strconv.Itoa(auth.MinPassword) + "-" + strconv.Itoa(auth.MaxPassword) + " bytes"})
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		c.JSON(500, gin.H{"error": "internal error"})
		return
	}
	u, err := s.store.CreateUser(c.Request.Context(), email, strings.TrimSpace(in.Name), hash, s.cfg.AdminEmail)
	if errors.Is(err, store.ErrEmailTaken) {
		c.JSON(409, gin.H{"error": "email already registered"})
		return
	}
	if err != nil {
		s.log.Error("create user", "err", err)
		c.JSON(500, gin.H{"error": "internal error"})
		return
	}
	s.respondToken(c, 201, u)
}

// dummyHash lets login take the same time whether or not the email exists.
var (
	dummyOnce sync.Once
	dummyHash string
)

func (s *Server) login(c *gin.Context) {
	var in credentials
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": "bad request"})
		return
	}
	email, _ := normalizeEmail(in.Email)
	u, err := s.store.GetUserByEmail(c.Request.Context(), email)
	if err != nil {
		dummyOnce.Do(func() { dummyHash, _ = auth.HashPassword("pandora-dummy-password") })
		auth.CheckPassword(dummyHash, in.Password)
		c.JSON(401, gin.H{"error": "invalid credentials"})
		return
	}
	if !auth.CheckPassword(u.PasswordHash, in.Password) || !u.Active {
		c.JSON(401, gin.H{"error": "invalid credentials"})
		return
	}
	s.store.TouchLogin(c.Request.Context(), u.ID)
	s.respondToken(c, 200, u)
}

func (s *Server) respondToken(c *gin.Context, code int, u *model.User) {
	tok, exp, err := s.auth.Issue(u.ID)
	if err != nil {
		c.JSON(500, gin.H{"error": "internal error"})
		return
	}
	c.JSON(code, gin.H{"token": tok, "expires_at": exp, "user": u})
}

func (s *Server) me(c *gin.Context) { c.JSON(200, c.MustGet(userKey)) }

func (s *Server) listUsers(c *gin.Context) {
	us, err := s.store.ListUsers(c.Request.Context())
	if err != nil {
		c.JSON(500, gin.H{"error": "internal error"})
		return
	}
	c.JSON(200, us)
}

func (s *Server) updateUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "bad id"})
		return
	}
	var in struct {
		Role        *string `json:"role"`
		GitHubLogin *string `json:"github_login"`
		Active      *bool   `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": "bad request"})
		return
	}
	if in.Role != nil && !model.ValidRole(*in.Role) {
		c.JSON(400, gin.H{"error": "role must be admin, reviewer or developer"})
		return
	}
	// An admin cannot demote or deactivate themselves (prevents locking everyone out).
	if me := c.MustGet(userKey).(*model.User); me.ID == id && (in.Role != nil || in.Active != nil) {
		c.JSON(400, gin.H{"error": "cannot change your own role or active state"})
		return
	}
	if in.GitHubLogin != nil {
		g := strings.TrimSpace(*in.GitHubLogin)
		in.GitHubLogin = &g
	}
	u, err := s.store.UpdateUser(c.Request.Context(), id, in.Role, in.GitHubLogin, in.Active)
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.JSON(404, gin.H{"error": "user not found"})
	case errors.Is(err, store.ErrConflict):
		c.JSON(409, gin.H{"error": "github_login already linked to another user"})
	case err != nil:
		c.JSON(500, gin.H{"error": "internal error"})
	default:
		c.JSON(200, u)
	}
}

func normalizeEmail(raw string) (string, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil || a.Address != strings.TrimSpace(raw) {
		return "", errors.New("invalid email")
	}
	return strings.ToLower(a.Address), nil
}
