# crunch

Go backend skeleton: auth, admin, RBAC, async jobs, cron and LLM plumbing, ready for features.
Extracted from `central` with every SEO, billing and company/teams feature removed.

## What's in the box

| Piece | Where |
|---|---|
| Clerk auth (JWT middleware, user sync webhook, lazy user creation) | `internal/middleware/jwt.go`, `cmd/service/controllers/webhooks` |
| Users + profile | `internal/services/userservice`, `GET /v1/user/profile` |
| Admin panel API + access model (roles user/admin/superuser, permissions editor/approver/attributes) + admin action log | `cmd/service/controllers/admin`, `internal/authz`, `internal/middleware/{admin,audit}.go` |
| Account deletion cascade (pluggable per feature) | `internal/services/accountService` |
| Async jobs: SQS primary + LLM-gated secondary queue, retries, DLQ, idempotency | `internal/services/asyncHandler`, `internal/providers/impl/sqs` |
| Cron: claims, catch-up, per-job kill switch in values | `internal/cron` |
| Pipeline DAG helper for multi-step jobs | `internal/pipeline` |
| LLM clients (Anthropic, OpenAI, Gemini) + token budget | `internal/providers/impl/llm`, `internal/tokentracker` |
| Image generation (OpenAI, Gemini) | `internal/providers/impl/imageGen` |
| S3, shared HTTP client | `internal/providers/impl/{s3,apiClient}` |
| Config: env vars + per-env `values/<env>/values.yaml` | `internal/config` |
| Docker, compose, ECR + SSM deploy, GitHub Actions | `Dockerfile`, `deploy/`, `.github/workflows/` |

## Adding a feature

Same layering as central (`internal/services/catalogService` is the real example):

1. **Model**: struct + indexes + queries in `internal/models/<name>.go`; ensure the indexes in `cmd/service/app_context.go`.
2. **Service** in `internal/services/<name>Service/`:
   - `service.go`: the interface, errors, request/payload types, process types
   - `store/`: a `Store` interface + thin wrapper over `models.*`
   - `service/`: the implementation (`NewService(store, deps…)`)
   - `dto/`: response shapes
3. **Wire it**: a field on `config.InternalServices`, built in `providers.InjectDefaultServices`.
4. **HTTP**: `cmd/service/controllers/<name>/{routes.go,handlers.go}`, registered in `loadAppAPIs` (`cmd/service/app-service.go`).
5. **Async / cron**: handlers in `internal/services/asyncHandler/registry.go`, jobs in `internal/cron/jobs_*.go` (dark until switched on in values). A data cleaner for account deletion goes into `InjectDefaultServices`.

WarehouseHub services: `attributeService`, `catalogService` (search, AI search, enquiries and analytics follow). Shared rules (evaluator, validators, price maths) are in `internal/warehousehub/domain`.

## Run locally

1. Put real values in `.env`. It starts with **dummy values only** and is gitignored; see `.env.example` for what each value does.
2. Make yourself superuser (sign in once through Clerk first, so your user exists):
   ```
   go run ./cmd/superuser -emails you@example.com -apply
   ```
   Other staff: they sign in, then you set their access (admin + editor/approver/attributes) from the admin panel.
3. Start the service with `go run ./cmd/service`.
4. Check it:
   - `curl localhost:3090/health`
   - `GET /v1/admin/whoami` with a Clerk JWT

## Deploy

See `deploy/README.md`. For the AWS layout, see `deploy/AWS_SETUP.md`.
