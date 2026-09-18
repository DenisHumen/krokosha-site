#!/usr/bin/env bash
# Switches the live site back to an earlier release (the last three are kept).
#
#   sudo ./rollback.sh            the release before the current one
#   sudo ./rollback.sh --list     show the releases
#   sudo ./rollback.sh NAME       a particular release, e.g. 20260918-061012
#
# The rebuild timer is paused, otherwise the next run would publish a new release on top.
# Resume with: sudo systemctl start krokosha-sync.timer   (deploy/update.sh does it too)
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=deploy/lib/common.sh
source "$SCRIPT_DIR/lib/common.sh"

require_root

releases_dir="$KROKOSHA_WWW/releases"
current=$(basename "$(readlink -f "$KROKOSHA_WWW/current")")
mapfile -t releases < <(find "$releases_dir" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort)

if [[ ${1:-} == --list ]]; then
  for release in "${releases[@]}"; do
    if [[ $release == "$current" ]]; then
      printf '%s  ← live\n' "$release"
    else
      printf '%s\n' "$release"
    fi
  done
  exit 0
fi

target=${1:-}
if [[ -z $target ]]; then
  for release in "${releases[@]}"; do
    [[ $release < $current ]] && target=$release
  done
  [[ -n $target ]] || die "there is no release older than the live one ($current)"
fi
[[ $target =~ ^[0-9]{8}-[0-9]{6}$ && -f $releases_dir/$target/index.html ]] || die "no such release: $target (see --list)"
[[ $target != "$current" ]] || die "$target is already live"

ln -sfn "$releases_dir/$target" "$KROKOSHA_WWW/current.new"
chown -h "$KROKOSHA_USER:$KROKOSHA_USER" "$KROKOSHA_WWW/current.new"
mv -T "$KROKOSHA_WWW/current.new" "$KROKOSHA_WWW/current"
systemctl stop krokosha-sync.timer

ok "live: $target (was $current)"
warn "the rebuild timer is paused; resume with: sudo systemctl start krokosha-sync.timer"
