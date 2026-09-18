#!/usr/bin/env bash
# Installs the site on a clean server with one command; running it again changes nothing that is
# already in place (brief B8).
#
#   sudo ./install.sh --domain example.com --email admin@example.com
#
# It touches only its own files and services. Anything else living on the server — a VPN,
# other sites — keeps working: see «Firewall» below and docs/architecture.md §5.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=deploy/lib/common.sh
source "$SCRIPT_DIR/lib/common.sh"

# Everything lives in main(), called on the last line: bash then parses the whole file before it
# runs anything, so updating the repository (which may replace this very file) is safe.
main() {

DEFAULT_REPO=https://github.com/DenisHumen/krokosha-site.git

usage() {
  cat <<EOF
Usage: sudo $0 --domain DOMAIN --email EMAIL [options]

  --domain DOMAIN        the site's host name (www.DOMAIN is served too if it points here)
  --email EMAIL          administrator's address (Let's Encrypt account, later: alerts)

  --tls MODE             letsencrypt (default) | selfsigned (tests) | none (HTTP only)
  --agree-tos            accept the Let's Encrypt Subscriber Agreement without being asked
                         (https://letsencrypt.org/repository/)
  --staging              use the Let's Encrypt staging environment (untrusted test certificates)
  --allow PORT/PROTO     keep an extra port open in the firewall, e.g. --allow 51820/udp;
                         may be repeated. WireGuard interfaces are detected automatically
  --skip-firewall        do not configure UFW
  --skip-dns-check       do not verify that DOMAIN points to this server
  --repo URL             git repository to install from (default: $DEFAULT_REPO)
  --branch NAME          branch or tag (default: main)
  --from-env             take every setting from $KROKOSHA_ENV (what update.sh does)
  -y, --yes              never ask questions; fail if an answer is needed
  -h, --help             this text

Environment: GITHUB_TOKEN — optional read-only token for the GitHub API, stored in $KROKOSHA_ENV.
EOF
}

DOMAIN='' ADMIN_EMAIL='' TLS_MODE='' AGREE_TOS=no STAGING=no SKIP_FIREWALL='' SKIP_DNS=''
REPO_URL='' REPO_BRANCH='' FROM_ENV=no ASSUME_YES=no
EXTRA_PORTS=()

while [[ $# -gt 0 ]]; do
  case $1 in
    --domain) DOMAIN=${2:?--domain needs a value}; shift 2 ;;
    --email) ADMIN_EMAIL=${2:?--email needs a value}; shift 2 ;;
    --tls) TLS_MODE=${2:?--tls needs a value}; shift 2 ;;
    --agree-tos) AGREE_TOS=yes; shift ;;
    --staging) STAGING=yes; shift ;;
    --allow) EXTRA_PORTS+=("${2:?--allow needs a value}"); shift 2 ;;
    --skip-firewall) SKIP_FIREWALL=yes; shift ;;
    --skip-dns-check) SKIP_DNS=yes; shift ;;
    --repo) REPO_URL=${2:?--repo needs a value}; shift 2 ;;
    --branch) REPO_BRANCH=${2:?--branch needs a value}; shift 2 ;;
    --from-env) FROM_ENV=yes; shift ;;
    -y | --yes) ASSUME_YES=yes; shift ;;
    -h | --help) usage; exit 0 ;;
    *) usage >&2; die "unknown option: $1" ;;
  esac
done

require_root

# ---------------------------------------------------------------------------------------------
# Settings: command line → saved settings → questions
# ---------------------------------------------------------------------------------------------

ask() { # ask VAR "question"
  local answer
  [[ $ASSUME_YES == no && -t 0 ]] || die "$2 — pass it on the command line"
  read -r -p "$2: " answer
  printf -v "$1" '%s' "$answer"
}

if [[ $FROM_ENV == yes ]]; then
  [[ -f $KROKOSHA_ENV ]] || die "$KROKOSHA_ENV not found: run install.sh with --domain and --email first"
