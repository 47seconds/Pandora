// Package model holds the domain types shared across Pandora.
package model

// Run statuses. A run is one pipeline execution for (PR, head SHA, description).
// Only the newest run of a PR is live; older ones become StatusSuperseded.
const (
	StatusQueued     = "queued"
	StatusAIReview   = "ai_review"
	StatusAIPassed   = "ai_passed"   // AI review clean, ready for the next phase (tests)
	StatusNeedsHuman = "needs_human" // mismatch/uncertain: a reviewer must decide
	StatusBlocked    = "blocked"     // critical issues found
	StatusError      = "error"       // review itself failed
	StatusSuperseded = "superseded"  // newer commit/edit replaced this run
)

type PR struct {
	ID        int64  `json:"id"`
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	State     string `json:"state"` // open | closed
	HeadSHA   string `json:"head_sha"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Run struct {
	ID        int64  `json:"id"`
	PRID      int64  `json:"pr_id"`
	SHA       string `json:"sha"`
	DescHash  string `json:"desc_hash"`
	Status    string `json:"status"`
	Verdict   string `json:"verdict"`
	Report    string `json:"report"` // JSON
	Error     string `json:"error"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type PRWithRun struct {
	PR
	LatestRun *Run `json:"latest_run,omitempty"`
}

// Roles.
const (
	RoleAdmin     = "admin"
	RoleReviewer  = "reviewer"
	RoleDeveloper = "developer"
)

type User struct {
	ID           int64  `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"`
	GitHubLogin  string `json:"github_login"`
	Active       bool   `json:"active"`
	CreatedAt    string `json:"created_at"`
	LastLoginAt  string `json:"last_login_at"`
}

func ValidRole(r string) bool {
	return r == RoleAdmin || r == RoleReviewer || r == RoleDeveloper
}
