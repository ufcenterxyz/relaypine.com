#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
# An explicitly supplied test DSN wins over the local development configuration.
relaypine_test_dsn="${MYSQL_TEST_DSN:-}"
relaypine_test_redis="${REDIS_TEST_ADDR:-}"
if [[ -f .env ]]; then
  set -a
  source .env
  set +a
fi
if [[ -z "$relaypine_test_dsn" ]]; then
  # Keep access-policy migration tests away from the real development accounts.
  docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot' <<'SQL'
CREATE DATABASE IF NOT EXISTS relaypine_test CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
GRANT ALL PRIVILEGES ON relaypine_test.* TO 'relaypine'@'%';
SQL
  relaypine_test_dsn="$(python3 - <<'PY'
import os
prefix, database = os.environ['MYSQL_DSN'].rsplit('/', 1)
query = database.partition('?')[2]
print(prefix + '/relaypine_test' + ('?' + query if query else ''))
PY
)"
fi
if [[ "$relaypine_test_dsn" == "${MYSQL_DSN:-}" ]]; then
  echo 'Refusing to run access-policy tests against the application database.' >&2
  exit 1
fi
export MYSQL_TEST_DSN="$relaypine_test_dsn"
export REDIS_TEST_ADDR="${relaypine_test_redis:-${REDIS_ADDR:-127.0.0.1:4813}}"
cd services/gateway
# A single canonical admin identity is used; isolate package lifecycles.
go test -race -count=1 -p 1 ./...
