# shellcheck shell=bash
# Shared helpers of the deploy scripts. Source it; do not run it.

# Layout on the server (brief B8). Used by the scripts that source this file.
# shellcheck disable=SC2034
KROKOSHA_USER=krokosha
KROKOSHA_ROOT=/opt/krokosha              # repo/, toolchain/, bin/
KROKOSHA_REPO="$KROKOSHA_ROOT/repo"
KROKOSHA_STATE=/var/lib/krokosha         # caches, later the database
KROKOSHA_WWW=/var/www/krokosha           # releases/<timestamp>, current → release, acme/
KROKOSHA_ETC=/etc/krokosha               # env (secrets), tls/
KROKOSHA_ENV="$KROKOSHA_ETC/env"

if [[ -t 2 ]]; then
  _c_bold=$'\e[1m' _c_red=$'\e[31m' _c_yellow=$'\e[33m' _c_green=$'\e[32m' _c_off=$'\e[0m'
else
  _c_bold='' _c_red='' _c_yellow='' _c_green='' _c_off=''
fi

step() { printf '\n%s==> %s%s\n' "$_c_bold" "$*" "$_c_off" >&2; }
log()  { printf '    %s\n' "$*" >&2; }
ok()   { printf '    %s✓%s %s\n' "$_c_green" "$_c_off" "$*" >&2; }
warn() { printf '    %s! %s%s\n' "$_c_yellow" "$*" "$_c_off" >&2; }
die()  { printf '\n%sERROR: %s%s\n' "$_c_red" "$*" "$_c_off" >&2; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

require_root() {
  [[ ${EUID:-$(id -u)} -eq 0 ]] || die "run as root: sudo $0"
}

# as_site_user CMD… — run a command as the unprivileged site user with a clean environment.
# It starts in /: the caller's working directory (an admin's home, say) may be closed to that user,
# and git refuses to run from a directory it cannot read.
as_site_user() (
  cd /
  exec runuser -u "$KROKOSHA_USER" -- env -i \
    HOME="$KROKOSHA_STATE" \
    PATH="$KROKOSHA_ROOT/toolchain/node/bin:$KROKOSHA_ROOT/toolchain/go/bin:/usr/local/bin:/usr/bin:/bin" \
    LANG=C.UTF-8 \
    "$@"
)

# install_if_changed SRC DST [MODE] [OWNER:GROUP] — copies only when the content differs.
# Returns 0 when the file was written, 1 when it was already up to date.
install_if_changed() {
  local src=$1 dst=$2 mode=${3:-0644} owner=${4:-root:root}
  if [[ -f $dst ]] && cmp -s "$src" "$dst"; then
    chmod "$mode" "$dst"
    chown "$owner" "$dst"
    return 1
  fi
  install -D -m "$mode" -o "${owner%%:*}" -g "${owner##*:}" "$src" "$dst"
  return 0
}

# render TEMPLATE — prints the template with @@NAME@@ placeholders replaced by the values of the
# shell variables of the same name. Unlike envsubst it never touches nginx's own $variables.
render() {
  local template=$1 content name value
  content=$(<"$template")
  while [[ $content =~ @@([A-Z_][A-Z0-9_]*)@@ ]]; do
    name=${BASH_REMATCH[1]}
    [[ -v $name ]] || die "template $template: variable $name is not set"
    value=${!name}
    content=${content//"@@${name}@@"/"$value"}
  done
  printf '%s\n' "$content"
}

# env_get KEY — value of KEY in /etc/krokosha/env ("" if absent).
env_get() {
  [[ -f $KROKOSHA_ENV ]] || return 0
  sed -n "s/^$1=//p" "$KROKOSHA_ENV" | tail -n 1
}

# env_set KEY VALUE — adds or replaces KEY in /etc/krokosha/env, keeping everything else.
env_set() {
  local key=$1 value=$2 tmp
  install -d -m 0750 -o root -g "$KROKOSHA_USER" "$KROKOSHA_ETC"
  tmp=$(mktemp "$KROKOSHA_ETC/.env.XXXXXX")
  if [[ -f $KROKOSHA_ENV ]]; then
    grep -v "^${key}=" "$KROKOSHA_ENV" >"$tmp" || true
  fi
  printf '%s=%s\n' "$key" "$value" >>"$tmp"
  chmod 0600 "$tmp"
  chown "root:$KROKOSHA_USER" "$tmp"
  mv -f "$tmp" "$KROKOSHA_ENV"
}

# Validation of user input that ends up in config files and shell commands.
valid_domain() { [[ $1 =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$ ]]; }
valid_email()  { [[ $1 =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]]; }
valid_port()   { [[ $1 =~ ^[0-9]{1,5}(:[0-9]{1,5})?/(tcp|udp)$ ]]; }
