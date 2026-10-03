#!/usr/bin/env bash
# Starts the local SQS stand-in (ElasticMQ) in the background if it isn't
# already running, and waits until it accepts connections. Needs Java.
# Logs: .local/elasticmq.log
set -euo pipefail
cd "$(dirname "$0")/../.."

JAR=.local/elasticmq-server.jar
VERSION=1.7.1

if nc -z 127.0.0.1 9324 2>/dev/null; then
  echo "local queue already running on :9324"
  exit 0
fi

if [ ! -f "$JAR" ]; then
  mkdir -p .local
  echo "downloading ElasticMQ $VERSION"
  curl -sSL -o "$JAR" "https://github.com/softwaremill/elasticmq/releases/download/v$VERSION/elasticmq-server-all-$VERSION.jar"
fi

nohup java -Dconfig.file=scripts/local/elasticmq.conf -jar "$JAR" > .local/elasticmq.log 2>&1 &

for _ in $(seq 1 30); do
  if nc -z 127.0.0.1 9324 2>/dev/null; then
    echo "local queue started on :9324"
    exit 0
  fi
  sleep 1
done

echo "local queue did not start; see .local/elasticmq.log" >&2
exit 1