fi
: "${DOMAIN:=$(env_get DOMAIN)}"
: "${ADMIN_EMAIL:=$(env_get ADMIN_EMAIL)}"
: "${TLS_MODE:=$(env_get TLS_MODE)}"
: "${REPO_URL:=$(env_get REPO_URL)}"
: "${REPO_BRANCH:=$(env_get REPO_BRANCH)}"
if [[ ${#EXTRA_PORTS[@]} -eq 0 ]]; then
  read -r -a EXTRA_PORTS <<<"$(env_get FIREWALL_ALLOW)"
fi
# Remembered too: an update must not switch on a firewall the operator declined,
# nor start failing on a DNS check that cannot pass behind NAT.
: "${SKIP_FIREWALL:=$(env_get SKIP_FIREWALL)}"
: "${SKIP_DNS:=$(env_get SKIP_DNS_CHECK)}"
: "${SKIP_FIREWALL:=no}"
: "${SKIP_DNS:=no}"
: "${TLS_MODE:=letsencrypt}"
: "${REPO_URL:=$DEFAULT_REPO}"
: "${REPO_BRANCH:=main}"

[[ -n $DOMAIN ]] || ask DOMAIN "Domain of the site (for example krokosha.xyz)"
[[ -n $ADMIN_EMAIL ]] || ask ADMIN_EMAIL "Administrator's email"
DOMAIN=${DOMAIN,,}

valid_domain "$DOMAIN" || die "not a valid domain name: $DOMAIN"
valid_email "$ADMIN_EMAIL" || die "not a valid email address: $ADMIN_EMAIL"
case $TLS_MODE in letsencrypt | selfsigned | none) ;; *) die "--tls must be letsencrypt, selfsigned or none" ;; esac
for port in "${EXTRA_PORTS[@]}"; do
  valid_port "$port" || die "--allow expects PORT/tcp or PORT/udp, got: $port"
done

WWW=$KROKOSHA_WWW
SITE_URL="https://$DOMAIN"
[[ $TLS_MODE == none ]] && SITE_URL="http://$DOMAIN"

# ---------------------------------------------------------------------------------------------
step "Checking the server"
# ---------------------------------------------------------------------------------------------

# shellcheck disable=SC1091
source /etc/os-release
case "${ID:-}:${VERSION_ID:-}" in
  ubuntu:24.04 | debian:12) ok "$PRETTY_NAME" ;;
  *) warn "$PRETTY_NAME is untested; supported: Ubuntu 24.04, Debian 12" ;;
esac

case $(uname -m) in
  x86_64) GO_ARCH=amd64 NODE_ARCH=x64 ;;
  aarch64) GO_ARCH=arm64 NODE_ARCH=arm64 ;;
  *) die "unsupported CPU architecture: $(uname -m)" ;;
esac

mem_mb=$(awk '/^MemTotal:/ {print int($2 / 1024)}' /proc/meminfo)
swap_mb=$(awk '/^SwapTotal:/ {print int($2 / 1024)}' /proc/meminfo)
((mem_mb + swap_mb >= 1500)) || die "not enough memory to build the site: ${mem_mb} MB RAM + ${swap_mb} MB swap, need 1.5 GB in total"
free_mb=$(df -Pm /opt 2>/dev/null | awk 'NR == 2 {print $4}')
((${free_mb:-0} >= 3000)) || die "not enough disk space: ${free_mb:-?} MB free, need 3 GB"
ok "memory ${mem_mb} MB (+${swap_mb} MB swap), disk ${free_mb} MB free"

# Ports 80 and 443 must be free or already ours.
if have ss; then
  foreign=$(ss -Htlnp '( sport = :80 or sport = :443 )' 2>/dev/null | grep -v '"nginx"' || true)
  [[ -z $foreign ]] || die "ports 80/443 are taken by another program:"$'\n'"$foreign"
fi

# Does the domain point here? Without it Let's Encrypt cannot validate, and visitors see nothing.
SERVE_WWW=no
resolves_here() {
  local name=$1 resolved local_ips ip
  resolved=$(getent ahostsv4 "$name" 2>/dev/null | awk '{print $1}' | sort -u)
  [[ -n $resolved ]] || return 1
  local_ips=$(ip -4 -o addr show scope global | awk '{sub(/\/.*/, "", $4); print $4}')
  for ip in $resolved; do
    grep -qxF "$ip" <<<"$local_ips" && return 0
  done
  return 1
}
if [[ $SKIP_DNS == yes ]]; then
  warn "DNS check skipped"
  SERVE_WWW=yes
