# AWS Setup — what it looks like

**Purpose:** describes the AWS setup the parent project (central) runs on, as the template for **crunch**. crunch goes into a **separate AWS account**, so every resource below is created fresh there (see the table at the bottom). The step-by-step commands live in `deploy/README.md` (CLI) and `deploy/readme-manual.md` (console).
**Sources:** central's `deploy/README.md`, `deploy/readme-manual.md`, `.github/workflows/ci-cd.yml`, `deploy/*`, `values/*`, `.env` (key names only) and past incident notes.
**No secrets in this file.** The account ID is shown as `<account-id>`.

---

## The picture

```
 GitHub (atharva-ng/<repo>)
   │ push to main / integration
   ▼
 GitHub Actions ──OIDC──► IAM role  github-actions-<app>-ecr
   │  test → docker build                │ inline policies: ecr-push, ssm-deploy
   │                                     ▼
   ├──────── push image ────────► ECR repo  <app>   (:latest / :integration / :<branch>-<sha>)
   │
   └── ssm:SendCommand ─────────► EC2 (Amazon Linux 2023) ── instance role <app>-server
                                   │  /home/ec2-user/<app>-deploy/deploy.sh
                                   │  docker compose pull + up  (container on 127.0.0.1:3090)
                                   │  app env: /etc/<app>/<app>.env
                                   ▼
                          ┌────────┴──────────────────────────┐
                          │ App talks to (with app IAM user keys):│
                          │  • S3 bucket (assets, presigned PUT)  │
                          │  • SQS queue + DLQ                    │
                          └───────────────────────────────────────┘
                          MongoDB Atlas (outside AWS), Clerk, LLM APIs
```

- **Region:** `ap-south-1` (Mumbai) for everything.
- **Two environments:** production and integration. They run as **two EC2 boxes** and share the ECR repo, the GitHub role and the instance role. The image tag tells them apart.

---

## Pieces, one by one

### 1. ECR (container images)
- central uses repos `central` and `central-sidecar`. **crunch needs one repo: `crunch`**, since the sidecar is dropped.
- **Tags:**
  - `main` pushes `:latest` + `:main-<sha>`
  - `integration` pushes `:integration` + `:integration-<sha>`
- **Lifecycle policy:** keep the last 20 images. It was bumped from 10, because two branches share one repo and integration pushes were expiring prod rollback tags.

### 2. IAM — GitHub Actions role (`github-actions-<app>-ecr`)
- Relies on the **GitHub OIDC provider** `token.actions.githubusercontent.com` (audience `sts.amazonaws.com`), which exists once per account. There are no long-lived keys in GitHub.
- **Trust:** only `repo:atharva-ng/<repo>:ref:refs/heads/main` and `…/integration` can assume it.
- **Inline policies:**
  - `ecr-push`: `ecr:GetAuthorizationToken` on `*`, plus push/pull actions on the repo ARN(s).
  - `ssm-deploy`: `ssm:SendCommand` on both instance ARNs + `document/AWS-RunShellScript`, and `ssm:GetCommandInvocation` on `*`.

### 3. IAM — EC2 instance role (`<app>-server`)
- Managed policies:
  - `AmazonEC2ContainerRegistryReadOnly`, for image pulls
  - `AmazonSSMManagedInstanceCore`, so the workflow can reach the box. It also gives you SSM sessions in place of SSH.
- One instance profile is attached to **both** boxes. There are no AWS keys on the box for deploys.
- ⚠️ The shell user (`ssm-user`) has **no S3 permissions**. To reach S3 from the box, borrow the app's keys from the app env file.

### 4. EC2 boxes
- **OS and tooling:** Amazon Linux 2023. Docker comes from `dnf`; the Compose v2 plugin is installed by hand to `/usr/local/lib/docker/cli-plugins`.
- **`/home/ec2-user/<app>-deploy/`** contains:
  - `deploy.sh`
  - `docker-compose.prod.yml`
  - `.env` with `AWS_REGION`, `ECR_REGISTRY=<account-id>.dkr.ecr.ap-south-1.amazonaws.com`, and on integration `IMAGE_TAG=integration`
- **App config:** `/etc/<app>/<app>.env` holds all runtime env (DB, Clerk, AWS app keys, SQS URLs, LLM keys). The image bakes in `values/`.
- **Container settings:** published only on `127.0.0.1:3090`, so a reverse proxy or LB sits in front. Memory limit 1g, json-file logs at 50m × 5, healthcheck `GET /health`.
- **Deploy flow:**
  - `deploy.sh`: ECR login via the instance role → remove stale same-name containers → `compose pull` → `up -d` → `image prune`.
  - Rollback: `IMAGE_TAG=main-<sha> ./deploy.sh`.
- central's integration box: `i-0d0254f492fa8b4cd`. The production instance ID lives in the GitHub variable `EC2_INSTANCE_ID`.

### 5. GitHub repo settings
- **Variables:** `AWS_REGION`, `EC2_INSTANCE_ID`, `EC2_INSTANCE_ID_INTEGRATION`
- **Secret:** `AWS_ROLE_ARN`, the GitHub Actions role above.
- The workflow needs `permissions: id-token: write` for OIDC.

### 6. S3 (app assets)
- **central's buckets:**
  - `central-assets-cge` for dev/integration. CORS allows origin `*`.
  - `central-prod-assets-cge` for prod. CORS allows origin `https://app.useindexly.com`.
