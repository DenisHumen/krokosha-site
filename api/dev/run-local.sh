#!/usr/bin/env bash
# The API with the admin area on this machine, for looking at the dashboards while developing:
# throw-away MySQL and Redis (compose.test.yaml), an administrator «dev», two weeks of demo visits.
#
#   api/dev/run-local.sh            → http://localhost:8099/_dev/   login: dev   password: local-dev-password
#
# Nothing here is used on the server. Stop with Ctrl+C; `docker compose -f compose.test.yaml down -v` removes the data.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

docker compose -f compose.test.yaml up -d --wait
mysql() { docker exec -i krokosha-test-mysql-1 mysql -uroot -pkrokosha-test "$@" 2>/dev/null; }
mysql -e 'CREATE DATABASE IF NOT EXISTS krokosha_dev CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci'

# Git Bash on Windows would rewrite a value that looks like a POSIX path (/_dev) into C:/Program Files/Git/_dev.
export MSYS2_ENV_CONV_EXCL='ADMIN_PATH'
export KROKOSHA_LISTEN=127.0.0.1:8099 SITE_URL=http://localhost:8099 ADMIN_PATH=/_dev KROKOSHA_LOG_LEVEL=debug
export MYSQL_ADDR=127.0.0.1:33306 MYSQL_DATABASE=krokosha_dev MYSQL_USER=root MYSQL_PASSWORD=krokosha-test
export REDIS_URL=redis://127.0.0.1:36379/1 KROKOSHA_CONTENT_DIR=../content

# The CLI applies the migrations; «this login already exists» on later runs is fine.
printf 'local-dev-password\n' | go run ./cmd/krokosha-cli admin create dev --password-stdin || true
mysql krokosha_dev <dev/seed-demo.sql
echo "Admin area: http://localhost:8099/_dev/   login: dev   password: local-dev-password"
exec go run ./cmd/krokosha-api
