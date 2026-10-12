#!/usr/bin/env bash
# One-time AWS setup for crunch: a single environment deployed from `master`.
#
#   push to master → GitHub Actions (OIDC role) → build → push to ECR
#                  → ssm:SendCommand → deploy.sh on the EC2 box
#
# Safe to re-run: every step checks for the resource first and reuses it.
# Never prints secrets. The app user's access key is written to
# ~/.crunch-setup/app-keys (0600), delivered to the box via SSM, and is
# not echoed.
#
# Usage:  AWS_PROFILE=crunch ./deploy/setup-aws.sh
set -euo pipefail
cd "$(dirname "$0")"

export AWS_PROFILE="${AWS_PROFILE:-crunch}"
# ap-south-1 is denied for this account by an org SCP; ap-southeast-2 is open.
export AWS_REGION=ap-southeast-2
export AWS_PAGER=""

APP=crunch
GITHUB_REPO=techwareod-wq/crunch
BRANCH=master
INSTANCE_TYPE=t3.small   # container memory limit is 1g; t3.micro has 1 GiB total

ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
ECR_REGISTRY="${ACCOUNT_ID}.dkr.ecr.${AWS_REGION}.amazonaws.com"
PUBLIC_BUCKET="${APP}-media-public-${ACCOUNT_ID}"
PRIVATE_BUCKET="${APP}-media-private-${ACCOUNT_ID}"
QUEUE="${APP}-primary"
DLQ="${APP}-dlq"
GH_ROLE="github-actions-${APP}-ecr"
SERVER_ROLE="${APP}-server"
APP_USER="${APP}-app"
SG_NAME="${APP}-server-sg"
OIDC_ARN="arn:aws:iam::${ACCOUNT_ID}:oidc-provider/token.actions.githubusercontent.com"

STATE_DIR="$HOME/.crunch-setup"
KEY_FILE="$STATE_DIR/app-keys"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$STATE_DIR" && chmod 700 "$STATE_DIR"

step() { printf '\n==> %s\n' "$*"; }

# ---------------------------------------------------------------------------
step "ECR repository $APP"
aws ecr describe-repositories --repository-names "$APP" >/dev/null 2>&1 ||
  aws ecr create-repository --repository-name "$APP" \
    --image-scanning-configuration scanOnPush=true >/dev/null
aws ecr put-lifecycle-policy --repository-name "$APP" --lifecycle-policy-text \
  '{"rules":[{"rulePriority":1,"description":"keep last 20","selection":{"tagStatus":"any","countType":"imageCountMoreThan","countNumber":20},"action":{"type":"expire"}}]}' >/dev/null

# ---------------------------------------------------------------------------
step "GitHub OIDC provider"
aws iam get-open-id-connect-provider --open-id-connect-provider-arn "$OIDC_ARN" >/dev/null 2>&1 ||
  aws iam create-open-id-connect-provider \
    --url https://token.actions.githubusercontent.com \
    --client-id-list sts.amazonaws.com \
    --thumbprint-list 6938fd4d98bab03faadb97b34396831e3780aea1 >/dev/null

# ---------------------------------------------------------------------------
step "S3 buckets $PUBLIC_BUCKET, $PRIVATE_BUCKET"
for b in "$PUBLIC_BUCKET" "$PRIVATE_BUCKET"; do
  aws s3api head-bucket --bucket "$b" >/dev/null 2>&1 ||
    aws s3api create-bucket --bucket "$b" \
      --create-bucket-configuration LocationConstraint="$AWS_REGION" >/dev/null
  aws s3api put-public-access-block --bucket "$b" --public-access-block-configuration \
    BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
  # Browser uploads go straight to S3 via presigned PUT. Origin is * until the
  # frontend domain is decided; then list the exact https:// origin(s) here.
  aws s3api put-bucket-cors --bucket "$b" --cors-configuration \
    '{"CORSRules":[{"AllowedOrigins":["*"],"AllowedMethods":["PUT","GET","HEAD"],"AllowedHeaders":["*"],"ExposeHeaders":["ETag"],"MaxAgeSeconds":3000}]}'
done

# ---------------------------------------------------------------------------
step "CloudFront for public media"
DIST_COMMENT="${APP}-public-media"
DIST_ID=$(aws cloudfront list-distributions \
  --query "DistributionList.Items[?Comment=='${DIST_COMMENT}'].Id | [0]" --output text)
