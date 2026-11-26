// Package review performs the AI review of a PR against its description.
package review

import (
	"context"
	"fmt"
	"strings"
)

const (
	VerdictPass       = "pass"
	VerdictNeedsHuman = "needs_human"
	VerdictBlock      = "block"
)

type Input struct {
	Repo      string
	Number    int
	Title     string
	Body      string
	Author    string
	Base      string
	Head      string
	Diff      string
	Truncated bool // diff was cut to fit the size limit
}

type Issue struct {
	Severity    string `json:"severity"` // critical | major | minor
	File        string `json:"file"`
	Description string `json:"description"`
}

type Result struct {
	MatchesDescription bool     `json:"matches_description"`
	Verdict            string   `json:"verdict"`
	Summary            string   `json:"summary"`
	UnrelatedChanges   []string `json:"unrelated_changes"`
	Issues             []Issue  `json:"issues"`
	MissingTests       bool     `json:"missing_tests"`
	EnvVarChanges      []string `json:"env_var_changes"`
	HasMigrations      bool     `json:"has_migrations"`
}

type Reviewer interface {
	Review(ctx context.Context, in Input) (*Result, error)
}

// ApplyPolicy enforces rules the model cannot be trusted to follow on its own.
// It only ever makes the verdict stricter.
func ApplyPolicy(in Input, r *Result) {
	switch r.Verdict {
	case VerdictPass, VerdictNeedsHuman, VerdictBlock:
	default:
		r.Verdict = VerdictNeedsHuman
	}
	for _, i := range r.Issues {
		if strings.EqualFold(i.Severity, "critical") {
			r.Verdict = VerdictBlock
		}
	}
	if r.Verdict != VerdictPass {
		return
	}
	switch {
	case !r.MatchesDescription:
		r.Verdict = VerdictNeedsHuman
	case len(r.UnrelatedChanges) > 0:
		r.Verdict = VerdictNeedsHuman
	case in.Truncated:
		r.Verdict = VerdictNeedsHuman
		r.Summary += " (Diff was too large and truncated; human review required.)"
	}
}

const marker = "<!-- pandora-ai-review -->"

// Marker identifies Pandora's review comment so it can be updated in place.
func Marker() string { return marker }

// Markdown renders a review result as a PR comment.
func Markdown(sha string, r *Result) string {
	var b strings.Builder
	b.WriteString(marker + "\n")
	icon := map[string]string{VerdictPass: "✅", VerdictNeedsHuman: "⚠️", VerdictBlock: "⛔"}[r.Verdict]
	fmt.Fprintf(&b, "## %s Pandora AI Review: `%s`\n", icon, r.Verdict)
	fmt.Fprintf(&b, "Commit `%s` · matches description: **%v**\n\n%s\n", short(sha), r.MatchesDescription, r.Summary)
	if len(r.UnrelatedChanges) > 0 {
		b.WriteString("\n**Unrelated changes**\n")
		for _, u := range r.UnrelatedChanges {
			fmt.Fprintf(&b, "- %s\n", u)
		}
	}
	if len(r.Issues) > 0 {
		b.WriteString("\n**Issues**\n")
		for _, i := range r.Issues {
			fmt.Fprintf(&b, "- **%s** `%s`: %s\n", i.Severity, i.File, i.Description)
		}
	}
	if len(r.EnvVarChanges) > 0 {
		b.WriteString("\n**Env var changes**: " + strings.Join(r.EnvVarChanges, ", ") + "\n")
	}
	if r.HasMigrations {
		b.WriteString("\n**Database migrations detected**\n")
	}
	if r.MissingTests {
		b.WriteString("\n_No tests accompany these changes._\n")
	}
	return b.String()
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
