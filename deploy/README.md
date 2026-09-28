# Deployment

Merges to `main` trigger `.github/workflows/ci-cd.yml`: both test suites run, the
`central` and `central-sidecar` images are built and pushed to ECR tagged `latest` and
the git SHA, then the workflow runs `deploy.sh` on the server via AWS SSM — pinned to
that commit's image tag. The server (Amazon Linux EC2) never touches git — it pulls
images from ECR and runs them via `docker-compose.prod.yml`.

```
merge to main → GitHub Actions: test → build → push to ECR → SSM runs deploy.sh on the instance
manual deploy / rollback:  ssh in →  IMAGE_TAG=main-<git-sha> ./deploy.sh
```

The `integration` branch drives a second, identical pipeline to the integration EC2
instance (images tagged `integration` instead of `latest`) — see
[Integration environment](#integration-environment) at the bottom.

## One-time AWS setup

Run these locally with admin AWS credentials. Set the two variables first:

```bash
export AWS_REGION=<your-region>            # e.g. ap-south-1
export ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
export INSTANCE_ID=<your-ec2-instance-id>  # the server the workflow deploys to
```

### 1. Create the ECR repositories

```bash
aws ecr create-repository --repository-name central --region $AWS_REGION
aws ecr create-repository --repository-name central-sidecar --region $AWS_REGION
```

Optional but recommended — keep only recent images so storage doesn't grow forever:

```bash
POLICY='{"rules":[{"rulePriority":1,"description":"keep last 10","selection":{"tagStatus":"any","countType":"imageCountMoreThan","countNumber":10},"action":{"type":"expire"}}]}'
aws ecr put-lifecycle-policy --repository-name central --lifecycle-policy-text "$POLICY" --region $AWS_REGION
aws ecr put-lifecycle-policy --repository-name central-sidecar --lifecycle-policy-text "$POLICY" --region $AWS_REGION
```

### 2. Let GitHub Actions push (OIDC — no long-lived keys)

Create the GitHub OIDC identity provider (skip if your account already has one):

```bash
aws iam create-open-id-connect-provider \
  --url https://token.actions.githubusercontent.com \
  --client-id-list sts.amazonaws.com
```

Create a role only the `main` branch of `atharva-ng/central` can assume:

```bash
cat > /tmp/trust.json <<EOF
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Federated": "arn:aws:iam::${ACCOUNT_ID}:oidc-provider/token.actions.githubusercontent.com"},
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {
      "StringEquals": {"token.actions.githubusercontent.com:aud": "sts.amazonaws.com"},
      "StringLike": {"token.actions.githubusercontent.com:sub": "repo:atharva-ng/central:ref:refs/heads/main"}
    }
  }]
}
EOF
aws iam create-role --role-name github-actions-central-ecr \
  --assume-role-policy-document file:///tmp/trust.json

cat > /tmp/ecr-push.json <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {"Effect": "Allow", "Action": "ecr:GetAuthorizationToken", "Resource": "*"},
    {"Effect": "Allow",
     "Action": ["ecr:BatchCheckLayerAvailability", "ecr:CompleteLayerUpload",
                "ecr:InitiateLayerUpload", "ecr:PutImage", "ecr:UploadLayerPart",
                "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer"],
     "Resource": ["arn:aws:ecr:${AWS_REGION}:${ACCOUNT_ID}:repository/central",
                  "arn:aws:ecr:${AWS_REGION}:${ACCOUNT_ID}:repository/central-sidecar"]}
  ]
}
EOF
aws iam put-role-policy --role-name github-actions-central-ecr \
  --policy-name ecr-push --policy-document file:///tmp/ecr-push.json
```

Let the same role trigger the deploy on the instance via SSM:

```bash
cat > /tmp/ssm-deploy.json <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {"Effect": "Allow", "Action": "ssm:SendCommand",
     "Resource": ["arn:aws:ec2:${AWS_REGION}:${ACCOUNT_ID}:instance/${INSTANCE_ID}",
                  "arn:aws:ssm:${AWS_REGION}::document/AWS-RunShellScript"]},
    {"Effect": "Allow", "Action": "ssm:GetCommandInvocation", "Resource": "*"}
  ]
}
EOF
aws iam put-role-policy --role-name github-actions-central-ecr \
  --policy-name ssm-deploy --policy-document file:///tmp/ssm-deploy.json
```

### 3. Configure the GitHub repo

```bash
gh variable set AWS_REGION --body "$AWS_REGION"
gh variable set EC2_INSTANCE_ID --body "$INSTANCE_ID"
gh secret set AWS_ROLE_ARN --body "arn:aws:iam::${ACCOUNT_ID}:role/github-actions-central-ecr"
```

(Or set them under repo Settings → Secrets and variables → Actions.)

## One-time server setup (Amazon Linux 2023 EC2)

### 1. Attach an instance role for ECR pulls + SSM

No AWS keys on the box — attach an IAM role to the instance with
`AmazonEC2ContainerRegistryReadOnly` (image pulls) and `AmazonSSMManagedInstanceCore`
(lets the workflow's SSM step reach the instance; the SSM agent itself is preinstalled
and running on AL2023):

```bash
aws iam create-role --role-name central-server \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam attach-role-policy --role-name central-server \
  --policy-arn arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly
aws iam attach-role-policy --role-name central-server \
  --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore
aws iam create-instance-profile --instance-profile-name central-server
aws iam add-role-to-instance-profile --instance-profile-name central-server --role-name central-server
aws ec2 associate-iam-instance-profile \
  --instance-id $INSTANCE_ID --iam-instance-profile Name=central-server
```

If the instance already has a role, just attach both managed policies to it.
`aws ecr get-login-password` then works with no configured credentials (AWS CLI v2 is
preinstalled on AL2023 and picks up the instance role automatically).

### 2. Install Docker + the compose plugin

AL2023 packages Docker but not Compose v2 — install the plugin manually:

```bash
sudo dnf install -y docker
sudo systemctl enable --now docker
sudo usermod -aG docker ec2-user   # re-login for this to take effect

sudo mkdir -p /usr/local/lib/docker/cli-plugins
sudo curl -SL "https://github.com/docker/compose/releases/latest/download/docker-compose-linux-$(uname -m)" \
  -o /usr/local/lib/docker/cli-plugins/docker-compose
sudo chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
docker compose version
```

### 3. Copy this directory and configure it

Copy `deploy.sh` and `docker-compose.prod.yml` to `/home/ec2-user/central-deploy/`
(exactly that path — the workflow's SSM step invokes
`/home/ec2-user/central-deploy/deploy.sh`) — this is the last time anything is copied
from the repo — and create `.env` next to them:

```bash
# /home/ec2-user/central-deploy/.env
AWS_REGION=<your-region>
ECR_REGISTRY=<account-id>.dkr.ecr.<your-region>.amazonaws.com
```

Runtime app config stays where it already is: `/etc/central/central.env`
(both services read it via `env_file`; the images bake in `values/`).

## Deploying

Automatic: merging to `main` runs tests, pushes the images, and the workflow's final
job runs `deploy.sh` on the instance via SSM with `IMAGE_TAG` pinned to that commit's
SHA. The deploy's output (compose pull/up + `ps`) appears in the Actions log, and the
run fails if the script fails.

Manual (re-deploy or roll back) — ssh in and run:

```bash
cd /home/ec2-user/central-deploy
./deploy.sh                    # deploy :latest
IMAGE_TAG=main-<git-sha> ./deploy.sh  # pin any previous commit's build
```

Old local images are pruned automatically by the script; ECR history is bounded by the
lifecycle policy (last 10 images).

## Integration environment

A second EC2 instance running the same stack, deployed from the `integration` branch:

```
push to integration → GitHub Actions: test → build → push to ECR (:integration + :sha) → SSM runs deploy.sh on the integration instance
```

Everything is shared with production except the instance itself: same two ECR repos
(the mutable tag `integration` vs `latest` tells the builds apart; SHA tags are
per-commit and unique either way), same GitHub Actions role (trust extended to the
`integration` branch), same `central-server` instance role (attached to both boxes).

Set the variables first (note the extra one):

```bash
export AWS_REGION=<your-region>
export ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
export INSTANCE_ID=<production-ec2-instance-id>
export INTEGRATION_INSTANCE_ID=<integration-ec2-instance-id>
```

### 1. ECR — nothing to create

The existing `central` and `central-sidecar` repos hold both environments' images.
Recommended: bump the lifecycle policy from 10 to 20 kept images, since two branches
now push into the same repos (otherwise active integration pushes can expire
production rollback tags):

```bash
POLICY='{"rules":[{"rulePriority":1,"description":"keep last 20","selection":{"tagStatus":"any","countType":"imageCountMoreThan","countNumber":20},"action":{"type":"expire"}}]}'
aws ecr put-lifecycle-policy --repository-name central --lifecycle-policy-text "$POLICY" --region $AWS_REGION
aws ecr put-lifecycle-policy --repository-name central-sidecar --lifecycle-policy-text "$POLICY" --region $AWS_REGION
```

### 2. Extend the GitHub Actions role to the `integration` branch

Replace the trust policy of `github-actions-central-ecr` so both branches can assume
it (`update-assume-role-policy` overwrites the whole document):

```bash
cat > /tmp/trust.json <<EOF
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Federated": "arn:aws:iam::${ACCOUNT_ID}:oidc-provider/token.actions.githubusercontent.com"},
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {
      "StringEquals": {"token.actions.githubusercontent.com:aud": "sts.amazonaws.com"},
      "StringLike": {"token.actions.githubusercontent.com:sub": [
        "repo:atharva-ng/central:ref:refs/heads/main",
        "repo:atharva-ng/central:ref:refs/heads/integration"
      ]}
    }
  }]
}
EOF
aws iam update-assume-role-policy --role-name github-actions-central-ecr \
  --policy-document file:///tmp/trust.json
```

The `ecr-push` inline policy needs no change (same repos). Rewrite `ssm-deploy` so the
role can also trigger deploys on the integration instance (`put-role-policy`
overwrites the whole policy, so both instance ARNs must be listed):

```bash
cat > /tmp/ssm-deploy.json <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {"Effect": "Allow", "Action": "ssm:SendCommand",
     "Resource": ["arn:aws:ec2:${AWS_REGION}:${ACCOUNT_ID}:instance/${INSTANCE_ID}",
                  "arn:aws:ec2:${AWS_REGION}:${ACCOUNT_ID}:instance/${INTEGRATION_INSTANCE_ID}",
                  "arn:aws:ssm:${AWS_REGION}::document/AWS-RunShellScript"]},
    {"Effect": "Allow", "Action": "ssm:GetCommandInvocation", "Resource": "*"}
  ]
}
EOF
aws iam put-role-policy --role-name github-actions-central-ecr \
  --policy-name ssm-deploy --policy-document file:///tmp/ssm-deploy.json
```

### 3. Configure the GitHub repo

One new variable; region, role ARN, and the production instance variable are reused:

```bash
gh variable set EC2_INSTANCE_ID_INTEGRATION --body "$INTEGRATION_INSTANCE_ID"
```

### 4. Integration server one-time setup (Amazon Linux 2023 EC2)

Launch the instance, then repeat the production
[One-time server setup](#one-time-server-setup-amazon-linux-2023-ec2) on it with two
differences.

Attach the **existing** `central-server` instance profile instead of creating a new
role (an instance profile can be attached to any number of instances):

```bash
aws ec2 associate-iam-instance-profile \
  --instance-id $INTEGRATION_INSTANCE_ID --iam-instance-profile Name=central-server
```

Then, exactly as for production: install Docker + the compose plugin, copy `deploy.sh`
and `docker-compose.prod.yml` to `/home/ec2-user/central-deploy/`, and create the app
config at `/etc/central/central.env` (with integration-specific values — its own DB,
keys, etc.). The deploy `.env` gets one extra line so manual `./deploy.sh` runs pull
`:integration` instead of production's `:latest`:

```bash
# /home/ec2-user/central-deploy/.env
AWS_REGION=<your-region>
ECR_REGISTRY=<account-id>.dkr.ecr.<your-region>.amazonaws.com
IMAGE_TAG=integration
```

(`deploy.sh` treats `.env`'s `IMAGE_TAG` as a default — an explicit
`IMAGE_TAG=<sha> ./deploy.sh`, including the workflow's SSM pin, still wins.)

### 5. Deploying to integration

Automatic: push or merge to `integration`. The same `CI/CD` workflow runs — tests,
build, push as `:integration` + `:integration-<sha>` — and its deploy job targets
`EC2_INSTANCE_ID_INTEGRATION`, pinned to the commit's SHA.

Manual, on the integration box:

```bash
cd /home/ec2-user/central-deploy
./deploy.sh                       # deploy :integration (default from .env)
IMAGE_TAG=integration-<git-sha> ./deploy.sh   # pin any previous build (rollback)
```
