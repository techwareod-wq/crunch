#!/usr/bin/env bash
# Pull the latest images from ECR and restart the stack.
# Run from anywhere: ./deploy.sh            → deploys :latest
#                    IMAGE_TAG=main-<sha> ./deploy.sh → deploys a specific build (rollback)
set -euo pipefail
cd "$(dirname "$0")"

# .env must define ECR_REGISTRY (<account>.dkr.ecr.<region>.amazonaws.com) and AWS_REGION.
# It may also set a default IMAGE_TAG (the integration box pins IMAGE_TAG=integration);
# a caller-supplied IMAGE_TAG (CI's SHA pin, manual rollback) always wins over it.
CALLER_IMAGE_TAG="${IMAGE_TAG:-}"
set -a
source .env
set +a
if [ -n "$CALLER_IMAGE_TAG" ]; then
  export IMAGE_TAG="$CALLER_IMAGE_TAG"
fi

: "${ECR_REGISTRY:?set ECR_REGISTRY in .env}"
: "${AWS_REGION:?set AWS_REGION in .env}"

# Compose derives the project name from this directory; pin it so container
# ownership doesn't change if the checkout is ever moved or renamed.
export COMPOSE_PROJECT_NAME=central-deploy

aws ecr get-login-password --region "$AWS_REGION" |
  docker login --username AWS --password-stdin "$ECR_REGISTRY"

# The fixed container_names conflict with any same-named container this
# project doesn't own (started by hand, or by compose under another project
# name). Remove those so `up` can create ours; compose recreates its own.
for name in central central-sidecar; do
  cid=$(docker ps -aq --filter "name=^${name}$")
  [ -z "$cid" ] && continue
  owner=$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.project" }}' "$cid")
  if [ "$owner" != "$COMPOSE_PROJECT_NAME" ]; then
    echo "Removing stale container $name ($cid, project: '${owner:-none}')"
    docker rm -f "$cid"
  fi
done

docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d --remove-orphans

# Drop superseded image layers so the disk doesn't fill up over time.
docker image prune -f

docker compose -f docker-compose.prod.yml ps
