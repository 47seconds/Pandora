// Package auth handles password hashing and JWT issuing/verification.
package auth

import (
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const issuer = "pandora"

type Manager struct {
	secret []byte
	ttl    time.Duration
}

func NewManager(secret string, ttl time.Duration) *Manager {
	return &Manager{secret: []byte(secret), ttl: ttl}
}

// Issue returns a signed HS256 token for the user id and its expiry.
func (m *Manager) Issue(userID int64) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(m.ttl)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer:    issuer,
		Subject:   strconv.FormatInt(userID, 10),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp),
	})
	s, err := tok.SignedString(m.secret)
	return s, exp, err
}

// Parse verifies signature, algorithm, issuer and expiry, and returns the user id.
func (m *Manager) Parse(token string) (int64, error) {
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.NewParser(
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
	).ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return m.secret, nil })
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil {
		return 0, errors.New("bad subject")
	}
	return id, nil
}

// bcrypt silently ignores bytes past 72, so reject longer passwords instead.
const (
	MinPassword = 8
	MaxPassword = 72
)

func HashPassword(p string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, p string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) == nil
}
