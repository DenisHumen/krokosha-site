#!/usr/bin/env bash
# Builds the site and publishes it as a new release (brief B2, B3):
#
#   GitHub sync → astro build → check-dist → releases/<timestamp> → atomic switch of `current`
#
# Runs as the unprivileged site user: from krokosha-sync.timer every 6 hours, from install.sh and
# update.sh. A failed step leaves the live release untouched.
set -euo pipefail

ROOT=${KROKOSHA_ROOT:-/opt/krokosha}
REPO=${KROKOSHA_REPO:-$ROOT/repo}
STATE=${KROKOSHA_STATE:-/var/lib/krokosha}
WWW=${KROKOSHA_WWW:-/var/www/krokosha}
KEEP_RELEASES=${KEEP_RELEASES:-3}

log() { printf '[build-release] %s\n' "$*"; }

# npm and the site build run third-party code. They get a clean environment: nothing from
# /etc/krokosha/env (GitHub token, later mail and bot credentials) is visible to them.
# The VPS has 2 GB of RAM shared with mail and the API, hence the heap limit.
node_env() {
  env -i \
    HOME="$STATE" \
    PATH="$ROOT/toolchain/node/bin:/usr/bin:/bin" \
    LANG=C.UTF-8 \
    SITE_URL="$SITE_URL" \
    NODE_OPTIONS="${BUILD_NODE_OPTIONS:---max-old-space-size=768}" \
    ASTRO_TELEMETRY_DISABLED=1 \
    npm_config_cache="$STATE/cache/npm" \
    npm_config_update_notifier=false \
    npm_config_fund=false \
    npm_config_audit=false \
    "$@"
}

[[ -n ${SITE_URL:-} ]] || { log "SITE_URL is not set (expected from /etc/krokosha/env)"; exit 1; }

mkdir -p "$STATE/cache" "$WWW/releases"
exec 9>"$STATE/build.lock"
if ! flock -n 9; then
  log "another build is running, nothing to do"
  exit 0
fi

# 1. GitHub data. A failure is not fatal: the site is built from the cached answer,
#    or from the committed snapshot on the very first run.
if "$ROOT/bin/krokosha-cli" sync --content "$REPO/content" --cache "$STATE/cache/github"; then
  log "GitHub sync: ok"
else
  log "GitHub sync failed — building from what is already there"
fi

# 2. Dependencies, only when the lock file changed.
cd "$REPO/web"
lock_hash=$(sha256sum package-lock.json | cut -d' ' -f1)
if [[ ! -d node_modules || $(cat node_modules/.lock-hash 2>/dev/null) != "$lock_hash" ]]; then
  log "installing npm dependencies"
  node_env npm ci --omit=dev
  printf '%s\n' "$lock_hash" >node_modules/.lock-hash
fi

# 3. Build and verify. Nothing is published unless the checks pass.
log "building the site for $SITE_URL"
rm -rf dist
node_env npm run build
node_env node scripts/check-dist.mjs dist

# 4. Publish: copy, then switch the symlink atomically.
release="$WWW/releases/$(date -u +%Y%m%d-%H%M%S)"
cp -a dist "$release"
chmod -R u=rwX,go=rX "$release"
ln -sfn "$release" "$WWW/current.new"
mv -T "$WWW/current.new" "$WWW/current"
log "published $release"

# 5. Keep the last releases for rollback.
live=$(readlink -f "$WWW/current")
find "$WWW/releases" -mindepth 1 -maxdepth 1 -type d | sort -r | tail -n "+$((KEEP_RELEASES + 1))" |
  while read -r old; do
    [[ $(readlink -f "$old") == "$live" ]] && continue
    rm -rf "$old"
    log "removed old release $old"
  done
