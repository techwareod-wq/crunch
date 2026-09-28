# Manual Platform Setup Guide

Step-by-step instructions for setting up the deployment pipeline **by hand through each platform's web console** — no admin AWS CLI required locally. This is the UI-equivalent of the CLI commands in `README.md`; do the sections in order.

The end state:

```
merge to main → GitHub Actions: test → build → push to ECR → SSM runs deploy.sh on the EC2 instance
```

You will touch four places:

| # | Platform | What you set up |
|---|----------|-----------------|
| 1 | AWS ECR | Two image repositories + lifecycle policies |
| 2 | AWS IAM | GitHub OIDC provider, a role for GitHub Actions, a role for the EC2 instance |
| 3 | GitHub | Repo Actions variables + secret |
| 4 | EC2 server | Docker + Compose, the deploy directory, env files |

Before you start, note down three values — you'll need them repeatedly:

- **AWS region** — e.g. `ap-south-1`
- **AWS account ID** — 12-digit number, shown in the AWS console under your account menu (top-right)
- **EC2 instance ID** — e.g. `i-0abc123...`, from EC2 → Instances

---

## 1. AWS ECR — image repositories

Console → search **ECR** → **Elastic Container Registry**. Make sure the region selector (top-right) shows your region.

### 1.1 Create the two repositories

1. **Private registry → Repositories → Create repository**
2. Repository name: `central`
3. Leave everything else at defaults (mutable tags, AES-256 encryption) → **Create repository**
4. Repeat for a second repository named `central-sidecar`

### 1.2 Lifecycle policy (recommended — keep last 10 images)

For **each** of the two repositories:

1. Click the repository name → left sidebar **Lifecycle policy** → **Create rule**
2. Rule priority: `1`
3. Rule description: `keep last 10`
4. Image status: **Any**
5. Match criteria: **Image count more than** → `10`
6. **Save**

### 1.3 Note the registry URL

On the Repositories list, each repo's URI looks like:

```
<account-id>.dkr.ecr.<region>.amazonaws.com/central
```

The part before `/central` — `<account-id>.dkr.ecr.<region>.amazonaws.com` — is your **ECR registry URL**. You'll need it for the server's `.env` in step 4.4.

---

## 2. AWS IAM — identities and permissions

Console → search **IAM**. (IAM is global; the region doesn't matter here.)

### 2.1 GitHub OIDC identity provider

Lets GitHub Actions authenticate to AWS with short-lived tokens instead of stored keys. **Skip this if your account already has a `token.actions.githubusercontent.com` provider** (check IAM → Identity providers).

1. IAM → **Identity providers** → **Add provider**
2. Provider type: **OpenID Connect**
3. Provider URL: `https://token.actions.githubusercontent.com`
4. Audience: `sts.amazonaws.com`
5. **Add provider**

### 2.2 Role for GitHub Actions (`github-actions-central-ecr`)

This role is what the workflow assumes to push images and trigger the deploy.

1. IAM → **Roles** → **Create role**
2. Trusted entity type: **Web identity**
3. Identity provider: `token.actions.githubusercontent.com`
4. Audience: `sts.amazonaws.com`
5. GitHub organization: `atharva-ng`, repository: `central`, branch: `main` (this restricts the role so only the `main` branch of that repo can assume it)
6. **Next** — don't attach any policies yet → **Next**
7. Role name: `github-actions-central-ecr` → **Create role**

Open the created role → **Trust relationships** tab and verify it matches this (replace `<ACCOUNT_ID>`; edit via **Edit trust policy** if the wizard produced something looser):

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Federated": "arn:aws:iam::<ACCOUNT_ID>:oidc-provider/token.actions.githubusercontent.com"},
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {
      "StringEquals": {"token.actions.githubusercontent.com:aud": "sts.amazonaws.com"},
      "StringLike": {"token.actions.githubusercontent.com:sub": "repo:atharva-ng/central:ref:refs/heads/main"}
    }
  }]
}
```

Now add two **inline policies** (role page → **Permissions** tab → **Add permissions** → **Create inline policy** → **JSON** tab → paste → name it → **Create policy**):

**Inline policy 1 — name it `ecr-push`** (replace `<AWS_REGION>` and `<ACCOUNT_ID>`):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {"Effect": "Allow", "Action": "ecr:GetAuthorizationToken", "Resource": "*"},
    {"Effect": "Allow",
     "Action": ["ecr:BatchCheckLayerAvailability", "ecr:CompleteLayerUpload",
                "ecr:InitiateLayerUpload", "ecr:PutImage", "ecr:UploadLayerPart",
                "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer"],
     "Resource": ["arn:aws:ecr:<AWS_REGION>:<ACCOUNT_ID>:repository/central",
                  "arn:aws:ecr:<AWS_REGION>:<ACCOUNT_ID>:repository/central-sidecar"]}
  ]
}
```

