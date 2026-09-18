#!/usr/bin/env bash
# Local staging: a Docker container that looks like the VPS, with the site installed in it by the
# real deploy/install.sh. Nothing here touches a real server.
#
#   deploy/docker/staging/staging.sh up        build the image, start the container, install the site
#                                              from the CURRENT BRANCH (committed state) → https://krokosha.localhost
#   deploy/docker/staging/staging.sh update    pull the current branch into the container and re-apply (update.sh)
#   deploy/docker/staging/staging.sh test      full installer test cycle in a throw-away container
#                                              (install, re-run, update, rollback, uninstall — what CI runs)
#   deploy/docker/staging/staging.sh shell     root shell inside
#   deploy/docker/staging/staging.sh logs      journal of the site's services
#   deploy/docker/staging/staging.sh down      stop and remove the container (volumes too)
#
# Requires Docker (Docker Desktop on Windows/macOS works; run this from Git Bash or WSL).
# The certificate is self-signed: the browser will warn once.
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd "$HERE/../../.." && pwd)
IMAGE=krokosha-staging
NAME=${KROKOSHA_STAGING_NAME:-krokosha-staging}
DOMAIN=${KROKOSHA_STAGING_DOMAIN:-krokosha.localhost}
HTTP_PORT=${KROKOSHA_STAGING_HTTP_PORT:-80}
HTTPS_PORT=${KROKOSHA_STAGING_HTTPS_PORT:-443}

# Git Bash on Windows rewrites arguments that look like POSIX paths (/src, /sys/fs/cgroup…);
# Docker needs them as they are. Only for docker: git must keep getting converted paths.
docker() {
  MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*' command docker "$@"
}

host_path() { # path of the repository as the Docker daemon sees it
  if command -v cygpath >/dev/null 2>&1; then cygpath -w "$REPO"; else printf '%s' "$REPO"; fi
}

build() {
  docker build --quiet --tag "$IMAGE" "$(if command -v cygpath >/dev/null 2>&1; then cygpath -w "$HERE"; else printf '%s' "$HERE"; fi)" >/dev/null
}

start() { # NAME [docker run options…]
  local name=$1
  shift
  docker rm --force --volumes "$name" >/dev/null 2>&1 || true
  # Docker runs inside too (MySQL, Redis, mail). Its image stores need a real filesystem —
  # overlay on top of overlay does not mount — so they get volumes, fresh for every start.
  docker volume rm "$name-docker" "$name-containerd" >/dev/null 2>&1 || true
  docker run --detach --name "$name" --hostname staging \
    --privileged --cgroupns=host \
    --volume /sys/fs/cgroup:/sys/fs/cgroup:rw \
    --tmpfs /run --tmpfs /run/lock --tmpfs /tmp:exec \
    --volume "$(host_path):/src:ro" \
    --volume "$name-docker:/var/lib/docker" \
    --volume "$name-containerd:/var/lib/containerd" \
    "$@" "$IMAGE" >/dev/null
  # Wait until systemd is up.
  for _ in $(seq 1 60); do
    if docker exec "$name" systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded'; then
      return 0
    fi
    sleep 1
  done
  echo "systemd did not come up in the container" >&2
  docker logs "$name" | tail -n 20 >&2
  return 1
}

current_branch() {
  local branch
  branch=$(git -C "$REPO" branch --show-current)
  [[ -n $branch ]] || { echo "detached HEAD: switch to a branch first" >&2; exit 1; }
  if [[ -n $(git -C "$REPO" status --porcelain) ]]; then
    echo "warning: uncommitted changes are NOT installed — the installer clones the committed branch '$branch'" >&2
  fi
  printf '%s' "$branch"
}

case ${1:-} in
  up)
    branch=$(current_branch)
    build
    start "$NAME" --publish "$HTTP_PORT:80" --publish "$HTTPS_PORT:443"
    # A throw-away administrator with a random password, new for every `up`.
    docker exec "$NAME" bash -c "umask 077 && od -An -N9 -tx1 /dev/urandom | tr -dc 0-9a-f >/root/admin-password && echo >>/root/admin-password"
    docker exec --env GITHUB_TOKEN="${GITHUB_TOKEN:-}" "$NAME" /src/deploy/install.sh \
      --domain "$DOMAIN" --email dev@example.com --repo /src --branch "$branch" \
      --tls selfsigned --skip-dns-check --yes \
      --admin-path /_staging --admin-login dev --admin-password-file /root/admin-password
    base="https://$DOMAIN$([[ $HTTPS_PORT == 443 ]] || printf ':%s' "$HTTPS_PORT")"
    echo
    echo "Staging is up: $base/"
    echo "Admin area:    $base/_staging/   login: dev   password: $(docker exec "$NAME" cat /root/admin-password)"
    ;;
  update)
    branch=$(current_branch)
    docker exec "$NAME" sed -i "s|^REPO_BRANCH=.*|REPO_BRANCH=$branch|" /etc/krokosha/env
    docker exec "$NAME" /opt/krokosha/repo/deploy/update.sh
    ;;
  test)
    branch=$(current_branch)
    build
    start "$NAME-test"
    # The CI script installs from a checkout it can clone as branch "ci-test".
    docker exec "$NAME-test" bash -c "git config --global --add safe.directory /src/.git && git config --global --add safe.directory /src \
      && git clone --quiet --branch '$branch' /src /srv/krokosha-src && git -C /srv/krokosha-src switch --quiet --create ci-test"
    status=0
    docker exec --env GITHUB_TOKEN="${GITHUB_TOKEN:-}" "$NAME-test" /srv/krokosha-src/deploy/ci/test-install.sh /srv/krokosha-src || status=$?
    docker rm --force --volumes "$NAME-test" >/dev/null
    docker volume rm "$NAME-test-docker" "$NAME-test-containerd" >/dev/null 2>&1 || true
    exit "$status"
    ;;
  shell)
    exec docker exec --interactive --tty "$NAME" bash
    ;;
  logs)
    docker exec "$NAME" journalctl --no-pager --lines "${2:-80}" \
      --unit 'krokosha-*' --unit nginx
    ;;
  down)
    docker rm --force --volumes "$NAME" >/dev/null 2>&1 || true
    docker volume rm "$NAME-docker" "$NAME-containerd" >/dev/null 2>&1 || true
    echo "removed $NAME"
    ;;
  *)
    sed -n '2,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
    exit 2
    ;;
esac
