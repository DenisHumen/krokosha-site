#!/usr/bin/env bash
# Updates an installed site: pulls the configured branch, then re-applies the installer with the
# saved settings (it is idempotent) — new code, configs and units, a fresh build, an atomic switch.
# The server pulls from GitHub by itself; no deploy keys of the server are stored anywhere (brief B9).
#
#   sudo /opt/krokosha/repo/deploy/update.sh
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=deploy/lib/common.sh
source "$SCRIPT_DIR/lib/common.sh"

# main() makes bash read the whole file before the pull below may replace it.
main() {
  require_root "$@"
  [[ -d $KROKOSHA_REPO/.git && -f $KROKOSHA_ENV ]] || die "the site is not installed here: run deploy/install.sh first"

  local branch before after
  branch=$(env_get REPO_BRANCH)
  branch=${branch:-main}

  step "Pulling $branch"
  before=$(as_site_user git -C "$KROKOSHA_REPO" rev-parse --short HEAD)
  as_site_user git -C "$KROKOSHA_REPO" fetch --quiet --tags origin "$branch"
  as_site_user git -C "$KROKOSHA_REPO" checkout --quiet "$branch"
  if as_site_user git -C "$KROKOSHA_REPO" show-ref --verify --quiet "refs/remotes/origin/$branch"; then
    as_site_user git -C "$KROKOSHA_REPO" merge --quiet --ff-only "origin/$branch"
  fi
  after=$(as_site_user git -C "$KROKOSHA_REPO" rev-parse --short HEAD)
  if [[ $before == "$after" ]]; then
    ok "already at $after"
  else
    ok "$before → $after"
    as_site_user git -C "$KROKOSHA_REPO" log --oneline --no-decorate "$before..$after" | sed 's/^/      /' >&2
  fi

  # The freshly pulled installer does the rest.
  exec "$KROKOSHA_REPO/deploy/install.sh" --from-env --yes "$@"
}

main "$@"
