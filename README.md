# Pandora: PR review service (Phase 1)

Runs on the UAT server. GitHub only sends it events; all review work happens here.

**Phase 1 scope:** PR raised/updated -> AI review -> result posted back. Nothing after that yet
(tests, approval, deploy come in later phases).

## Layout
    main.go                 entrypoint (wiring + graceful shutdown)
    services/
      config/               env config
      model/                domain types: PR, Run, statuses
      store/                SQLite persistence + migrations/ (versioned .sql schema)
      auth/                 JWT + password hashing
      handler/              gin routes + HTTP handlers (webhook, admin API)
      service/              pipeline logic (supersede, run lifecycle)
      github/               webhook verification + GitHub REST client
      review/               AI reviewer (anthropic, mock) + verdict policy
    static/                 dashboard (plain HTML/CSS/JS, no build); served at /dashboard/
    sources/<service>/      per-service test definitions (smoke_test, basic_test, feature_test)
                            forge, citadel, eva scaffolded; filled in the testing phase

## Flow
1. `POST /webhook/github` receives a `pull_request` event (HMAC-verified).
2. PR is upserted; a **run** is created, keyed by `(PR, head SHA, description hash)`. Duplicates are no-ops.
3. Older runs of that PR are marked `superseded` and their in-flight review is cancelled.
4. Review fetches the PR + diff from GitHub, asks the model, then `ApplyPolicy` can only tighten the verdict
   (critical issue -> block, mismatch/unrelated/truncated diff -> needs_human, empty description -> needs_human).
5. Result is stored, posted as one updating PR comment, and set as commit status `pandora/ai-review`.

Run statuses: `queued -> ai_review -> ai_passed | needs_human | blocked | error`, or `superseded`.
Closing a PR supersedes all its runs. Runs interrupted by a restart are resumed on boot.

## Run
    cp .env.example .env   # fill in, then export the vars
    go run .

## Hooking up GitHub (workflow)
Each service repo gets `.github/workflows/ai-review.yaml`; copy it from
`templates/github/workflows/ai-review.yaml`. It does no work itself: it signs the PR event and POSTs it to
`/webhook/github`. Pandora does the review and reports back with a PR comment and the `pandora/ai-review` status.

Per repo set `vars.PANDORA_URL` and `secrets.PANDORA_WEBHOOK_SECRET` (same value as `PANDORA_WEBHOOK_SECRET`
on the server). The UAT URL must be reachable from GitHub's runners.

Cost note: a GitHub-hosted runner bills at least ~1 minute per run. To make it free, run this one job on a
self-hosted runner on the UAT box (`runs-on: self-hosted`), or use a direct repo webhook to the same endpoint
(no workflow at all). The endpoint accepts both.

## Dashboard
Open `/dashboard/` (`/` redirects there). Log in with a Pandora account: PR list with live status
(auto-refreshes every 15s), per-PR review history with the AI report, and a Users page for admins
(roles, GitHub login, deactivate). It is static files in `static/` calling the API above; the page itself is
public but all data needs the JWT. To use a real frontend later, put its build output in `static/`
(must contain `index.html`, relative asset paths) or point `PANDORA_STATIC_DIR` elsewhere.
Strict CSP is set (no inline scripts/styles), so a build must not rely on them.

## AI provider
`PANDORA_REVIEW_PROVIDER`: `anthropic` (needs `ANTHROPIC_API_KEY`, `sk-ant-...`), `openrouter`
(needs `OPENROUTER_API_KEY`, `sk-or-...`; model ids look like `anthropic/claude-sonnet-5.5`), or `mock`
(approves everything; wiring tests only). An OpenRouter key placed under `ANTHROPIC_API_KEY` is rejected at startup with a hint.

## GitHub App (how Pandora talks to GitHub)
Pandora authenticates as a GitHub App, not with a personal token. For every repo it looks up the app's
installation and mints a short-lived (1h) token limited to that single repo, cached and refreshed automatically.
Nothing long-lived is used for API calls, and access is exactly the repos where the app is installed.

Create it once (GitHub org -> Settings -> Developer settings -> GitHub Apps -> New):
- **Webhook:** uncheck "Active" (events arrive via the workflow below). Or enable it with URL
  `https://<pandora>/webhook/github` + the same secret as `PANDORA_WEBHOOK_SECRET` and subscribe to
  "Pull request", and then the workflow is optional.
- **Repository permissions:** Pull requests: Read & write; Commit statuses: Read & write; Metadata: Read.
  (Later phases will need more, e.g. Contents: Read to run tests, Checks. Changing permissions makes
  GitHub ask the org to re-approve.)
- Generate a **private key** (.pem) and note the **App ID**. Install the app on the repos to review.
- Set `PANDORA_GITHUB_APP_ID` and `PANDORA_GITHUB_APP_PRIVATE_KEY` (or `_FILE`). The key as a single line:
  `awk 'NF {printf "%s\\n", $0}' key.pem`

If the app isn't installed on a repo, events for it fail with "github app is not installed on this repo".
`PANDORA_GITHUB_TOKEN` still works as a dev fallback when no App is set (a warning is logged).

## Database and schema
SQLite file (`PANDORA_DB_PATH`, `/data/pandora.db` in Docker, on the `pandora_data` volume). Back it up by copying
the file (use `sqlite3 pandora.db ".backup out.db"` while running).
The schema lives in `services/store/migrations/NNNN_name.sql`, embedded in the binary and applied on startup.
To change it, **add a new file** (e.g. `0002_add_stage_results.sql`); never edit one that has shipped.
Tables: `prs`, `runs`, `events`, `users`, `schema_migrations`.

## Auth
JWT (HS256, bcrypt passwords). Roles: `admin`, `reviewer`, `developer`.
- `POST /auth/signup` `{email,name,password}` -> token. First signup (or `PANDORA_BOOTSTRAP_ADMIN_EMAIL`) is admin; others are developers.
- `POST /auth/login` `{email,password}` -> `{token, expires_at, user}`
- `GET /auth/me`
- Send `Authorization: Bearer <token>`. The user is re-checked in the DB on each request, so deactivation and role changes apply instantly.

## API (JWT required)
- `GET /api/v1/prs?state=open`, `GET /api/v1/prs/:id/runs`: any role
- `GET /api/v1/users`, `PATCH /api/v1/users/:id` `{role, github_login, active}`: admin only

Not built yet: login rate limiting, password reset, refresh tokens/logout (tokens just expire).