else
  resolves_here "$DOMAIN" || die "$DOMAIN does not resolve to an address of this server. Fix the A record, or pass --skip-dns-check if the server is behind NAT"
  ok "$DOMAIN points to this server"
  if resolves_here "www.$DOMAIN"; then
    SERVE_WWW=yes
    ok "www.$DOMAIN points to this server"
  else
    warn "www.$DOMAIN does not point here: it will not be served"
  fi
fi

if [[ $TLS_MODE == letsencrypt && ! -d /etc/letsencrypt/live/$DOMAIN && $AGREE_TOS == no ]]; then
  log "A certificate will be requested from Let's Encrypt for $ADMIN_EMAIL."
  log "This requires accepting their Subscriber Agreement: https://letsencrypt.org/repository/"
  answer=''
  ask answer "Do you accept it? [y/N]"
  [[ ${answer,,} == y || ${answer,,} == yes ]] || die "the agreement was not accepted; use --tls none or --tls selfsigned to go on without Let's Encrypt"
  AGREE_TOS=yes
fi

# ---------------------------------------------------------------------------------------------
step "Installing packages"
# ---------------------------------------------------------------------------------------------

export DEBIAN_FRONTEND=noninteractive
packages=(nginx git curl ca-certificates xz-utils rsync openssl logrotate)
[[ $TLS_MODE == letsencrypt ]] && packages+=(certbot)
[[ $SKIP_FIREWALL == no ]] && packages+=(ufw)
packages+=(fail2ban python3-systemd)
# A freshly installed nginx or module needs a restart, not a reload, to be picked up.
NGINX_RESTART=no
dpkg-query -W -f='${Status}' libnginx-mod-http-brotli-filter 2>/dev/null | grep -q 'ok installed' || NGINX_RESTART=yes
missing=()
for package in "${packages[@]}"; do
  dpkg-query -W -f='${Status}' "$package" 2>/dev/null | grep -q 'ok installed' || missing+=("$package")