if [ "$DIST_ID" = "None" ] || [ -z "$DIST_ID" ]; then
  OAC_ID=$(aws cloudfront list-origin-access-controls \
    --query "OriginAccessControlList.Items[?Name=='${APP}-public-media'].Id | [0]" --output text)
  if [ "$OAC_ID" = "None" ] || [ -z "$OAC_ID" ]; then
    OAC_ID=$(aws cloudfront create-origin-access-control --origin-access-control-config \
      "{\"Name\":\"${APP}-public-media\",\"SigningProtocol\":\"sigv4\",\"SigningBehavior\":\"always\",\"OriginAccessControlOriginType\":\"s3\"}" \
      --query OriginAccessControl.Id --output text)
  fi
  cat >"$TMP/dist.json" <<EOF
{
  "CallerReference": "${APP}-public-media-$(date +%s)",
  "Comment": "${DIST_COMMENT}",
  "Enabled": true,
  "PriceClass": "PriceClass_200",
  "Origins": {"Quantity": 1, "Items": [{
    "Id": "s3-public",
    "DomainName": "${PUBLIC_BUCKET}.s3.${AWS_REGION}.amazonaws.com",
    "OriginAccessControlId": "${OAC_ID}",
    "S3OriginConfig": {"OriginAccessIdentity": ""}
  }]},
  "DefaultCacheBehavior": {
    "TargetOriginId": "s3-public",
    "ViewerProtocolPolicy": "redirect-to-https",
    "CachePolicyId": "658327ea-f89d-4fab-a63d-7e88639e58f6",
    "AllowedMethods": {"Quantity": 2, "Items": ["GET", "HEAD"]},
    "Compress": true
  }
}
EOF
  DIST_ID=$(aws cloudfront create-distribution --distribution-config "file://$TMP/dist.json" \
    --query Distribution.Id --output text)
fi
DIST_DOMAIN=$(aws cloudfront get-distribution --id "$DIST_ID" --query Distribution.DomainName --output text)
cat >"$TMP/public-bucket-policy.json" <<EOF
{"Version":"2012-10-17","Statement":[{
  "Sid":"CloudFrontRead","Effect":"Allow",
  "Principal":{"Service":"cloudfront.amazonaws.com"},
  "Action":"s3:GetObject","Resource":"arn:aws:s3:::${PUBLIC_BUCKET}/*",
  "Condition":{"StringEquals":{"AWS:SourceArn":"arn:aws:cloudfront::${ACCOUNT_ID}:distribution/${DIST_ID}"}}
}]}
EOF
aws s3api put-bucket-policy --bucket "$PUBLIC_BUCKET" --policy "file://$TMP/public-bucket-policy.json"
PUBLIC_MEDIA_BASE_URL="https://${DIST_DOMAIN}"

# ---------------------------------------------------------------------------
step "SQS queues $QUEUE (+ $DLQ)"
DLQ_URL=$(aws sqs create-queue --queue-name "$DLQ" \
  --attributes MessageRetentionPeriod=1209600,SqsManagedSseEnabled=true \
  --query QueueUrl --output text)
DLQ_ARN=$(aws sqs get-queue-attributes --queue-url "$DLQ_URL" \
  --attribute-names QueueArn --query Attributes.QueueArn --output text)
cat >"$TMP/queue-attrs.json" <<EOF
{
  "VisibilityTimeout": "600",
  "ReceiveMessageWaitTimeSeconds": "20",
  "MessageRetentionPeriod": "1209600",
  "SqsManagedSseEnabled": "true",
  "RedrivePolicy": "{\"deadLetterTargetArn\":\"${DLQ_ARN}\",\"maxReceiveCount\":\"5\"}"
}
EOF
QUEUE_URL=$(aws sqs get-queue-url --queue-name "$QUEUE" --query QueueUrl --output text 2>/dev/null ||
  aws sqs create-queue --queue-name "$QUEUE" --attributes "file://$TMP/queue-attrs.json" \
    --query QueueUrl --output text)
aws sqs set-queue-attributes --queue-url "$QUEUE_URL" --attributes "file://$TMP/queue-attrs.json"
QUEUE_ARN=$(aws sqs get-queue-attributes --queue-url "$QUEUE_URL" \
  --attribute-names QueueArn --query Attributes.QueueArn --output text)