**Inline policy 2 — name it `ssm-deploy`** (replace `<AWS_REGION>`, `<ACCOUNT_ID>`, `<INSTANCE_ID>`):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {"Effect": "Allow", "Action": "ssm:SendCommand",
     "Resource": ["arn:aws:ec2:<AWS_REGION>:<ACCOUNT_ID>:instance/<INSTANCE_ID>",
                  "arn:aws:ssm:<AWS_REGION>::document/AWS-RunShellScript"]},
    {"Effect": "Allow", "Action": "ssm:GetCommandInvocation", "Resource": "*"}
  ]
}
```

Finally, copy the role's **ARN** from the top of the role page (`arn:aws:iam::<ACCOUNT_ID>:role/github-actions-central-ecr`) — you'll paste it into GitHub in step 3.

### 2.3 Instance role for the EC2 server (`central-server`)

Lets the server pull from ECR and be reached by SSM — no AWS keys on the box.

**If the instance already has an IAM role attached:** skip creating a new one — open that existing role and attach the two managed policies from step 4 below, then jump to section 3.

1. IAM → **Roles** → **Create role**
2. Trusted entity type: **AWS service** → Use case: **EC2** → **Next**
3. Search and tick both:
    - `AmazonEC2ContainerRegistryReadOnly` (image pulls)
    - `AmazonSSMManagedInstanceCore` (lets the workflow's SSM step reach the instance)
4. **Next** → Role name: `central-server` → **Create role** (the console creates the matching instance profile automatically)

**Attach it to the instance:**

1. EC2 console → **Instances** → select your instance
2. **Actions → Security → Modify IAM role**
3. Choose `central-server` → **Update IAM role**

The SSM agent is preinstalled and running on Amazon Linux 2023, so a few minutes after attaching the role the instance should appear in **Systems Manager → Fleet Manager**. If it doesn't show up, reboot the instance (or restart the agent: `sudo systemctl restart amazon-ssm-agent`).

---

## 3. GitHub — repo variables and secret

Go to the repo on github.com → **Settings → Secrets and variables → Actions**.

### 3.1 Variables tab → New repository variable

| Name | Value |
|------|-------|
| AWS_REGION | your region, e.g. ap-south-1 |
| EC2_INSTANCE_ID | your instance ID, e.g. i-0abc123... |

### 3.2 Secrets tab → New repository secret

| Name | Value |
|------|-------|
| AWS_ROLE_ARN | arn:aws:iam::<ACCOUNT_ID>:role/github-actions-central-ecr (from step 2.2) |

That's everything the workflow (`.github/workflows/ci-cd.yml`) reads — it needs no other credentials.

---

## 4. EC2 server — one-time setup (Amazon Linux 2023)

SSH into the instance for these steps.

### 4.1 Install Docker

```bash
sudo dnf install -y docker
sudo systemctl enable --now docker
sudo usermod -aG docker ec2-user   # log out and back in for this to take effect
```

### 4.2 Install the Compose v2 plugin

AL2023 packages Docker but not Compose — install the plugin manually:

```bash
sudo mkdir -p /usr/local/lib/docker/cli-plugins
sudo curl -SL "https://github.com/docker/compose/releases/latest/download/docker-compose-linux-$(uname -m)"
  -o /usr/local/lib/docker/cli-plugins/docker-compose
