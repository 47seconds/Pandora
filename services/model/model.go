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
	ID          int64    `json:"id"`
	Repo        string   `json:"repo"`
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	Author      string   `json:"author"` // GitHub login
	State       string   `json:"state"`  // open | closed
	HeadSHA     string   `json:"head_sha"`
	BaseRef     string   `json:"base_ref"`
	Draft       bool     `json:"draft"`
	Merged      bool     `json:"merged"`
	GHCreatedAt string   `json:"gh_created_at"`
	GHUpdatedAt string   `json:"gh_updated_at"`
	ClosedAt    string   `json:"closed_at"`
	MergedAt    string   `json:"merged_at"`
	CreatedAt   string   `json:"created_at"` // when Pandora first saw it
	UpdatedAt   string   `json:"updated_at"`
	Reviewers   []string `json:"reviewers"` // requested reviewers (GitHub logins)
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

// Roles: new accounts are pending until an admin approves them. The superadmin is the
// single primary owner (the PANDORA_BOOTSTRAP_ADMIN_GITHUB account, or the first ever
// sign-in when that is unset); it can never be assigned through the API.
const (
	RolePending    = "pending"
	RoleUser       = "user"
	RoleAdmin      = "admin"
	RoleSuperAdmin = "superadmin"
)

// AssignableRole reports whether an admin may set this role on someone.
func AssignableRole(r string) bool { return r == RolePending || r == RoleUser || r == RoleAdmin }

// IsAdmin is true for admins and the superadmin.
func IsAdmin(role string) bool { return role == RoleAdmin || role == RoleSuperAdmin }

type User struct {
	ID          int64  `json:"id"`
	GitHubID    int64  `json:"github_id"`
	GitHubLogin string `json:"github_login"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	AvatarURL   string `json:"avatar_url"`
	Role        string `json:"role"`
	Active      bool   `json:"active"`
	CreatedAt   string `json:"created_at"`
	LastLoginAt string `json:"last_login_at"`
}

// GitHubProfile is what GitHub tells us about a user at sign-in.
type GitHubProfile struct {
	ID        int64
	Login     string
	Name      string
	Email     string
	AvatarURL string
}

// Stats feeds the admin overview.
type Stats struct {
	Open            int            `json:"open"`
	Closed          int            `json:"closed"` // closed without merging
	Merged          int            `json:"merged"`
	Drafts          int            `json:"drafts"` // open drafts
	ByAIStatus      map[string]int `json:"by_ai_status"`
	TopAuthors      []Count        `json:"top_authors"`
	AwaitingReview  []Count        `json:"awaiting_review"` // open PRs waiting on each reviewer
	ByRepo          []Count        `json:"by_repo"`
	Weekly          []WeekCount    `json:"weekly"`
	AvgHoursToMerge float64        `json:"avg_hours_to_merge"`
}

type Count struct {
	Name  string `json:"name"`
	Open  int    `json:"open"`
	Total int    `json:"total"`
}

type WeekCount struct {
	Week   string `json:"week"` // Monday, YYYY-MM-DD
	Opened int    `json:"opened"`
	Merged int    `json:"merged"`
}
