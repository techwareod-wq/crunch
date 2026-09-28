# crunch

Go backend skeleton: auth, admin, RBAC, async jobs, cron and LLM plumbing, ready for features.
Extracted from `central` with every SEO, billing and company/teams feature removed.

## What's in the box

| Piece | Where |
|---|---|
| Clerk auth (JWT middleware, user sync webhook, lazy user creation) | `internal/middleware/jwt.go`, `cmd/service/controllers/webhooks` |
| Users + profile | `internal/services/userservice`, `GET /v1/user/profile` |
| Admin panel API + DB-backed RBAC roles + admin action log | `cmd/service/controllers/admin`, `internal/authz`, `internal/middleware/{admin,audit}.go` |
| Account deletion cascade (pluggable per feature) | `internal/services/accountService` |
| Async jobs: SQS primary + LLM-gated secondary queue, retries, DLQ, idempotency | `internal/services/asyncHandler`, `internal/providers/impl/sqs` |
| Cron: claims, catch-up, per-job kill switch in values | `internal/cron` |
| Pipeline DAG helper for multi-step jobs | `internal/pipeline` |
| LLM clients (Anthropic, OpenAI, Gemini) + token budget | `internal/providers/impl/llm`, `internal/tokentracker` |
| Image generation (OpenAI, Gemini) | `internal/providers/impl/imageGen` |
| S3, SMTP mailer, shared HTTP client | `internal/providers/impl/{s3,mailer,apiClient}` |
| Config: env vars + per-env `values/<env>/values.yaml` | `internal/config` |
| Docker, compose, ECR + SSM deploy, GitHub Actions | `Dockerfile`, `deploy/`, `.github/workflows/` |

## Adding a feature

A feature is a **module** (`internal/modules`). It can add:

- async handlers (and say which ones go on the LLM queue)
- cron jobs
- HTTP routes
- a data cleaner for account deletion

Copy `internal/modules/demo`, then add one line to `cmd/service/modules.go`. You don't need to edit any platform package.

The demo module is a working example you can delete:

- **`POST /v1/admin/demo/dispatch`** (requires `cron.manage`) with `{"text": "..."}`: enqueues a `DEMO_SUMMARIZE` job, which asks the default LLM for a one-line summary and logs it.
- **`demo_heartbeat` cron job**: does the same once a day. It is off until you set `cron.jobs.demo_heartbeat.enabled: true`.

## Run locally

1. Put real values in `.env`. It starts with **dummy values only** and is gitignored; see `.env.example` for what each value does.
2. Seed the roles and your first superuser (sign in once through Clerk first, so your user exists):
   ```
   go run ./cmd/rolesmigrate -seed-roles
   go run ./cmd/rolesmigrate -seed-admins -emails you@example.com
   ```
3. Start the service with `go run ./cmd/service`.
4. Check it:
   - `curl localhost:3090/health`
   - `GET /v1/admin/whoami` with a Clerk JWT

## Deploy

See `deploy/README.md`. For the AWS layout, see `deploy/AWS_SETUP.md`.