done
if [[ ${#missing[@]} -gt 0 ]]; then
  log "apt-get install ${missing[*]}"
  apt-get update -qq
  apt-get install -y -qq --no-install-recommends "${missing[@]}" >/dev/null
fi
# Brotli is optional: a smaller download for browsers, but the site works without it.
HAVE_BROTLI=no
if apt-get install -y -qq --no-install-recommends libnginx-mod-http-brotli-filter >/dev/null 2>&1; then
  HAVE_BROTLI=yes
fi
ok "packages are in place (brotli: $HAVE_BROTLI)"

# ---------------------------------------------------------------------------------------------
step "User and directories"
# ---------------------------------------------------------------------------------------------

if ! id "$KROKOSHA_USER" >/dev/null 2>&1; then
  useradd --system --home-dir "$KROKOSHA_STATE" --no-create-home --shell /usr/sbin/nologin "$KROKOSHA_USER"
  ok "created system user $KROKOSHA_USER (no shell, no password)"
fi
install -d -m 0755 -o root -g root "$KROKOSHA_ROOT" "$KROKOSHA_ROOT/bin" "$KROKOSHA_ROOT/toolchain"
install -d -m 0750 -o "$KROKOSHA_USER" -g "$KROKOSHA_USER" "$KROKOSHA_STATE" "$KROKOSHA_STATE/cache"
install -d -m 0755 -o "$KROKOSHA_USER" -g "$KROKOSHA_USER" "$WWW" "$WWW/releases"
install -d -m 0755 -o root -g root "$WWW/acme"
install -d -m 0750 -o root -g "$KROKOSHA_USER" "$KROKOSHA_ETC"
install -d -m 0755 -o root -g adm /var/log/krokosha
ok "$KROKOSHA_ROOT, $KROKOSHA_STATE, $WWW, $KROKOSHA_ETC"

# ---------------------------------------------------------------------------------------------
step "Build toolchain (official tarballs, verified by SHA-256)"
# ---------------------------------------------------------------------------------------------

# shellcheck source=deploy/toolchain.env
source "$SCRIPT_DIR/toolchain.env"

install_tarball() { # NAME VERSION URL SHA256 — unpacks into toolchain/NAME-VERSION, links toolchain/NAME
  local name=$1 version=$2 url=$3 sha256=$4
  local target="$KROKOSHA_ROOT/toolchain/$name-$version" archive
  if [[ ! -d $target ]]; then
    archive=$(mktemp)
    log "downloading $url"
    curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --output "$archive" "$url"
    if ! echo "$sha256  $archive" | sha256sum --check --status; then
      rm -f "$archive"
      die "checksum mismatch for $url — refusing to install it"
    fi
    rm -rf "$target.partial"
    mkdir -p "$target.partial"
    tar -xf "$archive" -C "$target.partial" --strip-components=1
    rm -f "$archive"
    mv "$target.partial" "$target"
  fi
  ln -sfn "$target" "$KROKOSHA_ROOT/toolchain/$name"
  # Older versions are no longer needed once the new one is linked.
  find "$KROKOSHA_ROOT/toolchain" -mindepth 1 -maxdepth 1 -type d -name "$name-*" ! -name "$name-$version" -exec rm -rf {} +
}

go_sha=GO_SHA256_$GO_ARCH node_sha=NODE_SHA256_$NODE_ARCH
install_tarball go "$GO_VERSION" "https://go.dev/dl/go$GO_VERSION.linux-$GO_ARCH.tar.gz" "${!go_sha}"
install_tarball node "$NODE_VERSION" "https://nodejs.org/dist/v$NODE_VERSION/node-v$NODE_VERSION-linux-$NODE_ARCH.tar.xz" "${!node_sha}"
ok "go $GO_VERSION, node $NODE_VERSION in $KROKOSHA_ROOT/toolchain"

# ---------------------------------------------------------------------------------------------
step "Source code"
# ---------------------------------------------------------------------------------------------

if [[ ! -d $KROKOSHA_REPO/.git ]]; then
  install -d -m 0755 -o "$KROKOSHA_USER" -g "$KROKOSHA_USER" "$KROKOSHA_REPO"
  if [[ $REPO_URL == /* || $REPO_URL == file://* ]]; then
    # A local checkout (tests): its owner differs from the site user, which git refuses by default.
    as_site_user git config --global --add safe.directory "${REPO_URL#file://}/.git"
    as_site_user git config --global --add safe.directory "${REPO_URL#file://}"
  fi
  as_site_user git -c init.defaultBranch=main -c advice.detachedHead=false clone --quiet --branch "$REPO_BRANCH" "$REPO_URL" "$KROKOSHA_REPO"
  ok "cloned $REPO_URL ($REPO_BRANCH)"
else
  as_site_user git -C "$KROKOSHA_REPO" remote set-url origin "$REPO_URL"
  as_site_user git -C "$KROKOSHA_REPO" fetch --quiet --tags origin "$REPO_BRANCH"
  as_site_user git -C "$KROKOSHA_REPO" checkout --quiet "$REPO_BRANCH"
  if as_site_user git -C "$KROKOSHA_REPO" show-ref --verify --quiet "refs/remotes/origin/$REPO_BRANCH"; then
    as_site_user git -C "$KROKOSHA_REPO" merge --quiet --ff-only "origin/$REPO_BRANCH"
  fi
  ok "updated to $(as_site_user git -C "$KROKOSHA_REPO" rev-parse --short HEAD)"
fi
# From here on the templates and scripts of the installed revision are used, so that update.sh
# applies exactly what was pulled.
DEPLOY="$KROKOSHA_REPO/deploy"

# ---------------------------------------------------------------------------------------------
step "Settings ($KROKOSHA_ENV)"
# ---------------------------------------------------------------------------------------------

env_set DOMAIN "$DOMAIN"
env_set ADMIN_EMAIL "$ADMIN_EMAIL"
env_set SITE_URL "$SITE_URL"
env_set TLS_MODE "$TLS_MODE"
env_set REPO_URL "$REPO_URL"
env_set REPO_BRANCH "$REPO_BRANCH"
env_set FIREWALL_ALLOW "${EXTRA_PORTS[*]:-}"
env_set SKIP_FIREWALL "$SKIP_FIREWALL"
env_set SKIP_DNS_CHECK "$SKIP_DNS"
if [[ -n ${GITHUB_TOKEN:-} ]]; then
  env_set GITHUB_TOKEN "$GITHUB_TOKEN"
elif [[ -z $(env_get GITHUB_TOKEN) ]]; then
  env_set GITHUB_TOKEN ""
  warn "no GITHUB_TOKEN: the GitHub API allows 60 requests per hour, enough thanks to caching. To raise it, put a read-only token into $KROKOSHA_ENV"
fi
ok "saved (mode 600, readable by root only)"

# ---------------------------------------------------------------------------------------------
step "Building the CLI and the site"
# ---------------------------------------------------------------------------------------------

as_site_user env GOCACHE="$KROKOSHA_STATE/cache/go-build" GOPATH="$KROKOSHA_STATE/cache/go" GOFLAGS=-mod=readonly GOTOOLCHAIN=local CGO_ENABLED=0 \
  go -C "$KROKOSHA_REPO/api" build -trimpath -ldflags '-s -w' -o "$KROKOSHA_STATE/cache/krokosha-cli" ./cmd/krokosha-cli
install -m 0755 -o root -g root "$KROKOSHA_STATE/cache/krokosha-cli" "$KROKOSHA_ROOT/bin/krokosha-cli"
ok "$KROKOSHA_ROOT/bin/krokosha-cli"

for unit in krokosha-sync.service krokosha-sync.timer; do
  install_if_changed "$DEPLOY/systemd/$unit" "/etc/systemd/system/$unit" || true
done
systemctl daemon-reload
# The first build goes through the same unit as every later one: same user, same sandbox.
if ! systemctl start krokosha-sync.service; then
  journalctl -u krokosha-sync.service -n 40 --no-pager >&2 || true
  die "the site build failed (log above)"
fi
[[ -f $WWW/current/index.html ]] || die "the build finished but $WWW/current/index.html is missing"
ok "release $(basename "$(readlink -f "$WWW/current")") is live in $WWW/current"

# ---------------------------------------------------------------------------------------------
step "Nginx"
# ---------------------------------------------------------------------------------------------

install_if_changed "$DEPLOY/nginx/krokosha-http.conf" /etc/nginx/conf.d/krokosha-http.conf || true
install_if_changed "$DEPLOY/nginx/snippets/krokosha-tls.conf" /etc/nginx/snippets/krokosha-tls.conf || true
install_if_changed "$DEPLOY/logrotate/krokosha" /etc/logrotate.d/krokosha || true

tmp=$(mktemp)
cat "$DEPLOY/nginx/snippets/krokosha-gzip.conf" >"$tmp"
[[ $HAVE_BROTLI == yes ]] && cat "$DEPLOY/nginx/snippets/krokosha-brotli.conf" >>"$tmp"
install_if_changed "$tmp" /etc/nginx/snippets/krokosha-compression.conf || true

write_headers() { # with_hsts
  cat "$DEPLOY/nginx/snippets/krokosha-headers.conf" >"$tmp"
  [[ $1 == yes ]] && cat "$DEPLOY/nginx/snippets/krokosha-hsts.conf" >>"$tmp"
  install_if_changed "$tmp" /etc/nginx/snippets/krokosha-headers.conf || true
}

# SERVER_NAMES, TLS_CERT and TLS_KEY are read by render() through the @@NAME@@ placeholders.
# shellcheck disable=SC2034
SERVER_NAMES=$DOMAIN
[[ $SERVE_WWW == yes ]] && SERVER_NAMES="$DOMAIN www.$DOMAIN"

write_site() { # http-only | https
  {
    render "$DEPLOY/nginx/site-default.conf.tmpl"
    if [[ $1 == https ]]; then
      render "$DEPLOY/nginx/site-default-tls.conf.tmpl"
      render "$DEPLOY/nginx/site-http.conf.tmpl"
      render "$DEPLOY/nginx/site-https.conf.tmpl"
      [[ $SERVE_WWW == yes ]] && render "$DEPLOY/nginx/site-www.conf.tmpl"
    else
      render "$DEPLOY/nginx/site-http-only.conf.tmpl"
    fi
    true
  } >"$tmp"
  install_if_changed "$tmp" /etc/nginx/sites-available/krokosha.conf || true
  ln -sfn ../sites-available/krokosha.conf /etc/nginx/sites-enabled/krokosha.conf
  # Ubuntu's placeholder site would compete for the default_server role.
  rm -f /etc/nginx/sites-enabled/default
  nginx -t 2>/dev/null || { nginx -t || true; die "nginx rejected the configuration (see above)"; }
  systemctl enable --quiet nginx
  if [[ $NGINX_RESTART == yes ]] || ! systemctl is-active --quiet nginx; then
    systemctl restart nginx
    NGINX_RESTART=no
  else
    systemctl reload nginx
  fi
}

# shellcheck disable=SC2034
TLS_CERT='' TLS_KEY=''
case $TLS_MODE in
  none)
    write_headers no
    write_site http-only
    ok "serving http://$DOMAIN (no TLS)"
    ;;
  selfsigned)
    TLS_CERT=$KROKOSHA_ETC/tls/selfsigned.crt TLS_KEY=$KROKOSHA_ETC/tls/selfsigned.key
    if [[ ! -f $TLS_CERT ]]; then
      install -d -m 0750 -o root -g root "$KROKOSHA_ETC/tls"
      openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 30 \
        -subj "/CN=$DOMAIN" -addext "subjectAltName=DNS:$DOMAIN,DNS:www.$DOMAIN" \
        -keyout "$TLS_KEY" -out "$TLS_CERT" 2>/dev/null
      chmod 0600 "$TLS_KEY"
    fi
    write_headers no
    write_site https
    warn "serving https://$DOMAIN with a SELF-SIGNED certificate — for tests only"
    ;;
  letsencrypt)
    TLS_CERT=/etc/letsencrypt/live/$DOMAIN/fullchain.pem TLS_KEY=/etc/letsencrypt/live/$DOMAIN/privkey.pem
    if [[ ! -f $TLS_CERT ]]; then
      # The challenge is answered over plain HTTP, so the site goes up on port 80 first.
      write_headers no
      write_site http-only
      certbot_args=(certonly --webroot --webroot-path "$WWW/acme" --cert-name "$DOMAIN" -d "$DOMAIN"
        --non-interactive --agree-tos --email "$ADMIN_EMAIL" --no-eff-email --keep-until-expiring)
      [[ $SERVE_WWW == yes ]] && certbot_args+=(-d "www.$DOMAIN")
      [[ $STAGING == yes ]] && certbot_args+=(--staging)
      log "requesting a certificate for $SERVER_NAMES"
      certbot "${certbot_args[@]}" || die "Let's Encrypt did not issue a certificate. The site stays on http://$DOMAIN; fix the problem and run the installer again"
    fi
    # certbot.timer (from the package) renews; nginx has to pick the new files up.
    install -d -m 0755 /etc/letsencrypt/renewal-hooks/deploy
    printf '#!/bin/sh\n# Installed by krokosha-site: load the renewed certificate.\nsystemctl reload nginx\n' >"$tmp"
    install_if_changed "$tmp" /etc/letsencrypt/renewal-hooks/deploy/krokosha-reload-nginx 0755 || true
    systemctl enable --quiet --now certbot.timer
    write_headers yes
    write_site https
    ok "serving https://$DOMAIN, certificate renews automatically"
    ;;
esac
rm -f "$tmp"

# ---------------------------------------------------------------------------------------------
step "Timer: GitHub sync and rebuild every 6 hours"
# ---------------------------------------------------------------------------------------------

systemctl enable --quiet --now krokosha-sync.timer
ok "next run: $(systemctl show krokosha-sync.timer --property=NextElapseUSecRealtime --value)"

# ---------------------------------------------------------------------------------------------
step "Firewall"
# ---------------------------------------------------------------------------------------------

if [[ $SKIP_FIREWALL == yes ]]; then
  warn "skipped (--skip-firewall)"
else
  ip_forward_before=$(sysctl -n net.ipv4.ip_forward)

  # Whatever happens, SSH stays reachable: the rule is added first and verified before enabling.
  mapfile -t ssh_ports < <(sshd -T 2>/dev/null | awk '$1 == "port" {print $2}')
  [[ ${#ssh_ports[@]} -gt 0 ]] || ssh_ports=(22)
  for port in "${ssh_ports[@]}"; do
    ufw allow "$port/tcp" comment 'SSH' >/dev/null
  done
  ufw allow 80/tcp comment 'krokosha: HTTP (ACME, redirect)' >/dev/null
  ufw allow 443/tcp comment 'krokosha: HTTPS' >/dev/null
  for port in "${EXTRA_PORTS[@]}"; do
    ufw allow "$port" comment 'krokosha: --allow' >/dev/null
    log "kept open on request: $port"
  done

  # A VPN already running here must survive (docs/architecture.md §5): keep its port open and
  # let its clients be routed, because UFW's default for forwarded traffic is to drop it.
  if have wg; then
    for iface in $(wg show interfaces 2>/dev/null); do
      wg_port=$(wg show "$iface" listen-port 2>/dev/null || true)
      [[ -n $wg_port && $wg_port != 0 ]] && ufw allow "$wg_port/udp" comment "WireGuard $iface" >/dev/null
      ufw route allow in on "$iface" comment "WireGuard $iface clients" >/dev/null
      log "WireGuard $iface detected: port ${wg_port:-?}/udp and client routing stay allowed"
    done
  fi

  ufw default deny incoming >/dev/null
  ufw default allow outgoing >/dev/null
  for port in "${ssh_ports[@]}"; do
    ufw show added | grep -qE "^ufw allow $port/tcp" || die "the SSH rule for port $port is missing — not enabling the firewall"
  done
  ufw --force enable >/dev/null

  if [[ $ip_forward_before == 1 && $(sysctl -n net.ipv4.ip_forward) != 1 ]]; then
    sysctl -q -w net.ipv4.ip_forward=1
    sed -i 's|^#\?net/ipv4/ip_forward=.*|net/ipv4/ip_forward=1|' /etc/ufw/sysctl.conf
    warn "UFW had switched packet forwarding off; it is back on and pinned in /etc/ufw/sysctl.conf"
  fi
  ok "UFW is active: SSH (${ssh_ports[*]}), 80, 443${EXTRA_PORTS[*]:+, ${EXTRA_PORTS[*]}}; everything else incoming is denied"
fi

# ---------------------------------------------------------------------------------------------
step "fail2ban"
# ---------------------------------------------------------------------------------------------

if install_if_changed "$DEPLOY/fail2ban/krokosha.conf" /etc/fail2ban/jail.d/krokosha.conf; then
  systemctl enable --quiet fail2ban || true
  systemctl restart fail2ban || true
fi
if systemctl is-active --quiet fail2ban; then
  ok "sshd jail is on"
else
  warn "fail2ban is not running — check: journalctl -u fail2ban. The site itself is not affected"
fi

# ---------------------------------------------------------------------------------------------
step "Done"
# ---------------------------------------------------------------------------------------------

cat >&2 <<EOF

  Site:       $SITE_URL
  Release:    $(readlink -f "$WWW/current")
  Rebuild:    sudo systemctl start krokosha-sync.service     (runs by itself every 6 hours)
  Update:     sudo $KROKOSHA_REPO/deploy/update.sh
  Roll back:  sudo $KROKOSHA_REPO/deploy/rollback.sh
  Logs:       journalctl -u krokosha-sync.service, /var/log/krokosha/
  Settings:   $KROKOSHA_ENV

EOF
}

main "$@"