sudo chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
docker compose version   # should print a v2.x version
```

### 4.3 Copy the deploy files

Copy `deploy.sh` and `docker-compose.prod.yml` from this repo's `deploy/` directory to **exactly** `/home/ec2-user/central-deploy/` — the workflow's SSM step invokes `/home/ec2-user/central-deploy/deploy.sh` by that absolute path. From your machine:

```bash
scp deploy/deploy.sh deploy/docker-compose.prod.yml ec2-user@<server>:/home/ec2-user/central-deploy/
```

(Create the directory first if needed: `ssh ec2-user@<server> mkdir -p /home/ec2-user/central-deploy`.)

Make the script executable:

```bash
chmod +x /home/ec2-user/central-deploy/deploy.sh
```

This is the last time anything is copied from the repo — from now on the server only pulls images from ECR.

### 4.4 Create the deploy `.env`

Create `/home/ec2-user/central-deploy/.env` with the two values `deploy.sh` needs:

```bash
AWS_REGION=<your-region>
ECR_REGISTRY=<account-id>.dkr.ecr.<your-region>.amazonaws.com
```

(`ECR_REGISTRY` is the registry URL you noted in step 1.3.)

### 4.5 Runtime app config

Both containers read their runtime config from `/etc/central/central.env` via `env_file` — make sure it exists and contains the app's env (including `SIDECAR_SHARED_SECRET`; the images bake in `values/`). This file is not part of this setup guide's scope — it stays wherever your existing app config lives.

---

## 5. Verify the pipeline

1. **Server-side dry run** — on the instance:

    ```bash
    cd /home/ec2-user/central-deploy
    ./deploy.sh
    ```

    This should log in to ECR without any configured credentials (proving the instance role works), then fail to pull only because no images exist yet — that's expected on the very first run.

2. **First real deploy** — merge or push a commit to `main`. In the repo's **Actions** tab watch the `CI/CD` workflow: both test jobs → *Build & push to ECR* (proves the OIDC role + `ecr-push` policy) → *Deploy on server (SSM)* (proves the `ssm-deploy` policy + instance role). The deploy step's log shows the compose pull/up output and `docker compose ps` from the server.

3. **On the box** — `docker compose -f docker-compose.prod.yml ps` should show `central` (published on `127.0.0.1:3090`) and `central-sidecar` healthy.


### Manual deploy / rollback (any time later)

```bash
cd /home/ec2-user/central-deploy
./deploy.sh                       # deploy :latest
IMAGE_TAG=main-<git-sha> ./deploy.sh   # pin any previous commit's build (rollback)
```

---

## Troubleshooting

- **Actions step "Configure AWS credentials" fails with `Not authorized to perform sts:AssumeRoleWithWebIdentity`** — trust policy mismatch. Re-check step 2.2: the `sub` condition must be exactly `repo:atharva-ng/central:ref:refs/heads/main`, and the OIDC provider must exist.
- **Build & push fails with an ECR permission error** — the `ecr-push` inline policy region/account/repo ARNs don't match the actual repositories (step 2.2).
- **SSM step hangs then fails / `InvalidInstanceId`** — the instance isn't SSM-managed: the `central-server` role isn't attached, or the agent hasn't picked it up (step 2.3). Check Systems Manager → Fleet Manager for the instance.
- **`deploy.sh` can't log in to ECR on the server** — the instance role is missing `AmazonEC2ContainerRegistryReadOnly`, or `.env` has the wrong `ECR_REGISTRY`/`AWS_REGION`.
- **Containers start but the app misbehaves** — check `/etc/central/central.env`; both services read it, and the sidecar additionally pins `PORT=4090` in compose.