# ---------------------------------------------------------------------------
step "App IAM user $APP_USER (S3 + SQS only)"
aws iam get-user --user-name "$APP_USER" >/dev/null 2>&1 ||
  aws iam create-user --user-name "$APP_USER" >/dev/null
cat >"$TMP/app-policy.json" <<EOF
{"Version":"2012-10-17","Statement":[
  {"Effect":"Allow","Action":["s3:ListBucket","s3:ListBucketMultipartUploads"],
   "Resource":["arn:aws:s3:::${PUBLIC_BUCKET}","arn:aws:s3:::${PRIVATE_BUCKET}"]},
  {"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject",
     "s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],
   "Resource":["arn:aws:s3:::${PUBLIC_BUCKET}/*","arn:aws:s3:::${PRIVATE_BUCKET}/*"]},
  {"Effect":"Allow","Action":["sqs:SendMessage","sqs:ReceiveMessage","sqs:DeleteMessage",
     "sqs:ChangeMessageVisibility","sqs:GetQueueAttributes","sqs:GetQueueUrl"],
   "Resource":["${QUEUE_ARN}","${DLQ_ARN}"]}
]}
EOF
aws iam put-user-policy --user-name "$APP_USER" --policy-name "${APP}-app-access" \
  --policy-document "file://$TMP/app-policy.json"
if [ ! -s "$KEY_FILE" ]; then
  if [ "$(aws iam list-access-keys --user-name "$APP_USER" --query 'length(AccessKeyMetadata)')" != "0" ]; then
    echo "!! $APP_USER already has an access key but $KEY_FILE is missing." >&2
    echo "   Delete the old key (aws iam delete-access-key) and re-run." >&2
    exit 1
  fi
  (umask 077; aws iam create-access-key --user-name "$APP_USER" \
    --query 'AccessKey.[AccessKeyId,SecretAccessKey]' --output text >"$KEY_FILE")
fi

# ---------------------------------------------------------------------------
step "EC2 instance role $SERVER_ROLE"
if ! aws iam get-role --role-name "$SERVER_ROLE" >/dev/null 2>&1; then
  aws iam create-role --role-name "$SERVER_ROLE" --assume-role-policy-document \
    '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}' >/dev/null
fi
aws iam attach-role-policy --role-name "$SERVER_ROLE" \
  --policy-arn arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly
aws iam attach-role-policy --role-name "$SERVER_ROLE" \
  --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore
if ! aws iam get-instance-profile --instance-profile-name "$SERVER_ROLE" >/dev/null 2>&1; then
  aws iam create-instance-profile --instance-profile-name "$SERVER_ROLE" >/dev/null
  aws iam add-role-to-instance-profile --instance-profile-name "$SERVER_ROLE" --role-name "$SERVER_ROLE"
  sleep 15 # instance profiles take a moment before run-instances accepts them
fi

# ---------------------------------------------------------------------------
step "EC2 box ($INSTANCE_TYPE, Amazon Linux 2023)"
INSTANCE_ID=$(aws ec2 describe-instances \
  --filters "Name=tag:App,Values=$APP" "Name=instance-state-name,Values=pending,running,stopping,stopped" \
  --query 'Reservations[].Instances[].InstanceId | [0]' --output text)
if [ "$INSTANCE_ID" = "None" ] || [ -z "$INSTANCE_ID" ]; then
  VPC_ID=$(aws ec2 describe-vpcs --filters Name=isDefault,Values=true --query 'Vpcs[0].VpcId' --output text)
  SG_ID=$(aws ec2 describe-security-groups --filters "Name=group-name,Values=$SG_NAME" "Name=vpc-id,Values=$VPC_ID" \
    --query 'SecurityGroups[0].GroupId' --output text)
  if [ "$SG_ID" = "None" ]; then
    # No inbound rules: deploys and shells go through SSM, the app binds 127.0.0.1.
    SG_ID=$(aws ec2 create-security-group --group-name "$SG_NAME" --vpc-id "$VPC_ID" \
      --description "crunch server - egress only, access via SSM" --query GroupId --output text)
  fi
  # SSM GetParameter is SCP-blocked, so resolve the AMI by name.
  AMI_ID=$(aws ec2 describe-images --owners amazon \
    --filters "Name=name,Values=al2023-ami-2023.*-x86_64" "Name=state,Values=available" \
    --query 'sort_by(Images,&CreationDate)[-1].ImageId' --output text)

  DEPLOY_SH_B64=$(base64 <deploy.sh | tr -d '\n')
  COMPOSE_B64=$(base64 <docker-compose.prod.yml | tr -d '\n')
  cat >"$TMP/user-data.sh" <<EOF
