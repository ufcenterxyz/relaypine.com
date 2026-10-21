#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
set -a
source .env
set +a
export MYSQL_TEST_DSN="$MYSQL_DSN" REDIS_TEST_ADDR="$REDIS_ADDR"
cd services/gateway
go test -race -count=1 ./...
