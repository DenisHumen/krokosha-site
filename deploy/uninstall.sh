#!/usr/bin/env bash
# Removes what install.sh created: services, nginx site, code, releases, the system user.
#
# Left alone on purpose: installed packages (nginx, certbot, ufw, fail2ban), firewall rules,
# Let's Encrypt certificates, and anything that does not belong to the site.
#
#   sudo ./uninstall.sh            asks for confirmation
#   sudo ./uninstall.sh --yes      no questions (tests)
#   sudo ./uninstall.sh --purge    also deletes settings and secrets in /etc/krokosha
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=deploy/lib/common.sh
source "$SCRIPT_DIR/lib/common.sh"

main() {
  local assume_yes=no purge=no answer
  while [[ $# -gt 0 ]]; do
    case $1 in
      -y | --yes) assume_yes=yes ;;
      --purge) purge=yes ;;
      *) die "unknown option: $1" ;;
    esac
    shift
  done
  require_root

  if [[ $assume_yes == no ]]; then
    [[ -t 0 ]] || die "refusing to uninstall without confirmation; pass --yes"
    printf 'This removes the site from this server: %s, %s, %s.\n' "$KROKOSHA_ROOT" "$KROKOSHA_WWW" "$KROKOSHA_STATE" >&2
    read -r -p "Type the word 'remove' to continue: " answer
    [[ $answer == remove ]] || die "cancelled"
  fi

  step "Stopping services"
  systemctl disable --now krokosha-sync.timer 2>/dev/null || true
  systemctl stop krokosha-sync.service 2>/dev/null || true
  rm -f /etc/systemd/system/krokosha-sync.service /etc/systemd/system/krokosha-sync.timer
  systemctl daemon-reload

  step "Removing the nginx site"
  rm -f /etc/nginx/sites-enabled/krokosha.conf /etc/nginx/sites-available/krokosha.conf \
    /etc/nginx/conf.d/krokosha-http.conf /etc/nginx/snippets/krokosha-*.conf \
    /etc/logrotate.d/krokosha /etc/fail2ban/jail.d/krokosha.conf \
    /etc/letsencrypt/renewal-hooks/deploy/krokosha-reload-nginx
  if have nginx && nginx -t 2>/dev/null; then
    systemctl reload nginx 2>/dev/null || true
  fi
  systemctl reload fail2ban 2>/dev/null || true

  step "Removing files"
  rm -rf "$KROKOSHA_ROOT" "$KROKOSHA_WWW" "$KROKOSHA_STATE" /var/log/krokosha
  if [[ $purge == yes ]]; then
    rm -rf "$KROKOSHA_ETC"
    ok "settings and secrets removed"
  else
    warn "$KROKOSHA_ETC is kept (settings, secrets); --purge removes it"
  fi
  if id "$KROKOSHA_USER" >/dev/null 2>&1; then
    userdel "$KROKOSHA_USER" 2>/dev/null || true
  fi

  ok "the site is removed. Packages, firewall rules and certificates were left as they are"
}

main "$@"