#!/bin/bash
set -euxo pipefail
dnf install -y docker
systemctl enable --now docker
usermod -aG docker ec2-user
mkdir -p /usr/local/lib/docker/cli-plugins
curl -fsSL -o /usr/local/lib/docker/cli-plugins/docker-compose \
  https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64
chmod +x /usr/local/lib/docker/cli-plugins/docker-compose

D=/home/ec2-user/${APP}-deploy
mkdir -p \$D
echo '${DEPLOY_SH_B64}' | base64 -d >\$D/deploy.sh
echo '${COMPOSE_B64}' | base64 -d >\$D/docker-compose.prod.yml
chmod +x \$D/deploy.sh
cat >\$D/.env <<ENV
AWS_REGION=${AWS_REGION}
ECR_REGISTRY=${ECR_REGISTRY}
ENV
chown -R ec2-user:ec2-user \$D

mkdir -p /etc/${APP}
cat >/etc/${APP}/${APP}.env <<ENV
ENVIRONMENT=production
AWS_REGION=${AWS_REGION}
AWS_ACCESS_KEY_ID=
AWS_SECRET_ACCESS_KEY=
AWS_S3_PUBLIC_BUCKET=${PUBLIC_BUCKET}
AWS_S3_PRIVATE_BUCKET=${PRIVATE_BUCKET}
PUBLIC_MEDIA_BASE_URL=${PUBLIC_MEDIA_BASE_URL}
SQS_QUEUE_URL=${QUEUE_URL}
SQS_DLQ_URL=${DLQ_URL}
# --- fill these in (sudo vi /etc/${APP}/${APP}.env), then run deploy.sh ---
MONGO_URI=
CLERK_SECRET_KEY=
CLERK_WEBHOOK_SECRET=
ANTHROPIC_API_KEY=
GROK_API_KEY=
GOOGLE_MAPS_API_KEY=
VOYAGE_API_KEY=
PUBLIC_BASE_URL=
ADMIN_BASE_URL=
ENV
chmod 600 /etc/${APP}/${APP}.env
EOF

  INSTANCE_ID=$(aws ec2 run-instances --image-id "$AMI_ID" --instance-type "$INSTANCE_TYPE" \
    --iam-instance-profile Name="$SERVER_ROLE" --security-group-ids "$SG_ID" \
    --metadata-options HttpTokens=required,HttpEndpoint=enabled,HttpPutResponseHopLimit=2 \
    --block-device-mappings 'DeviceName=/dev/xvda,Ebs={VolumeSize=30,VolumeType=gp3,Encrypted=true}' \
    --user-data "file://$TMP/user-data.sh" \
    --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=$APP},{Key=App,Value=$APP}]" \
    --query 'Instances[0].InstanceId' --output text)
fi
echo "instance: $INSTANCE_ID"

# ---------------------------------------------------------------------------
step "GitHub Actions role $GH_ROLE (trusts $GITHUB_REPO@$BRANCH)"
cat >"$TMP/gh-trust.json" <<EOF
{"Version":"2012-10-17","Statement":[{
  "Effect":"Allow",
  "Principal":{"Federated":"${OIDC_ARN}"},
  "Action":"sts:AssumeRoleWithWebIdentity",
  "Condition":{
    "StringEquals":{
      "token.actions.githubusercontent.com:aud":"sts.amazonaws.com",
      "token.actions.githubusercontent.com:sub":"repo:${GITHUB_REPO}:ref:refs/heads/${BRANCH}"
    }
  }
}]}
EOF
if aws iam get-role --role-name "$GH_ROLE" >/dev/null 2>&1; then
  aws iam update-assume-role-policy --role-name "$GH_ROLE" --policy-document "file://$TMP/gh-trust.json"
else
  aws iam create-role --role-name "$GH_ROLE" --assume-role-policy-document "file://$TMP/gh-trust.json" >/dev/null
