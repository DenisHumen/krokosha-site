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

# A pretend server: state and release directories, a build report, an access log of two weeks.
work=${TMPDIR:-/tmp}/krokosha-dev
mkdir -p "$work/state/status" "$work/state/requests" "$work/www/releases/20260919-060012"
ln -sfn "$work/www/releases/20260919-060012" "$work/www/current" 2>/dev/null || true
printf '{"started_at":"%s","finished_at":"%s","ok":true,"step":"done","github":"ok","release":"20260919-060012"}
'   "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$work/state/status/sync.json"
go run ./dev/demolog >"$work/access.json.log"
mysql krokosha_dev -e 'DELETE FROM traffic_state' 2>/dev/null || true # a fresh log every run: read it from the start
export MSYS2_ENV_CONV_EXCL='ADMIN_PATH;KROKOSHA_STATE;KROKOSHA_WWW;KROKOSHA_ACCESS_LOG'
if command -v cygpath >/dev/null 2>&1; then work=$(cygpath -m "$work"); fi
export KROKOSHA_STATE="$work/state" KROKOSHA_WWW="$work/www" KROKOSHA_ACCESS_LOG="$work/access.json.log"

# The CLI applies the migrations; «this login already exists» on later runs is fine.
printf 'local-dev-password\n' | go run ./cmd/krokosha-cli admin create dev --password-stdin || true
mysql krokosha_dev <dev/seed-demo.sql
echo "Admin area: http://localhost:8099/_dev/   login: dev   password: local-dev-password"
exec go run ./cmd/krokosha-api