- **CORS rule:** methods PUT/GET/HEAD, headers `*`, expose `ETag`, max-age 3000.
- ⚠️ **CORS is set by hand, not in code.** Origins **must include `https://`**. S3 matches the Origin header exactly; a missing scheme broke prod uploads on 2026-07-12.
- **Access:** the app reaches S3 with an **IAM user's access keys** (`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` in the app env), not the instance role.
- Presigned PUTs sign only the `host` header with the current SDK. That is normal.
- ⚠️ A `central-loggs` bucket does **not** exist. Log dumps go to `central-assets-cge/logs/`, and you delete them after download.

### 7. SQS (async jobs)
- **central's queues:** `central-primary-queue`, `central-secondary` (LLM work, gated by the token tracker) and `central-dlq`.
- **Consumer settings (values.yaml):**
  - long poll 20s
  - visibility timeout 600s
  - batch size 10
- **Idempotency:** the dedupe ledger TTL is 384h, which exceeds SQS's 14-day max retention.
- Also reached with the app IAM user's keys.

### 8. IAM — local/app users
- **App IAM user:** its keys go in `/etc/<app>/<app>.env` and in the local `.env`. It needs S3 read/write on the bucket and SQS send/receive/delete/change-visibility on the 3 queues.
- **Mac's default AWS profile:** IAM user `integration`. It is **low-privilege**, with no IAM/EC2/ECR read, so do admin changes in the console or with admin creds.

### 9. Outside AWS (for completeness)
- **MongoDB Atlas:** separate clusters for integration and production.
  - ⚠️ central's local `.env` points at the live integration cluster.
- **Clerk:** Clerk's webhook calls `/v1/webhooks/clerk`.

---

## crunch: what to create (fresh, don't reuse central's)

| Resource | crunch name (suggested) |
|---|---|
| ECR repo | `crunch` (+ lifecycle: keep 20) |
| GitHub OIDC provider | create it (new account — none exists yet) |
| GitHub Actions role | `github-actions-crunch-ecr` (trust: `atharva-ng/crunch` main + integration) |
| Instance role / profile | `crunch-server` |
| EC2 boxes | new prod + integration (AL2023), deploy dir `/home/ec2-user/crunch-deploy/`, env `/etc/crunch/crunch.env` |
| S3 buckets | 2 per env (D-015), see "WarehouseHub media storage" below |
| SQS | `crunch-primary`, `crunch-dlq`, per environment (set redrive to the DLQ) |
| App IAM user | `crunch-app`, scoped to only the crunch buckets + queues |
| Mongo | new Atlas clusters, DB `crunchDB` (integration + prod). Local `.env` should point at a **local** Mongo |
| GitHub vars/secrets | `AWS_REGION`, `EC2_INSTANCE_ID`, `EC2_INSTANCE_ID_INTEGRATION`, `AWS_ROLE_ARN` |

**Open:** production API domain (for `server.allowedHosts` / `CORS_ALLOWED_ORIGINS`).

---

## WarehouseHub media storage (D-015, D-062)

Two buckets per environment. **No ACLs anywhere**: Object Ownership = "Bucket owner enforced", Block Public Access = ON for both buckets.

| | integration | production | Who reads it |
|---|---|---|---|
| Public media (photos, public docs) | `warehousehub-media-public-int` | `warehousehub-media-public-prod` | Anyone, **only through CloudFront** |
| Private docs (agreement PDFs, staff-only docs) | `warehousehub-media-private-int` | `warehousehub-media-private-prod` | Staff, via 5-min presigned GET |

### Env vars (in `/etc/crunch/crunch.env`)
```
AWS_S3_PUBLIC_BUCKET=warehousehub-media-public-<env>
AWS_S3_PRIVATE_BUCKET=warehousehub-media-private-<env>
PUBLIC_MEDIA_BASE_URL=https://<cloudfront-domain>      # no trailing slash
```
Link lifetimes live in values: `storage.privateLinkSeconds` (300), `storage.uploadLinkSeconds` (900).

### CloudFront (one distribution per env, public bucket only)
1. Origin = the public bucket's REST endpoint, with **Origin Access Control** (OAC, sign requests).
2. Paste the bucket policy CloudFront generates into the public bucket (allows `s3:GetObject` for `cloudfront.amazonaws.com` with `AWS:SourceArn` = the distribution ARN). Nothing else gets read access.
3. Cache policy `CachingOptimized`; viewer protocol = redirect to HTTPS.
4. Alternate domain (e.g. `media.<domain>`) + ACM cert in us-east-1: **blocked on D-005**. Until then use the `*.cloudfront.net` domain.

### CORS (both buckets — browsers PUT straight to S3 with presigned URLs)
- Methods PUT/GET/HEAD, headers `*`, expose `ETag`, max-age 3000.
- AllowedOrigins = the **admin panel** origin(s), **with `https://`** (S3 matches the Origin header exactly; see the 2026-07-12 incident above). Integration may use `*`.

### IAM (`crunch-app` user)
`s3:PutObject`, `s3:GetObject`, `s3:DeleteObject`, `s3:ListBucket` on both buckets (HeadObject is covered by GetObject). **Do not** grant `s3:PutObjectAcl`.

### Atlas Vector Search index (placeholder, AI-08)
Created by hand in Atlas on the `warehouses` collection once AI search lands. The index JSON will live in `deploy/ATLAS_INDEXES.md` (task AI-08).