fi
cat >"$TMP/ecr-push.json" <<EOF
{"Version":"2012-10-17","Statement":[
  {"Effect":"Allow","Action":"ecr:GetAuthorizationToken","Resource":"*"},
  {"Effect":"Allow","Action":["ecr:BatchCheckLayerAvailability","ecr:BatchGetImage",
     "ecr:GetDownloadUrlForLayer","ecr:InitiateLayerUpload","ecr:UploadLayerPart",
     "ecr:CompleteLayerUpload","ecr:PutImage"],
   "Resource":"arn:aws:ecr:${AWS_REGION}:${ACCOUNT_ID}:repository/${APP}"}
]}
EOF
aws iam put-role-policy --role-name "$GH_ROLE" --policy-name ecr-push --policy-document "file://$TMP/ecr-push.json"
cat >"$TMP/ssm-deploy.json" <<EOF
{"Version":"2012-10-17","Statement":[
  {"Effect":"Allow","Action":"ssm:SendCommand",
   "Resource":["arn:aws:ec2:${AWS_REGION}:${ACCOUNT_ID}:instance/${INSTANCE_ID}",
               "arn:aws:ssm:${AWS_REGION}::document/AWS-RunShellScript"]},
  {"Effect":"Allow","Action":"ssm:GetCommandInvocation","Resource":"*"}
]}
EOF
aws iam put-role-policy --role-name "$GH_ROLE" --policy-name ssm-deploy --policy-document "file://$TMP/ssm-deploy.json"
GH_ROLE_ARN=$(aws iam get-role --role-name "$GH_ROLE" --query Role.Arn --output text)

# ---------------------------------------------------------------------------
step "Waiting for the box to register with SSM and finish bootstrapping"
for _ in $(seq 1 60); do
  PING=$(aws ssm describe-instance-information --filters "Key=InstanceIds,Values=$INSTANCE_ID" \
    --query 'InstanceInformationList[0].PingStatus' --output text 2>/dev/null || true)
  [ "$PING" = "Online" ] && break
  sleep 10
done
[ "$PING" = "Online" ] || { echo "!! $INSTANCE_ID never came online in SSM" >&2; exit 1; }

# Put the app user's key into the env file. Sent as an SSM command so it never
# sits in user data; nothing here is echoed.
read -r APP_KEY_ID APP_KEY_SECRET <"$KEY_FILE"
cat >"$TMP/keys-cmd.json" <<EOF
{"commands":[
  "cloud-init status --wait >/dev/null || true",
  "test -f /etc/${APP}/${APP}.env",
  "sed -i 's|^AWS_ACCESS_KEY_ID=.*|AWS_ACCESS_KEY_ID=${APP_KEY_ID}|; s|^AWS_SECRET_ACCESS_KEY=.*|AWS_SECRET_ACCESS_KEY=${APP_KEY_SECRET}|' /etc/${APP}/${APP}.env",
  "docker compose version"
]}
EOF
CMD_ID=$(aws ssm send-command --instance-ids "$INSTANCE_ID" --document-name AWS-RunShellScript \
  --comment "crunch: install app keys" --parameters "file://$TMP/keys-cmd.json" \
  --query Command.CommandId --output text)
STATUS=Pending
for _ in $(seq 1 60); do
  sleep 5
  STATUS=$(aws ssm get-command-invocation --command-id "$CMD_ID" --instance-id "$INSTANCE_ID" \
    --query Status --output text 2>/dev/null || echo Pending)
  case "$STATUS" in Success|Failed|Cancelled|TimedOut) break ;; esac
done
echo "box bootstrap + key install: $STATUS"
[ "$STATUS" = "Success" ]

# ---------------------------------------------------------------------------
cat <<EOF

================================================================
Done. Set these in GitHub → techwareod-wq/crunch → Settings →
Secrets and variables → Actions:

  Secret    AWS_ROLE_ARN      = ${GH_ROLE_ARN}
  Variable  AWS_REGION        = ${AWS_REGION}
  Variable  EC2_INSTANCE_ID   = ${INSTANCE_ID}

Then fill MONGO_URI, CLERK_* and the API keys on the box:
  aws ssm start-session --target ${INSTANCE_ID}
  sudo vi /etc/${APP}/${APP}.env

Buckets:  ${PUBLIC_BUCKET} (via ${PUBLIC_MEDIA_BASE_URL}), ${PRIVATE_BUCKET}
Queues:   ${QUEUE_URL}
          ${DLQ_URL}
================================================================
EOF
