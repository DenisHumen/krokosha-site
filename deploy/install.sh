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
  --old-domain NAME      the name the site had before (a new --domain makes the previous one that by
                         itself): while NAME points here, its pages redirect to the same pages at
                         DOMAIN and its addresses get mail; the mailboxes move to DOMAIN with their
                         passwords and letters. It is let go once it points elsewhere, or with «none»

  --tls MODE             letsencrypt (default) | selfsigned (tests) | none (HTTP only)
  --agree-tos            accept the Let's Encrypt Subscriber Agreement without being asked
                         (https://letsencrypt.org/repository/)
  --staging              use the Let's Encrypt staging environment (untrusted test certificates)
  --allow PORT/PROTO     keep an extra port open in the firewall, e.g. --allow 51820/udp;
                         may be repeated. WireGuard interfaces are detected automatically
  --skip-firewall        do not configure UFW
  --skip-dns-check       do not verify that DOMAIN points to this server
  --data-dir DIR         where everything that must survive lives: database, mail, settings
                         (default: /srv/krokosha). Copy this one directory to move the site
  --admin-path PATH      secret path of the admin area, e.g. /_k7f3a9
                         (default: a random one, generated once and kept)
  --admin-login LOGIN    login of the first administrator (asked for if there is none yet)
  --admin-password-file FILE
                         take that administrator's password from the first line of FILE instead
                         of asking for it without echo — for automation; at least 12 characters
  --telegram-token-file FILE
                         token of the Telegram bot (from @BotFather) on the first line of FILE;
                         without it the installer asks, and an empty answer means «later».
                         The token is checked with Telegram (getMe) and kept in $KROKOSHA_ENV only
  --telegram-api URL     another Bot API server (a self-hosted one; tests). Default: Telegram's own
  --maxmind-account ID   MaxMind account for GeoLite2 (free: https://www.maxmind.com/en/geolite2/signup):
                         the countries and cities of visitors in the statistics. Needs
  --maxmind-key-file FILE
                         a license key of that account on the first line of FILE. Both are kept
                         in $KROKOSHA_ENV; geoipupdate fetches the database twice a week.
                         Without them the free DB-IP City Lite is used (no account, CC BY 4.0),
                         fetched once a month by krokosha-dbip.timer
  --dbip-url URL         another place of the DB-IP files (tests). Default: https://download.db-ip.com/free
  --netmap-offline       build the map of the internet (/map) only from the files already in
                         /var/lib/krokosha/netmap, never download its sources (tests)
  --no-mail              do not set up the mail server (it needs HTTPS, about 500 MB of memory,
                         and a provider that lets port 25 out)
  --mailbox ADDRESS      a mailbox to create besides the service one, e.g. denis@DOMAIN: letters
                         about requests go there, answers to clients are sent in its name
  --mail-name NAME       the name letters are signed with, e.g. "Denis Humen"
  --mailbox-password-file FILE
                         the password of that mailbox on the first line of FILE, instead of
                         asking for it without echo; at least 12 characters
  --no-indexnow          do not tell search engines about changed pages through IndexNow
                         (Bing, DuckDuckGo, Yandex…). On by default: a key is generated once
                         and served as /<key>.txt; every release submits the pages that changed
  --indexnow-api URL     another IndexNow endpoint (tests). Default: https://api.indexnow.org/indexnow
  --repo URL             git repository to install from (default: $DEFAULT_REPO)
  --branch NAME          branch or tag (default: main)
  --from-env             take every setting from $KROKOSHA_ENV (what update.sh does)
  -y, --yes              never ask questions; fail if an answer is needed
  -h, --help             this text

Environment: GITHUB_TOKEN — optional read-only token for the GitHub API, stored in $KROKOSHA_ENV.
EOF
}

DOMAIN='' OLD_DOMAIN='' ADMIN_EMAIL='' TLS_MODE='' AGREE_TOS=no STAGING=no SKIP_FIREWALL='' SKIP_DNS=''
REPO_URL='' REPO_BRANCH='' FROM_ENV=no ASSUME_YES=no DATA_DIR=''
ADMIN_PATH='' ADMIN_LOGIN='' ADMIN_PASSWORD_FILE=''
TELEGRAM_TOKEN_FILE='' TELEGRAM_API=''
MAXMIND_ACCOUNT='' MAXMIND_KEY_FILE='' DBIP_URL='' NETMAP_OFFLINE=no
INDEXNOW='' INDEXNOW_API=''
MAIL='' MAILBOX='' MAIL_NAME='' MAILBOX_PASSWORD_FILE=''
EXTRA_PORTS=()

while [[ $# -gt 0 ]]; do
  case $1 in
    --domain) DOMAIN=${2:?--domain needs a value}; shift 2 ;;
    --old-domain) OLD_DOMAIN=${2:?--old-domain needs a value}; shift 2 ;;
    --email) ADMIN_EMAIL=${2:?--email needs a value}; shift 2 ;;
    --tls) TLS_MODE=${2:?--tls needs a value}; shift 2 ;;
    --agree-tos) AGREE_TOS=yes; shift ;;
    --staging) STAGING=yes; shift ;;
    --allow) EXTRA_PORTS+=("${2:?--allow needs a value}"); shift 2 ;;
    --skip-firewall) SKIP_FIREWALL=yes; shift ;;
    --skip-dns-check) SKIP_DNS=yes; shift ;;
    --data-dir) DATA_DIR=${2:?--data-dir needs a value}; shift 2 ;;
    --admin-path) ADMIN_PATH=${2:?--admin-path needs a value}; shift 2 ;;
    --admin-login) ADMIN_LOGIN=${2:?--admin-login needs a value}; shift 2 ;;
    --admin-password-file) ADMIN_PASSWORD_FILE=${2:?--admin-password-file needs a value}; shift 2 ;;
    --telegram-token-file) TELEGRAM_TOKEN_FILE=${2:?--telegram-token-file needs a value}; shift 2 ;;
    --telegram-api) TELEGRAM_API=${2:?--telegram-api needs a value}; shift 2 ;;
    --maxmind-account) MAXMIND_ACCOUNT=${2:?--maxmind-account needs a value}; shift 2 ;;
    --maxmind-key-file) MAXMIND_KEY_FILE=${2:?--maxmind-key-file needs a value}; shift 2 ;;
    --dbip-url) DBIP_URL=${2:?--dbip-url needs a value}; shift 2 ;;
    --netmap-offline) NETMAP_OFFLINE=yes; shift ;;
    --no-indexnow) INDEXNOW=no; shift ;;
    --indexnow-api) INDEXNOW_API=${2:?--indexnow-api needs a value}; shift 2 ;;
    --no-mail) MAIL=no; shift ;;
    --mailbox) MAILBOX=${2:?--mailbox needs a value}; shift 2 ;;
    --mail-name) MAIL_NAME=${2:?--mail-name needs a value}; shift 2 ;;
    --mailbox-password-file) MAILBOX_PASSWORD_FILE=${2:?--mailbox-password-file needs a value}; shift 2 ;;
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
: "${DATA_DIR:=$(env_get KROKOSHA_DATA)}"
: "${DATA_DIR:=/srv/krokosha}"
# Generated once and kept: bookmarks of the owner point there.
: "${ADMIN_PATH:=$(env_get ADMIN_PATH)}"
# (od, not openssl: on a bare server openssl arrives only with the packages below.)
: "${ADMIN_PATH:=/_$(od -An -N5 -tx1 /dev/urandom | tr -dc '0-9a-f')}"
: "${TLS_MODE:=letsencrypt}"
: "${REPO_URL:=$DEFAULT_REPO}"
: "${REPO_BRANCH:=main}"

[[ -n $DOMAIN ]] || ask DOMAIN "Domain of the site (for example krokosha.com)"
[[ -n $ADMIN_EMAIL ]] || ask ADMIN_EMAIL "Administrator's email"
DOMAIN=${DOMAIN,,}

valid_domain "$DOMAIN" || die "not a valid domain name: $DOMAIN"
valid_email "$ADMIN_EMAIL" || die "not a valid email address: $ADMIN_EMAIL"
# A new name for the site: the one it had until now becomes its old name (deploy/README.md,
# «Смена домена»). An old name is remembered until it is let go.
PREVIOUS_DOMAIN=$(env_get DOMAIN)
OLD_BEFORE=$(env_get OLD_DOMAIN)
if [[ -z $OLD_DOMAIN ]]; then
  OLD_DOMAIN=$OLD_BEFORE
  [[ -z $PREVIOUS_DOMAIN || $PREVIOUS_DOMAIN == "$DOMAIN" ]] || OLD_DOMAIN=$PREVIOUS_DOMAIN
