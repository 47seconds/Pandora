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

## Hooking up GitHub (zero Actions minutes)
Preferred: repo Settings -> Webhooks -> payload URL `https://<uat-host>/webhook/github`, content type
`application/json`, secret = `PANDORA_WEBHOOK_SECRET`, event "Pull requests".

If UAT must be called from a workflow instead:

    - run: |
        SIG=$(printf '%s' "$PAYLOAD" | openssl dgst -sha256 -hmac "$SECRET" | sed 's/^.* //')
        curl -fsS -X POST "$PANDORA_URL/webhook/github" -H "X-GitHub-Event: pull_request" \
          -H "X-Hub-Signature-256: sha256=$SIG" -H "Content-Type: application/json" --data-binary "$PAYLOAD"
      env: { PAYLOAD: "${{ toJSON(github.event) }}", SECRET: "${{ secrets.PANDORA_SECRET }}", PANDORA_URL: "${{ vars.PANDORA_URL }}" }

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