fi
OLD_DOMAIN=${OLD_DOMAIN,,}
[[ $OLD_DOMAIN != none && $OLD_DOMAIN != "$DOMAIN" ]] || OLD_DOMAIN=''
[[ -z $OLD_DOMAIN ]] || valid_domain "$OLD_DOMAIN" || die "--old-domain: not a valid domain name: $OLD_DOMAIN"
# Old names that are being let go: their redirect, mail addresses and certificates are cleaned up.
FORGOTTEN_DOMAINS=()
[[ -z $OLD_BEFORE || $OLD_BEFORE == "$OLD_DOMAIN" || $OLD_BEFORE == "$DOMAIN" ]] || FORGOTTEN_DOMAINS+=("$OLD_BEFORE")
case $TLS_MODE in letsencrypt | selfsigned | none) ;; *) die "--tls must be letsencrypt, selfsigned or none" ;; esac
[[ $DATA_DIR == /* && $DATA_DIR != / ]] || die "--data-dir must be an absolute path"
DATA_DIR=${DATA_DIR%/}
for port in "${EXTRA_PORTS[@]}"; do
  valid_port "$port" || die "--allow expects PORT/tcp or PORT/udp, got: $port"
done
ADMIN_PATH=${ADMIN_PATH%/}
valid_admin_path "$ADMIN_PATH" || die "--admin-path must look like /_k7f3a9: a slash, then 4 to 64 letters, digits, - or _ (and not a path of the site itself)"
[[ -z $ADMIN_LOGIN || $ADMIN_LOGIN =~ ^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$ ]] || die "--admin-login: 3 to 64 latin letters, digits, dots, - or _"
[[ -z $ADMIN_PASSWORD_FILE || -r $ADMIN_PASSWORD_FILE ]] || die "--admin-password-file: cannot read $ADMIN_PASSWORD_FILE"
[[ -z $ADMIN_PASSWORD_FILE || -n $ADMIN_LOGIN ]] || die "--admin-password-file needs --admin-login"
[[ -z $TELEGRAM_TOKEN_FILE || -r $TELEGRAM_TOKEN_FILE ]] || die "--telegram-token-file: cannot read $TELEGRAM_TOKEN_FILE"
# Mail: on unless refused once (the refusal is remembered), and impossible without TLS — mail
# programs and the site itself sign in with passwords.
: "${MAIL:=$(env_get MAIL)}"
: "${MAIL:=yes}"
if [[ $MAIL == yes && $TLS_MODE == none ]]; then
  warn "no mail server with --tls none: passwords would travel in clear text"
  MAIL=no
fi
: "${MAILBOX:=$(env_get MAILBOX)}"
MAILBOX=${MAILBOX,,}
# The site moves: the owner's mailbox moves with it (see «Mail server» below).
[[ -z $PREVIOUS_DOMAIN || $MAILBOX != *"@$PREVIOUS_DOMAIN" ]] || MAILBOX=${MAILBOX%@*}@$DOMAIN
[[ -z $MAILBOX || $MAILBOX =~ ^[a-z0-9][a-z0-9._-]*@${DOMAIN//./\\.}$ ]] || die "--mailbox must be an address at $DOMAIN, e.g. denis@$DOMAIN"
[[ $MAILBOX != "leads@$DOMAIN" ]] || die "--mailbox: leads@$DOMAIN is the service mailbox of the site; choose another address"
[[ -z $MAILBOX_PASSWORD_FILE || -r $MAILBOX_PASSWORD_FILE ]] || die "--mailbox-password-file: cannot read $MAILBOX_PASSWORD_FILE"
[[ -z $MAILBOX_PASSWORD_FILE || -n $MAILBOX ]] || die "--mailbox-password-file needs --mailbox"
case $MAIL_NAME in *[\"\<\>\\]*) die "--mail-name must not contain quotes, angle brackets or backslashes" ;; esac
MAIL_HOST="mail.$DOMAIN"
[[ -z $TELEGRAM_API || $TELEGRAM_API =~ ^https?://[^[:space:]]+$ ]] || die "--telegram-api must be an address like https://api.telegram.org"
# IndexNow: on unless refused once (the refusal is remembered).
: "${INDEXNOW:=$(env_get INDEXNOW)}"
: "${INDEXNOW:=yes}"
[[ -z $INDEXNOW_API || $INDEXNOW_API =~ ^https?://[^[:space:]]+$ ]] || die "--indexnow-api must be an address like https://api.indexnow.org/indexnow"
[[ -z $MAXMIND_ACCOUNT || $MAXMIND_ACCOUNT =~ ^[0-9]{1,12}$ ]] || die "--maxmind-account is a number (the account ID shown at maxmind.com)"
[[ -z $MAXMIND_KEY_FILE || -r $MAXMIND_KEY_FILE ]] || die "--maxmind-key-file: cannot read $MAXMIND_KEY_FILE"
[[ -z $MAXMIND_KEY_FILE || -n $MAXMIND_ACCOUNT || -n $(env_get MAXMIND_ACCOUNT_ID) ]] || die "--maxmind-key-file needs --maxmind-account"
[[ -z $MAXMIND_ACCOUNT || -n $MAXMIND_KEY_FILE || -n $(env_get MAXMIND_LICENSE_KEY) ]] || die "--maxmind-account needs --maxmind-key-file"
[[ -z $DBIP_URL || $DBIP_URL =~ ^https?://[^[:space:]]+$ ]] || die "--dbip-url must be an address like https://download.db-ip.com/free"

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
  # The old name serves for as long as it points here: a domain nobody renews stops one day.
  if [[ -n $OLD_DOMAIN ]]; then
    if resolves_here "$OLD_DOMAIN"; then
      ok "$OLD_DOMAIN, the old name, points here too: its pages go to $DOMAIN, its mail arrives"
    else
      warn "$OLD_DOMAIN, the old name, points here no more: it is let go — its redirect, mail addresses and certificate"
      FORGOTTEN_DOMAINS+=("$OLD_DOMAIN")
      OLD_DOMAIN=''
    fi
  fi
fi

SERVE_MAIL_NAME=no
if [[ $MAIL == yes ]]; then
  if [[ $SKIP_DNS == yes ]] || resolves_here "$MAIL_HOST"; then
    SERVE_MAIL_NAME=yes
  else
    warn "$MAIL_HOST does not point here yet: the certificate will not cover it, and mail programs will complain until it does. Add the A record and run the installer again"
  fi
  if have ss; then
    foreign=$(ss -Htlnp '( sport = :25 or sport = :465 or sport = :587 or sport = :993 )' 2>/dev/null | grep -v docker-proxy || true)
    [[ -z $foreign ]] || die "the mail ports (25, 465, 587, 993) are taken by another program — remove it or pass --no-mail:"$'\n'"$foreign"
  fi
  # Many providers close outgoing port 25 until asked: without it no letter leaves the server.
  if timeout 6 bash -c 'exec 3<>/dev/tcp/aspmx.l.google.com/25' 2>/dev/null; then
    ok "outgoing port 25 is open: mail can leave the server"
  else
    warn "outgoing port 25 seems CLOSED: mail to other servers will not leave. Ask the provider to open it (the site itself keeps working, letters wait in the queue)"
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
[[ -n $MAXMIND_KEY_FILE || -n $(env_get MAXMIND_LICENSE_KEY) ]] && packages+=(geoipupdate)
# MySQL, Redis and the mail server run in Docker. The distribution's own packages are used;
# an already working Docker (any origin) is left alone.
if ! have docker || ! docker compose version >/dev/null 2>&1; then
  packages+=(docker.io docker-compose-v2)
fi
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
install -d -m 0750 -o "$KROKOSHA_USER" -g "$KROKOSHA_USER" "$KROKOSHA_STATE"
install -d -m 0755 -o "$KROKOSHA_USER" -g "$KROKOSHA_USER" "$WWW"
# Inside the site user's directories that user makes the directories itself. The user may put a
# link in the place of any name there, and root would follow it: `install -d -o` would hand the
# owner and the mode of /etc to the site user. cache/npm and cache/github are what the sandbox of
# the build may write to (krokosha-sync.service), so they must be there before it runs.
as_site_user mkdir -p -m 0750 "$KROKOSHA_STATE/cache" "$KROKOSHA_STATE/cache/npm" "$KROKOSHA_STATE/cache/github"
as_site_user mkdir -p -m 0755 "$WWW/releases"
# The map of the internet (docs/netmap.md): its sources, and the overview nginx serves.
as_site_user mkdir -p -m 0750 "$KROKOSHA_STATE/netmap"
as_site_user mkdir -p -m 0755 "$WWW/netmap"
# certbot's webroot is root's: made once, never chowned again for the reason above.
[[ ! -L $WWW/acme ]] || die "$WWW/acme is a symbolic link: something changed the files of the site user"
[[ -d $WWW/acme ]] || install -d -m 0755 -o root -g root "$WWW/acme"
# The data root: everything that cannot be rebuilt (docs/architecture.md §6).
install -d -m 0755 -o root -g root "$DATA_DIR"
install -d -m 0750 -o root -g "$KROKOSHA_USER" "$DATA_DIR/config"
install -d -m 0700 -o "$KROKOSHA_USER" -g "$KROKOSHA_USER" "$DATA_DIR/attachments"
install -d -m 0700 -o root -g root "$DATA_DIR/backups"
# Owned by the containers' own users; created without touching existing permissions.
mkdir -p "$DATA_DIR/mysql" "$DATA_DIR/redis"
# Settings and secrets belong to the data too: /etc/krokosha is a link into the data root.
if [[ -d $KROKOSHA_ETC && ! -L $KROKOSHA_ETC ]]; then
  cp -a "$KROKOSHA_ETC/." "$DATA_DIR/config/"
  rm -rf "$KROKOSHA_ETC"
fi
ln -sfn "$DATA_DIR/config" "$KROKOSHA_ETC"
install -d -m 0755 -o root -g adm /var/log/krokosha
ok "$KROKOSHA_ROOT, $KROKOSHA_STATE, $WWW"
ok "data root: $DATA_DIR ($KROKOSHA_ETC → $DATA_DIR/config)"

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

# The API reads these settings when it starts: if they change, it has to be restarted.
env_before=$(sha256sum "$KROKOSHA_ENV" 2>/dev/null || true)
env_set DOMAIN "$DOMAIN"
env_set OLD_DOMAIN "$OLD_DOMAIN"
env_set ADMIN_EMAIL "$ADMIN_EMAIL"
env_set SITE_URL "$SITE_URL"
env_set TLS_MODE "$TLS_MODE"
env_set REPO_URL "$REPO_URL"
env_set REPO_BRANCH "$REPO_BRANCH"
env_set FIREWALL_ALLOW "${EXTRA_PORTS[*]:-}"
env_set SKIP_FIREWALL "$SKIP_FIREWALL"
env_set SKIP_DNS_CHECK "$SKIP_DNS"
env_set KROKOSHA_DATA "$DATA_DIR"
env_set KROKOSHA_CONTENT_DIR "$KROKOSHA_REPO/content"

# Generated once and kept: changing them later would lock the services out of their own data.
env_default() { [[ -n $(env_get "$1") ]] || env_set "$1" "$2"; }
env_default MYSQL_ADDR 127.0.0.1:3306
env_default MYSQL_DATABASE krokosha
env_default MYSQL_USER krokosha
env_default MYSQL_PASSWORD "$(openssl rand -hex 24)"
env_default REDIS_PASSWORD "$(openssl rand -hex 24)"
# Signs what must not be forged: proof-of-work challenges of the contact form, reply addresses.
env_default APP_SECRET "$(openssl rand -hex 32)"
env_set REDIS_URL "redis://:$(env_get REDIS_PASSWORD)@127.0.0.1:6379/0"
# The admin area hides behind a secret path (brief B6); it is shown at the end of the installation.
env_set ADMIN_PATH "$ADMIN_PATH"
# The database root password is needed by the container only — the API never sees it.
MYSQL_ROOT_ENV="$KROKOSHA_ETC/mysql-root.env"
if [[ ! -s $MYSQL_ROOT_ENV ]]; then
  (umask 077 && printf 'MYSQL_ROOT_PASSWORD=%s\n' "$(openssl rand -hex 24)" >"$MYSQL_ROOT_ENV")
fi
chmod 0600 "$MYSQL_ROOT_ENV"
chown root:root "$MYSQL_ROOT_ENV"
# The Telegram bot (brief B10.3). The token never appears on a command line or on the screen:
# it comes from a file or is typed without echo, and lives in this file only.
telegram_token=''
if [[ -n $TELEGRAM_TOKEN_FILE ]]; then
  telegram_token=$(head -n 1 "$TELEGRAM_TOKEN_FILE" | tr -d '[:space:]')
elif [[ -z $(env_get TELEGRAM_BOT_TOKEN) && $ASSUME_YES == no && -t 0 ]]; then
  log "Telegram bot: create one with @BotFather and paste its token here (it is not shown). Empty — set the bot up later."
  read -r -s -p "Token: " telegram_token
  echo >&2
fi
if [[ -n $telegram_token ]]; then
  [[ $telegram_token =~ ^[0-9]{5,}:[A-Za-z0-9_-]{30,}$ ]] || die "that does not look like a token from @BotFather (123456789:AA…)"
  env_set TELEGRAM_BOT_TOKEN "$telegram_token"
fi
[[ -z $TELEGRAM_API ]] || env_set TELEGRAM_API_URL "$TELEGRAM_API"
# IndexNow (brief B7): the key is no secret — it is served by the site — but it must stay the
# same, or the engines would have to learn a new one.
env_set INDEXNOW "$INDEXNOW"
if [[ $INDEXNOW == yes ]]; then
  env_default INDEXNOW_KEY "$(openssl rand -hex 16)"
else
  env_set INDEXNOW_KEY ""
fi
[[ -z $INDEXNOW_API ]] || env_set INDEXNOW_API "$INDEXNOW_API"
# GeoLite2 (brief B5): the account and the key live here; /etc/GeoIP.conf is written from them.
[[ -z $MAXMIND_ACCOUNT ]] || env_set MAXMIND_ACCOUNT_ID "$MAXMIND_ACCOUNT"
if [[ -n $MAXMIND_KEY_FILE ]]; then
  maxmind_key=$(head -n 1 "$MAXMIND_KEY_FILE" | tr -d '[:space:]')
  [[ $maxmind_key =~ ^[A-Za-z0-9_]{16,}$ ]] || die "that does not look like a MaxMind license key (letters, digits and _)"
  env_set MAXMIND_LICENSE_KEY "$maxmind_key"
fi
# Without a key: DB-IP City Lite, fetched from here by deploy/bin/krokosha-dbip-update.
[[ -z $DBIP_URL ]] || env_set DBIP_URL "$DBIP_URL"
[[ $NETMAP_OFFLINE == no ]] || env_set NETMAP_FETCH off
# Telegram delivers updates to HTTPS only; without it the site asks Telegram itself.
if [[ $TLS_MODE == none ]]; then
  env_set TELEGRAM_MODE polling
else
  env_default TELEGRAM_MODE webhook
fi
# Mail (brief B8, B10.5). The site sends through its own mail server as the service mailbox
# leads@ — whose password nobody needs to know — and reads clients' answers from it.
env_set MAIL "$MAIL"
if [[ $MAIL == yes ]]; then
  env_set MAILBOX "$MAILBOX"
  env_default MAIL_SERVICE_PASSWORD "$(openssl rand -hex 24)"
  env_set MAIL_HOST "$MAIL_HOST"
  env_set MAIL_POSTMASTER "postmaster@$DOMAIN"
  env_set SMTP_ADDR 127.0.0.1:587
  env_set SMTP_USER "leads@$DOMAIN"
  env_set SMTP_PASSWORD "$(env_get MAIL_SERVICE_PASSWORD)"
  env_set IMAP_ADDR 127.0.0.1:993
  env_set IMAP_USER "leads@$DOMAIN"
  env_set IMAP_PASSWORD "$(env_get MAIL_SERVICE_PASSWORD)"
  env_set MAIL_INBOX "leads@$DOMAIN"
  # Letters are written in the owner's name when there is an owner's mailbox; notifications go there.
  mail_from=${MAILBOX:-leads@$DOMAIN}
  [[ -z $MAIL_NAME ]] || env_set MAIL_NAME "$MAIL_NAME"
  if [[ -n $(env_get MAIL_NAME) ]]; then
    env_set MAIL_FROM "$(env_get MAIL_NAME) <$mail_from>"
  else
    env_set MAIL_FROM "$mail_from"
  fi
  env_set MAIL_NOTIFY_TO "${MAILBOX:-$ADMIN_EMAIL}"
else
  for key in SMTP_ADDR SMTP_USER SMTP_PASSWORD IMAP_ADDR IMAP_USER IMAP_PASSWORD MAIL_INBOX; do
    [[ -z $(env_get "$key") ]] || env_set "$key" ""
  done
fi
if [[ -n ${GITHUB_TOKEN:-} ]]; then
  env_set GITHUB_TOKEN "$GITHUB_TOKEN"
elif [[ -z $(env_get GITHUB_TOKEN) ]]; then
  env_set GITHUB_TOKEN ""
  warn "no GITHUB_TOKEN: the GitHub API allows 60 requests per hour, enough thanks to caching. To raise it, put a read-only token into $KROKOSHA_ENV"
fi
ENV_CHANGED=no
[[ $(sha256sum "$KROKOSHA_ENV") == "$env_before" ]] || ENV_CHANGED=yes
ok "saved (mode 600, readable by root only)"

# ---------------------------------------------------------------------------------------------
step "Database and cache (MySQL and Redis in Docker)"
# ---------------------------------------------------------------------------------------------

systemctl enable --quiet --now docker
compose() {
  docker compose --env-file "$KROKOSHA_ENV" --env-file "$MYSQL_ROOT_ENV" \
    --file "$DEPLOY/compose/compose.yaml" "$@"
}
compose pull --quiet
# --wait returns when both containers report healthy.
compose up --detach --wait --remove-orphans || {
  compose ps >&2 || true
  compose logs --tail 40 >&2 || true
  die "MySQL or Redis did not become healthy (log above)"
}
ok "MySQL and Redis are healthy, data in $DATA_DIR/mysql and $DATA_DIR/redis, reachable from this host only"

# ---------------------------------------------------------------------------------------------
step "Building the programs and the site"
# ---------------------------------------------------------------------------------------------

as_site_user mkdir -p "$KROKOSHA_STATE/cache/bin"
as_site_user env GOCACHE="$KROKOSHA_STATE/cache/go-build" GOPATH="$KROKOSHA_STATE/cache/go" GOFLAGS=-mod=readonly GOTOOLCHAIN=local CGO_ENABLED=0 \
  go -C "$KROKOSHA_REPO/api" build -trimpath -ldflags '-s -w' -o "$KROKOSHA_STATE/cache/bin/" ./cmd/krokosha-cli ./cmd/krokosha-api
# The binaries are read as the site user, into files of root's: whatever stands under their names
# in the site user's cache, root copies nothing it could read and that user could not.
built=$(mktemp -d)
as_site_user cat "$KROKOSHA_STATE/cache/bin/krokosha-cli" >"$built/krokosha-cli"
as_site_user cat "$KROKOSHA_STATE/cache/bin/krokosha-api" >"$built/krokosha-api"
install_if_changed "$built/krokosha-cli" "$KROKOSHA_ROOT/bin/krokosha-cli" 0755 || true
# `sudo krokosha-cli admin passwd LOGIN` should simply work.
ln -sfn "$KROKOSHA_ROOT/bin/krokosha-cli" /usr/local/bin/krokosha-cli
api_changed=$ENV_CHANGED
install_if_changed "$built/krokosha-api" "$KROKOSHA_ROOT/bin/krokosha-api" 0755 && api_changed=yes
rm -rf "$built"
ok "$KROKOSHA_ROOT/bin/krokosha-cli, krokosha-api"

for unit in krokosha-sync.service krokosha-sync.timer krokosha-rebuild.path \
  krokosha-backup.service krokosha-backup.timer krokosha-backup-now.path krokosha-certwatch.service krokosha-certwatch.timer \
  krokosha-fail2ban.service krokosha-fail2ban.timer \
  krokosha-geoipupdate.service krokosha-geoipupdate.timer krokosha-dbip.service krokosha-dbip.timer \
  krokosha-netmap.service krokosha-netmap.timer; do
  install_if_changed "$DEPLOY/systemd/$unit" "/etc/systemd/system/$unit" || true
done
# Where the API leaves requests for a rebuild and the build leaves its report (both run as the
# site user; the API may write to requests/ only).
as_site_user mkdir -p -m 0750 "$KROKOSHA_STATE/requests" "$KROKOSHA_STATE/status"
install_if_changed "$DEPLOY/systemd/krokosha-api.service" /etc/systemd/system/krokosha-api.service && api_changed=yes
# The unit is static; the one path that depends on --data-dir goes into a drop-in.
api_dropin=$(mktemp)
printf '[Service]\nReadWritePaths=-%s/attachments\n' "$DATA_DIR" >"$api_dropin"
install_if_changed "$api_dropin" /etc/systemd/system/krokosha-api.service.d/data-dir.conf && api_changed=yes
# The build does not see the files of requests at all.
printf '[Service]\nInaccessiblePaths=-%s/attachments\n' "$DATA_DIR" >"$api_dropin"
install_if_changed "$api_dropin" /etc/systemd/system/krokosha-sync.service.d/data-dir.conf || true
rm -f "$api_dropin"
systemctl daemon-reload

# The API applies database migrations when it starts.
systemctl enable --quiet krokosha-api.service
if [[ $api_changed == yes ]] || ! systemctl is-active --quiet krokosha-api.service; then
  systemctl restart krokosha-api.service
fi
api_ok=no
for _ in $(seq 1 60); do
  if curl --silent --fail --max-time 3 http://127.0.0.1:8080/api/health >/dev/null 2>&1; then
    api_ok=yes
    break
  fi
  sleep 2
done
if [[ $api_ok != yes ]]; then
  journalctl -u krokosha-api.service -n 40 --no-pager >&2 || true
  die "the API did not come up (log above)"
fi
ok "API is up: $(curl --silent --max-time 3 http://127.0.0.1:8080/api/health)"

# ---------------------------------------------------------------------------------------------
step "Administrator of the admin area"
# ---------------------------------------------------------------------------------------------

cli() { "$KROKOSHA_ROOT/bin/krokosha-cli" "$@"; }
cli_errors=$(mktemp)
admins=$(cli admin list 2>"$cli_errors") || {
  cat "$cli_errors" >&2
  die "cannot read the list of administrators (message above)"
}
rm -f "$cli_errors"
if [[ -n $admins ]]; then
  ok "accounts: $(awk '{print $1}' <<<"$admins" | paste -sd ' ')"
  [[ -z $ADMIN_LOGIN ]] || grep -qiE "^$ADMIN_LOGIN " <<<"$admins" ||
    warn "--admin-login $ADMIN_LOGIN ignored: the first administrator exists already. More accounts: sudo krokosha-cli admin create LOGIN"
elif [[ -n $ADMIN_PASSWORD_FILE ]]; then
  head -n 1 "$ADMIN_PASSWORD_FILE" | cli admin create "$ADMIN_LOGIN" --password-stdin >/dev/null ||
    die "the administrator was not created (message above)"
  ok "administrator $ADMIN_LOGIN created, password taken from $ADMIN_PASSWORD_FILE — delete that file now"
elif [[ $ASSUME_YES == no && -t 0 ]]; then
  [[ -n $ADMIN_LOGIN ]] || ask ADMIN_LOGIN "Login for the admin area (3 to 64 latin letters, digits, dots, - or _)"
  log "Password for $ADMIN_LOGIN: at least 12 characters, it is not shown while you type."
  created=no
  for _ in 1 2 3; do
    if cli admin create "$ADMIN_LOGIN" >/dev/null; then
      created=yes
      break
    fi
    warn "let's try again"
  done
  if [[ $created == yes ]]; then
    ok "administrator $ADMIN_LOGIN created. Recommended next: two-factor authentication (admin area → Account)"
  else
    warn "no administrator yet. Create one later: sudo krokosha-cli admin create LOGIN"
  fi
else
  warn "no administrator yet, and nobody to ask for a password. Create one: sudo krokosha-cli admin create LOGIN"
fi

# ---------------------------------------------------------------------------------------------
step "Telegram bot"
# ---------------------------------------------------------------------------------------------

bot_summary="not set up — requests wait for it in the queue. Later: sudo $0 --from-env --telegram-token-file FILE"
if [[ -n $(env_get TELEGRAM_BOT_TOKEN) ]]; then
  cli_errors=$(mktemp)
  bot_name=$(cli bot check 2>"$cli_errors") || {
    cat "$cli_errors" >&2
    die "Telegram did not accept the token (message above). Put the right one into a file and run: sudo $0 --from-env --telegram-token-file FILE"
  }
  rm -f "$cli_errors"
  ok "the token belongs to $bot_name"
  bot_summary="$bot_name   (who has access: sudo krokosha-cli bot users)"
  if ! cli bot users | awk -F'\t' '$2 == "owner" && $3 == "active"' | grep -q .; then
    if [[ -n $telegram_token ]]; then
      # The first owner gets in by a one-time invitation, like everybody after them.
      bot_invite=$(cli bot invite --owner 2>/dev/null) || die "cannot make the owner's invitation"
      bot_summary="$bot_name — become its owner within 24 hours: https://t.me/${bot_name#@}?start=${bot_invite#/start }"
      ok "an invitation for the bot's owner is at the end of this output"
    else
      warn "the bot has no owner yet. Make an invitation: sudo krokosha-cli bot invite --owner"
    fi
  fi
else
  warn "no Telegram bot yet: $bot_summary"
fi

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

install_if_changed "$DEPLOY/nginx/snippets/krokosha-headers.conf" /etc/nginx/snippets/krokosha-headers.conf || true
install_if_changed "$DEPLOY/nginx/snippets/krokosha-proxy.conf" /etc/nginx/snippets/krokosha-proxy.conf || true
install_if_changed "$DEPLOY/nginx/snippets/krokosha-admin.conf" /etc/nginx/snippets/krokosha-admin.conf || true
# The log of the admin area exists from the start: fail2ban refuses to start a jail without its file.
[[ -f /var/log/krokosha/nginx-admin.json.log ]] || install -m 0640 -o www-data -g adm /dev/null /var/log/krokosha/nginx-admin.json.log

write_headers() { # with_hsts: HSTS only with a real certificate, otherwise an empty snippet
  if [[ $1 == yes ]]; then
    cat "$DEPLOY/nginx/snippets/krokosha-hsts.conf" >"$tmp"
  else
    printf '# HSTS is off: the certificate is not a publicly trusted one.\n' >"$tmp"
  fi
  install_if_changed "$tmp" /etc/nginx/snippets/krokosha-hsts.conf || true
}

# SERVER_NAMES, ADMIN_PATH, TLS_CERT and TLS_KEY are read by render() through the @@NAME@@ placeholders.
# shellcheck disable=SC2034
SERVER_NAMES=$DOMAIN
[[ $SERVE_WWW == yes ]] && SERVER_NAMES="$DOMAIN www.$DOMAIN"
# The old name, and the certificate it is served with over HTTPS (none: port 80 only).
# shellcheck disable=SC2034
OLD_NAMES="$OLD_DOMAIN www.$OLD_DOMAIN mail.$OLD_DOMAIN" OLD_TLS_CERT='' OLD_TLS_KEY=''

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
    # The certificate names mail.<domain> too: its challenges must be answered on port 80.
    [[ $SERVE_MAIL_NAME == yes ]] && render "$DEPLOY/nginx/site-mail-acme.conf.tmpl"
    if [[ -n $OLD_DOMAIN ]]; then
      render "$DEPLOY/nginx/site-old.conf.tmpl"
      [[ -n $OLD_TLS_CERT ]] && render "$DEPLOY/nginx/site-old-tls.conf.tmpl"
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

# cert_covers FILE NAME — does the certificate name the host?
cert_covers() { openssl x509 -in "$1" -noout -ext subjectAltName 2>/dev/null | grep -qE "DNS:$2(,|\$)"; }

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
    # One certificate for everything, the old name included while there is one.
    tls_names="DNS:$DOMAIN,DNS:www.$DOMAIN,DNS:$MAIL_HOST"
    [[ -z $OLD_DOMAIN ]] || tls_names+=",DNS:$OLD_DOMAIN,DNS:www.$OLD_DOMAIN,DNS:mail.$OLD_DOMAIN"
    if [[ ! -f $TLS_CERT ]] || ! cert_covers "$TLS_CERT" "$MAIL_HOST" || { [[ -n $OLD_DOMAIN ]] && ! cert_covers "$TLS_CERT" "$OLD_DOMAIN"; }; then
      install -d -m 0750 -o root -g root "$KROKOSHA_ETC/tls"
      openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 30 \
        -subj "/CN=$DOMAIN" -addext "subjectAltName=$tls_names" \
        -keyout "$TLS_KEY" -out "$TLS_CERT" 2>/dev/null
      chmod 0600 "$TLS_KEY"
      NGINX_RESTART=yes
    fi
    # shellcheck disable=SC2034 # read by render()
    [[ -z $OLD_DOMAIN ]] || OLD_TLS_CERT=$TLS_CERT OLD_TLS_KEY=$TLS_KEY
    write_headers no
    write_site https
    warn "serving https://$DOMAIN with a SELF-SIGNED certificate — for tests only"
    ;;
  letsencrypt)
    TLS_CERT=/etc/letsencrypt/live/$DOMAIN/fullchain.pem TLS_KEY=/etc/letsencrypt/live/$DOMAIN/privkey.pem
    # The old name keeps the certificate it had (it renews while the name points here).
    if [[ -n $OLD_DOMAIN && -f /etc/letsencrypt/live/$OLD_DOMAIN/fullchain.pem ]]; then
      # shellcheck disable=SC2034 # read by render()
      OLD_TLS_CERT=/etc/letsencrypt/live/$OLD_DOMAIN/fullchain.pem OLD_TLS_KEY=/etc/letsencrypt/live/$OLD_DOMAIN/privkey.pem
    fi
    # One certificate for the site and the mail server. An existing one that lacks the mail
    # server's name is reissued with it («--expand»).
    if [[ ! -f $TLS_CERT ]] || { [[ $SERVE_MAIL_NAME == yes ]] && ! cert_covers "$TLS_CERT" "$MAIL_HOST"; }; then
      # The challenge is answered over plain HTTP, so the site goes up on port 80 first.
      write_headers no
      write_site http-only
      certbot_args=(certonly --webroot --webroot-path "$WWW/acme" --cert-name "$DOMAIN" -d "$DOMAIN"
        --non-interactive --agree-tos --email "$ADMIN_EMAIL" --no-eff-email --keep-until-expiring --expand)
      [[ $SERVE_WWW == yes ]] && certbot_args+=(-d "www.$DOMAIN")
      [[ $SERVE_MAIL_NAME == yes ]] && certbot_args+=(-d "$MAIL_HOST")
      [[ $STAGING == yes ]] && certbot_args+=(--staging)
      log "requesting a certificate for $SERVER_NAMES"
      certbot "${certbot_args[@]}" || die "Let's Encrypt did not issue a certificate. The site stays on http://$DOMAIN; fix the problem and run the installer again"
    fi
    # certbot.timer (from the package) renews; nginx has to pick the new files up.
    install -d -m 0755 /etc/letsencrypt/renewal-hooks/deploy
    printf '#!/bin/sh\n# Installed by krokosha-site: load the renewed certificate.\nsystemctl reload nginx\n# The mail server reads the same certificate; it notices the change by itself within a minute,\n# a restart makes it certain.\ndocker restart krokosha-mail-1 >/dev/null 2>&1 || true\n' >"$tmp"
    install_if_changed "$tmp" /etc/letsencrypt/renewal-hooks/deploy/krokosha-reload-nginx 0755 || true
    systemctl enable --quiet --now certbot.timer
    write_headers yes
    write_site https
    ok "serving https://$DOMAIN, certificate renews automatically"
    [[ -z $OLD_DOMAIN ]] || ok "$OLD_DOMAIN, the old name: every page redirects to the same one at $DOMAIN$([[ -n $OLD_TLS_CERT ]] || echo ' (over plain HTTP only: there is no certificate for it)')"
    # Certificates of old names let go would only fail to renew from now on.
    for gone in "${FORGOTTEN_DOMAINS[@]}"; do
      [[ -d /etc/letsencrypt/live/$gone ]] || continue
      if certbot delete --cert-name "$gone" --non-interactive >/dev/null 2>&1; then
        ok "the certificate of $gone, the old name let go, is deleted"
      else
        warn "the certificate of $gone could not be deleted: sudo certbot delete --cert-name $gone"
      fi
    done
    ;;
esac
rm -f "$tmp"

# ---------------------------------------------------------------------------------------------
step "Mail server"
# ---------------------------------------------------------------------------------------------

MAIL_DIR=$DATA_DIR/mail
mail_summary="not set up (--no-mail, or no TLS). Later: sudo $0 --from-env --mailbox ADDRESS"
if [[ $MAIL != yes ]]; then
  warn "skipped: letters about requests wait in the queue until there is a mail server"
  if docker ps --all --format '{{.Names}}' | grep -qx krokosha-mail-1; then
    compose --profile mail rm --stop --force mail >/dev/null
    ok "the mail server that ran here before is stopped; its data stays in $MAIL_DIR"
  fi
else
  install -d -m 0750 -o root -g root "$MAIL_DIR"
  # The mail server's own users pass through its settings (rspamd reads the DKIM keys there, and
  # makes new ones): docker-mailserver sets that bit when it starts, and taking it away while the
  # server runs would leave a new key unwritable and the keys unreadable after a reload.
  install -d -m 0751 -o root -g root "$MAIL_DIR/config"
  install -d -m 0755 "$MAIL_DIR/data" "$MAIL_DIR/state" "$MAIL_DIR/logs"

  # Mailboxes: «address|{SHA512-CRYPT}hash» per line, the format docker-mailserver reads. A
  # mailbox that exists is left alone — its password is its owner's business from then on.
  accounts=$MAIL_DIR/config/postfix-accounts.cf
  touch "$accounts"
  chmod 0600 "$accounts"
  add_mailbox() { # add_mailbox ADDRESS — the password comes on standard input
    local address=$1 hash
    hash=$(openssl passwd -6 -stdin) || die "cannot hash the password of $address"
    printf '%s|{SHA512-CRYPT}%s\n' "$address" "$hash" >>"$accounts"
  }
  aliases=$MAIL_DIR/config/postfix-virtual.cf
  touch "$aliases"

  # The site has moved to DOMAIN: its mailboxes move with it — each keeps its password and its
  # letters. The mail server stands still meanwhile, so that nothing writes into a moving mailbox.
  moved=()
  if [[ -n $PREVIOUS_DOMAIN && $PREVIOUS_DOMAIN != "$DOMAIN" ]] && grep -q "^[^|]*@${PREVIOUS_DOMAIN//./\\.}|" "$accounts"; then
    compose --profile mail stop mail >/dev/null 2>&1 || true
    previous_re=${PREVIOUS_DOMAIN//./\\.}
    mapfile -t addresses < <(cut -d'|' -f1 "$accounts")
    for address in "${addresses[@]}"; do
      [[ $address == *"@$PREVIOUS_DOMAIN" ]] || continue
      name=${address%@*}
      if grep -q "^$name@${DOMAIN//./\\.}|" "$accounts"; then
        warn "$name@$DOMAIN exists already: $address stays as it is"
        continue
      fi
      sed -i "s/^$name@$previous_re|/$name@$DOMAIN|/" "$accounts"
      if [[ -d $MAIL_DIR/data/$PREVIOUS_DOMAIN/$name ]]; then
        # Owned by the mail server's own user, like the directory of the previous domain.
        [[ -d $MAIL_DIR/data/$DOMAIN ]] || install -d -m 0755 -o "$(stat -c %u "$MAIL_DIR/data/$PREVIOUS_DOMAIN")" \
          -g "$(stat -c %g "$MAIL_DIR/data/$PREVIOUS_DOMAIN")" "$MAIL_DIR/data/$DOMAIN"
        mv "$MAIL_DIR/data/$PREVIOUS_DOMAIN/$name" "$MAIL_DIR/data/$DOMAIN/$name"
      fi
      moved+=("$name@$DOMAIN")
    done
    rmdir "$MAIL_DIR/data/$PREVIOUS_DOMAIN" 2>/dev/null || true
    # Aliases that led to the moved mailboxes lead to them at their new addresses.
    sed -i "s/ \([^ @]*\)@$previous_re\$/ \1@$DOMAIN/" "$aliases"
    [[ ! -f $MAIL_DIR/config/dovecot-quotas.cf ]] || sed -i "s/^\([^:@]*\)@$previous_re:/\1@$DOMAIN:/" "$MAIL_DIR/config/dovecot-quotas.cf"
    ok "mailboxes moved from $PREVIOUS_DOMAIN with their passwords and letters: ${moved[*]:-none}"
  fi

  grep -q "^leads@$DOMAIN|" "$accounts" || env_get MAIL_SERVICE_PASSWORD | add_mailbox "leads@$DOMAIN"
  if [[ -n $MAILBOX ]] && ! grep -q "^$MAILBOX|" "$accounts"; then
    if [[ -n $MAILBOX_PASSWORD_FILE ]]; then
      mailbox_password=$(head -n 1 "$MAILBOX_PASSWORD_FILE")
    elif [[ $ASSUME_YES == no && -t 0 ]]; then
      log "Password for the mailbox $MAILBOX: at least 12 characters, it is not shown while you type."
      read -r -s -p "Password: " mailbox_password
      echo >&2
      read -r -s -p "Once more: " mailbox_again
      echo >&2
      [[ $mailbox_password == "$mailbox_again" ]] || die "the two passwords differ; run the installer again"
    else
      mailbox_password=''
      warn "$MAILBOX was not created: nobody to ask for its password. Later: sudo $0 --from-env --mailbox $MAILBOX --mailbox-password-file FILE"
    fi
    if [[ -n $mailbox_password ]]; then
      ((${#mailbox_password} >= 12)) || die "the password of $MAILBOX must be at least 12 characters long"
      printf '%s' "$mailbox_password" | add_mailbox "$MAILBOX"
      ok "mailbox $MAILBOX created"
    fi
    unset mailbox_password mailbox_again
  fi
  # postmaster@ and abuse@ are expected to exist (RFC 2142); they land in the owner's mailbox.
  alias_target=$MAILBOX
  grep -q "^$MAILBOX|" "$accounts" 2>/dev/null || alias_target="leads@$DOMAIN"
  for name in postmaster abuse; do
    grep -q "^$name@$DOMAIN " "$aliases" || printf '%s@%s %s\n' "$name" "$DOMAIN" "$alias_target" >>"$aliases"
  done
  # Mail to the old name reaches the same mailboxes, for as long as the name points here.
  for gone in "${FORGOTTEN_DOMAINS[@]}"; do
    sed -i "/^[^ ]*@${gone//./\\.} /d" "$aliases"
  done
  if [[ -n $OLD_DOMAIN ]]; then
    while IFS='|' read -r address _; do
      [[ $address == *"@$DOMAIN" ]] || continue
      grep -q "^${address%@*}@${OLD_DOMAIN//./\\.} " "$aliases" || printf '%s@%s %s\n' "${address%@*}" "$OLD_DOMAIN" "$address" >>"$aliases"
    done <"$accounts"
  fi

  # The certificate is the site's own; the container sees the directory it lives in.
  case $TLS_MODE in
    letsencrypt) env_set MAIL_TLS_DIR /etc/letsencrypt
      env_set MAIL_TLS_CERT "/etc/krokosha-tls/live/$DOMAIN/fullchain.pem"
      env_set MAIL_TLS_KEY "/etc/krokosha-tls/live/$DOMAIN/privkey.pem" ;;
    selfsigned) env_set MAIL_TLS_DIR "$DATA_DIR/config/tls"
      env_set MAIL_TLS_CERT /etc/krokosha-tls/selfsigned.crt
      env_set MAIL_TLS_KEY /etc/krokosha-tls/selfsigned.key ;;
  esac

  compose --profile mail pull --quiet mail
  compose --profile mail up --detach --wait mail || {
    docker logs --tail 60 krokosha-mail-1 >&2 || true
    die "the mail server did not start (log above)"
  }

  # DKIM: the key that signs outgoing mail. Made once; its public half goes into DNS.
  dkim_dns=$MAIL_DIR/config/rspamd/dkim/rsa-2048-mail-$DOMAIN.public.dns.txt
  if [[ ! -s $dkim_dns ]]; then
    docker exec krokosha-mail-1 setup config dkim keysize 2048 selector mail domain "$DOMAIN" >/dev/null ||
      die "cannot make the DKIM key (docker exec krokosha-mail-1 setup config dkim …)"
  fi
  [[ -s $dkim_dns ]] || die "the DKIM key was made, but $dkim_dns is missing"
  # Which domains are signed. docker-mailserver writes this file for the first domain only and never
  # adds another, so it is written here — in its words: the site's domain, and the old name while
  # there is one (its key stays where it was).
  signing=$MAIL_DIR/config/rspamd/override.d/dkim_signing.conf
  tmp=$(mktemp)
  {
    cat <<'CONF'
# documentation: https://rspamd.com/doc/modules/dkim_signing.html

enabled = true;

sign_authenticated = true;
sign_local = false;
try_fallback = false;

use_domain = "header";
use_redis = false; # don't change unless Redis also provides the DKIM keys
use_esld = true;
allow_username_mismatch = true;

check_pubkey = true; # you want to use this in the beginning

domain {
CONF
    for signed in "$DOMAIN" "$OLD_DOMAIN"; do
      [[ -n $signed && -s $MAIL_DIR/config/rspamd/dkim/rsa-2048-mail-$signed.private.txt ]] || continue
      printf '    %s {\n        path = "/tmp/docker-mailserver/rspamd/dkim/rsa-2048-mail-%s.private.txt";\n        selector = "mail";\n    }\n' "$signed" "$signed"
    done
    printf '}\n\n'
  } >"$tmp"
  if install_if_changed "$tmp" "$signing" 0644; then
    # rspamd reads it when the mail server starts.
    docker restart krokosha-mail-1 >/dev/null
    for _ in $(seq 1 90); do
      [[ $(docker inspect --format '{{.State.Health.Status}}' krokosha-mail-1 2>/dev/null) == healthy ]] && break
      sleep 2
    done
    [[ $(docker inspect --format '{{.State.Health.Status}}' krokosha-mail-1 2>/dev/null) == healthy ]] ||
      die "the mail server did not come back after its DKIM settings changed: docker logs krokosha-mail-1"
    ok "outgoing mail is signed with DKIM for $DOMAIN${OLD_DOMAIN:+ and $OLD_DOMAIN}"
  fi
  rm -f "$tmp"

  # What has to be entered at the DNS provider — kept in a file, shown at the end.
  # (getent fails for a name that does not resolve — behind NAT, in tests: that is an answer, not an error)
  server_ip=$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk 'NR == 1 {print $1}' || true)
  [[ -n $server_ip ]] || server_ip=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") print $(i + 1)}' || true)
  [[ -n $server_ip ]] || server_ip='<the address of this server>'
  tmp=$(mktemp)
  cat >"$tmp" <<RECORDS
DNS records for mail at $DOMAIN (enter them at the DNS provider; «@» is the domain itself)

  A      mail                $server_ip
  MX     @                   10 $MAIL_HOST.
  TXT    @                   "v=spf1 mx -all"
  TXT    mail._domainkey     $(tr -d '\n' <"$dkim_dns")
  TXT    _dmarc              "v=DMARC1; p=quarantine; rua=mailto:postmaster@$DOMAIN"
  SRV    _imaps._tcp         0 1 993 $MAIL_HOST.
  SRV    _submissions._tcp   0 1 465 $MAIL_HOST.
  SRV    _submission._tcp    0 1 587 $MAIL_HOST.

  PTR    $server_ip  →  $MAIL_HOST
         Not a DNS record of yours: it is set in the control panel of the server's provider
         («reverse DNS»). Without it big mail services put letters into spam.

Mail programs: IMAP $MAIL_HOST:993 (SSL/TLS), SMTP $MAIL_HOST:465 (SSL/TLS) or 587 (STARTTLS),
the user name is the whole address. Thunderbird finds these by itself.
RECORDS
  install_if_changed "$tmp" "$MAIL_DIR/DNS.txt" 0644 || true
  rm -f "$tmp"

  # Thunderbird and others ask the site how to set a mailbox up.
  tmp=$(mktemp)
  sed -e "s/@@DOMAIN@@/$DOMAIN/g" -e "s/@@MAIL_HOST@@/$MAIL_HOST/g" "$DEPLOY/mail/autoconfig.xml.tmpl" >"$tmp"
  write_as_site_user "$WWW/mail-autoconfig.xml" <"$tmp"
  rm -f "$tmp"

  install_if_changed "$DEPLOY/bin/krokosha-mailbox" /usr/local/bin/krokosha-mailbox 0755 || true
  # The «Почта» screen of the admin area: the API drops requests into requests/mail, the root
  # helper applies them and lists the mailboxes in mail/. Both belong to the site user: the helper
  # reads and writes there as that user (bin/krokosha-mailbox). mail/ was root's until 2026-09;
  # chown -h changes a link itself, never what it points at.
  as_site_user mkdir -p -m 0750 "$KROKOSHA_STATE/requests/mail" "$KROKOSHA_STATE/mail"
  [[ $(stat -c %U "$KROKOSHA_STATE/mail") == "$KROKOSHA_USER" ]] || chown -h "$KROKOSHA_USER:$KROKOSHA_USER" "$KROKOSHA_STATE/mail"
  for unit in krokosha-mailbox.service krokosha-mailbox.path; do
    install_if_changed "$DEPLOY/systemd/$unit" "/etc/systemd/system/$unit" || true
  done
  systemctl daemon-reload
  /usr/local/bin/krokosha-mailbox publish
  systemctl enable --quiet --now krokosha-mailbox.path
  # The API started with the new addresses before its mailbox moved there, and signs in to it once
  # only (a wrong password is not retried: the mail server's fail2ban would ban the host). Again now.
  if [[ ${#moved[@]} -gt 0 ]]; then
    systemctl try-restart krokosha-api.service
    for _ in $(seq 1 30); do
      curl --silent --fail --max-time 3 http://127.0.0.1:8080/api/health >/dev/null 2>&1 && break
      sleep 1
    done
  fi
  mail_summary="$MAIL_HOST — mailboxes: $(cut -d'|' -f1 "$accounts" | paste -sd ' ')   (sudo krokosha-mailbox list | add | passwd | del)"
  ok "the mail server runs; mailboxes: $(cut -d'|' -f1 "$accounts" | paste -sd ' ')"
fi
# ---------------------------------------------------------------------------------------------
step "Timer: GitHub sync and rebuild every 6 hours"
# ---------------------------------------------------------------------------------------------

systemctl enable --quiet --now krokosha-sync.timer
# The «rebuild now» button of the admin area.
systemctl enable --quiet --now krokosha-rebuild.path
ok "next run: $(systemctl show krokosha-sync.timer --property=NextElapseUSecRealtime --value)"

# ---------------------------------------------------------------------------------------------
step "Backups and the certificate watch"
# ---------------------------------------------------------------------------------------------

# Every night: the database, the mail, the files of requests, the settings (deploy/backup.sh).
# Every day: a look at the certificates that are really served; what is about to expire is renewed
# at once, and the owner is told when that fails (deploy/bin/krokosha-certwatch).
systemctl enable --quiet --now krokosha-backup.timer krokosha-certwatch.timer
# The «backup now» button of the admin area.
systemctl enable --quiet --now krokosha-backup-now.path
[[ -n $(env_get BACKUP_RSYNC_TO) ]] ||
  warn "backups stay on this disk ($DATA_DIR/backups): to copy every one elsewhere, set BACKUP_RSYNC_TO=user@host:/path in $KROKOSHA_ENV"
ok "backup: $(systemctl show krokosha-backup.timer --property=NextElapseUSecRealtime --value); certificates: $(systemctl show krokosha-certwatch.timer --property=NextElapseUSecRealtime --value)"

# ---------------------------------------------------------------------------------------------
step "GeoIP: countries and cities of visitors"
# ---------------------------------------------------------------------------------------------

# The API reads one MaxMind DB file (GEOIP_DB) and notices a new version of it by itself. With a
# MaxMind key that is GeoLite2, which geoipupdate brings twice a week (the key is in
# /etc/GeoIP.conf, readable by root only). Without a key — or until the key has brought its first
# file — it is the free DB-IP City Lite, which krokosha-dbip.timer brings once a month; its licence
# (CC BY 4.0) wants a link to DB-IP where the results are shown, and the admin area has one.
# GEOIP_DB=off, or a file of the owner's own, is left as it is.
GEO_LITE=/var/lib/GeoIP/GeoLite2-City.mmdb
GEO_DBIP=/var/lib/GeoIP/dbip-city-lite.mmdb
geo_db=$(env_get GEOIP_DB)
if [[ ${geo_db,,} == off || (-n $geo_db && $geo_db != "$GEO_LITE" && $geo_db != "$GEO_DBIP") ]]; then
  systemctl disable --quiet --now krokosha-geoipupdate.timer krokosha-dbip.timer 2>/dev/null || true
  rm -f "$GEO_DBIP" "$GEO_DBIP.month"
  if [[ ${geo_db,,} == off ]]; then
    geo_summary="off (GEOIP_DB=off in $KROKOSHA_ENV)"
  else
    geo_summary="$geo_db (GEOIP_DB in $KROKOSHA_ENV; keeping that file fresh is up to you)"
  fi
  ok "$geo_summary"
else
  install -d -m 0755 /var/lib/GeoIP
  geo_use='' geo_fetched=no
  if [[ -n $(env_get MAXMIND_LICENSE_KEY) ]]; then
    geoip_conf=$(mktemp)
    cat >"$geoip_conf" <<EOF
# Written by krokosha-site/deploy/install.sh from $KROKOSHA_ENV; edit the settings there.
AccountID $(env_get MAXMIND_ACCOUNT_ID)
LicenseKey $(env_get MAXMIND_LICENSE_KEY)
EditionIDs GeoLite2-City
DatabaseDirectory /var/lib/GeoIP
EOF
    install_if_changed "$geoip_conf" /etc/GeoIP.conf 0600 || true
    rm -f "$geoip_conf"
    systemctl enable --quiet --now krokosha-geoipupdate.timer
    if [[ ! -f $GEO_LITE ]] && systemctl start krokosha-geoipupdate.service 2>/dev/null && [[ -f $GEO_LITE ]]; then
      geo_fetched=yes
    fi
    if [[ -f $GEO_LITE ]]; then
      geo_use=$GEO_LITE
      geo_summary="MaxMind GeoLite2-City of $(date -r "$GEO_LITE" +%F), refreshed twice a week (krokosha-geoipupdate.timer)"
      ok "$geo_summary"
    else
      warn "the MaxMind key is saved, but GeoLite2 could not be fetched yet — see journalctl -u krokosha-geoipupdate; the timer tries again. Until then: DB-IP City Lite"
    fi
  else
    systemctl disable --quiet --now krokosha-geoipupdate.timer 2>/dev/null || true
  fi
  if [[ -z $geo_use ]]; then
    geo_use=$GEO_DBIP
    systemctl enable --quiet --now krokosha-dbip.timer
    # It downloads only when this month's file is not in place yet.
    geo_before=$(stat -c %Y "$GEO_DBIP" 2>/dev/null || true)
    systemctl start krokosha-dbip.service 2>/dev/null || true
    [[ $(stat -c %Y "$GEO_DBIP" 2>/dev/null || true) == "$geo_before" ]] || geo_fetched=yes
    if [[ -s $GEO_DBIP ]]; then
      geo_summary="DB-IP City Lite of $(cat "$GEO_DBIP.month" 2>/dev/null || date -r "$GEO_DBIP" +%Y-%m), free (CC BY 4.0), refreshed every month (krokosha-dbip.timer)"
      if [[ -n $(env_get MAXMIND_LICENSE_KEY) ]]; then
        geo_summary+=", until the MaxMind key brings GeoLite2"
      else
        geo_summary+=". MaxMind GeoLite2 instead: sudo $0 --from-env --maxmind-account ID --maxmind-key-file FILE"
      fi
      ok "$geo_summary"
    else
      geo_summary="DB-IP City Lite could not be fetched yet — see journalctl -u krokosha-dbip; the timer tries again tomorrow, or now: sudo systemctl start krokosha-dbip.service"
      warn "$geo_summary"
    fi
  else
    # GeoLite2 is there: the other database is not needed any more.
    systemctl disable --quiet --now krokosha-dbip.timer 2>/dev/null || true
    rm -f "$GEO_DBIP" "$GEO_DBIP.month"
  fi
  geo_restart=$geo_fetched
  if [[ $(env_get GEOIP_DB) != "$geo_use" ]]; then
    env_set GEOIP_DB "$geo_use"
    geo_restart=yes
  fi
  if [[ $geo_restart == yes ]]; then
    # The API reads GEOIP_DB when it starts, and looks for a new file only every ten minutes.
    systemctl try-restart krokosha-api.service
    for _ in $(seq 1 30); do
      curl --silent --fail --max-time 3 http://127.0.0.1:8080/api/health >/dev/null 2>&1 && break
      sleep 1
    done
  fi
fi

# ---------------------------------------------------------------------------------------------
step "The map of the internet (/map)"
# ---------------------------------------------------------------------------------------------

# Rebuilt every night from open data by krokosha-netmap.timer (docs/netmap.md): the sources in
# $KROKOSHA_STATE/netmap, what changed into MySQL, the overview in $WWW/netmap for nginx. The API
# picks up every new map by itself; until the first one is there, /map says its data is not ready.
systemctl enable --quiet --now krokosha-netmap.timer
if [[ -s $WWW/netmap/overview.json ]]; then
  netmap_summary="rebuilt every night (krokosha-netmap.timer); the map of $(date -r "$WWW/netmap/overview.json" +%F) is served"
elif [[ $(env_get NETMAP_FETCH) == off ]]; then
  netmap_summary="built from the files in $KROKOSHA_STATE/netmap only (NETMAP_FETCH=off): sudo systemctl start krokosha-netmap.service"
elif systemctl is-active --quiet krokosha-netmap.service; then
  netmap_summary="the first map is being built: journalctl -u krokosha-netmap -f"
else
  # The first run downloads some 130 MB and writes the whole map: minutes, in the background.
  systemctl start --no-block krokosha-netmap.service
  netmap_summary="the first map is being built in the background (a few minutes): journalctl -u krokosha-netmap -f"
fi
ok "$netmap_summary"

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
  if [[ $MAIL == yes ]]; then
    ufw allow 25/tcp comment 'krokosha: mail from other servers' >/dev/null
    ufw allow 465/tcp comment 'krokosha: mail programs, sending' >/dev/null
    ufw allow 587/tcp comment 'krokosha: mail programs, sending' >/dev/null
    ufw allow 993/tcp comment 'krokosha: mail programs, reading' >/dev/null
  fi
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

fail2ban_changed=no
install_if_changed "$DEPLOY/fail2ban/filter-krokosha-admin.conf" /etc/fail2ban/filter.d/krokosha-admin.conf && fail2ban_changed=yes
install_if_changed "$DEPLOY/fail2ban/krokosha.conf" /etc/fail2ban/jail.d/krokosha.conf && fail2ban_changed=yes
if [[ $fail2ban_changed == yes ]]; then
  systemctl enable --quiet fail2ban || true
  systemctl restart fail2ban || true
fi
if systemctl is-active --quiet fail2ban; then
  ok "jails are on: sshd, krokosha-admin (failed logins to the admin area)"
else
  warn "fail2ban is not running — check: journalctl -u fail2ban. The site itself is not affected"
fi
# The «system status» screen shows how many addresses the jails keep out: only root may ask.
systemctl enable --quiet --now krokosha-fail2ban.timer
systemctl start krokosha-fail2ban.service || warn "cannot count the bans: journalctl -u krokosha-fail2ban"

# ---------------------------------------------------------------------------------------------
step "Done"
# ---------------------------------------------------------------------------------------------

if [[ $TLS_MODE == none ]]; then
  admin_url="not served: the admin area needs HTTPS (--tls letsencrypt or selfsigned)"
else
  admin_url="$SITE_URL$ADMIN_PATH/   (keep this address to yourself)"
fi

old_summary="none"
[[ -z $OLD_DOMAIN ]] || old_summary="$OLD_DOMAIN — its pages redirect here and its addresses get mail, for as long as it points to this server"

cat >&2 <<EOF

  Site:       $SITE_URL
  Old name:   $old_summary
  Admin area: $admin_url
  Accounts:   sudo krokosha-cli admin list | create LOGIN | passwd LOGIN | totp-reset LOGIN
  Bot:        $bot_summary
  Mail:       $mail_summary
  Release:    $(readlink -f "$WWW/current")
  Rebuild:    sudo systemctl start krokosha-sync.service     (runs by itself every 6 hours)
  Update:     sudo $KROKOSHA_REPO/deploy/update.sh
  Roll back:  sudo $KROKOSHA_REPO/deploy/rollback.sh
  Backups:    $DATA_DIR/backups, every night   (now: sudo $KROKOSHA_REPO/deploy/backup.sh; back: sudo $KROKOSHA_REPO/deploy/restore.sh --from DIR)
  GeoIP:      $geo_summary
  Map:        $netmap_summary
  IndexNow:   $([[ $INDEXNOW == yes ]] && echo "on — the key is served as $SITE_URL/$(env_get INDEXNOW_KEY).txt; changed pages are submitted after every release" || echo "off (--no-indexnow)")
  Search:     Google Search Console and Bing Webmaster Tools are connected by hand once: deploy/README.md, «Поисковики»
  Logs:       journalctl -u krokosha-sync.service, /var/log/krokosha/
  Settings:   $KROKOSHA_ENV
  Data:       $DATA_DIR   (database, cache, settings — copy this directory to move the site)
  API:        $SITE_URL/api/health

EOF
if [[ $MAIL == yes ]]; then
  sed 's/^/  /' "$MAIL_DIR/DNS.txt" >&2
  printf '\n  (this list is kept in %s)\n\n' "$MAIL_DIR/DNS.txt" >&2
fi
}

main "$@"
