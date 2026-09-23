#!/usr/bin/env bash
# Removes what install.sh created: services, containers, nginx site, code, releases, the system user.
# The data root (database, mail, settings) is KEPT unless --purge is given.
#
# Left alone on purpose: installed packages (nginx, certbot, ufw, fail2ban), firewall rules,
# Let's Encrypt certificates, and anything that does not belong to the site.
#
#   sudo ./uninstall.sh            asks for confirmation
#   sudo ./uninstall.sh --yes      no questions (tests)
#   sudo ./uninstall.sh --purge    also deletes the data root: database, mail, settings, secrets
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
    if [[ $purge == yes ]]; then
      printf 'WITH --purge: the data root is deleted too — the database, mail and settings are gone for good.\n' >&2
    fi
    read -r -p "Type the word 'remove' to continue: " answer
    [[ $answer == remove ]] || die "cancelled"
  fi

  local data_dir
  data_dir=$(env_get KROKOSHA_DATA)

  step "Stopping services"
  systemctl disable --now krokosha-sync.timer krokosha-rebuild.path krokosha-api.service \
    krokosha-backup.timer krokosha-certwatch.timer krokosha-geoipupdate.timer krokosha-dbip.timer 2>/dev/null || true
  systemctl stop krokosha-sync.service krokosha-backup.service krokosha-certwatch.service krokosha-geoipupdate.service \
    krokosha-dbip.service 2>/dev/null || true
  rm -rf /etc/systemd/system/krokosha-sync.service /etc/systemd/system/krokosha-sync.timer \
    /etc/systemd/system/krokosha-backup.service /etc/systemd/system/krokosha-backup.timer \
    /etc/systemd/system/krokosha-certwatch.service /etc/systemd/system/krokosha-certwatch.timer \
    /etc/systemd/system/krokosha-geoipupdate.service /etc/systemd/system/krokosha-geoipupdate.timer \
    /etc/systemd/system/krokosha-dbip.service /etc/systemd/system/krokosha-dbip.timer \
    /etc/systemd/system/krokosha-rebuild.path \
    /etc/systemd/system/krokosha-api.service /etc/systemd/system/krokosha-api.service.d
  systemctl daemon-reload

  step "Stopping the containers (MySQL, Redis)"
  if have docker && [[ -f $KROKOSHA_REPO/deploy/compose/compose.yaml && -f $KROKOSHA_ENV ]]; then
    docker compose --env-file "$KROKOSHA_ENV" --env-file "$KROKOSHA_ETC/mysql-root.env" \
      --file "$KROKOSHA_REPO/deploy/compose/compose.yaml" --profile mail down --remove-orphans 2>/dev/null || true
  fi

  step "Removing the nginx site"
  rm -f /etc/nginx/sites-enabled/krokosha.conf /etc/nginx/sites-available/krokosha.conf \
    /etc/nginx/conf.d/krokosha-http.conf /etc/nginx/snippets/krokosha-*.conf \
    /etc/logrotate.d/krokosha /etc/fail2ban/jail.d/krokosha.conf /etc/fail2ban/filter.d/krokosha-admin.conf \
    /etc/letsencrypt/renewal-hooks/deploy/krokosha-reload-nginx
  if have nginx && nginx -t 2>/dev/null; then
    systemctl reload nginx 2>/dev/null || true
  fi
  # A restart, not a reload: the jail of the admin area is gone together with its log file.
  systemctl restart fail2ban 2>/dev/null || true

  step "Removing files"
  rm -rf "$KROKOSHA_ROOT" "$KROKOSHA_WWW" "$KROKOSHA_STATE" /var/log/krokosha
  [[ $(readlink /usr/local/bin/krokosha-cli 2>/dev/null) != "$KROKOSHA_ROOT"/* ]] || rm -f /usr/local/bin/krokosha-cli
  rm -f /usr/local/bin/krokosha-mailbox
  # The DB-IP database is of use to nobody else, and nothing would keep it fresh any more.
  rm -f /var/lib/GeoIP/dbip-city-lite.mmdb /var/lib/GeoIP/dbip-city-lite.mmdb.month /var/lib/GeoIP/.dbip.lock \
    /var/lib/GeoIP/.dbip-city-lite.*
  if [[ $purge == yes ]]; then
    rm -rf "$KROKOSHA_ETC"
    if [[ -n $data_dir && $data_dir == /* && $data_dir != / && -d $data_dir ]]; then
      rm -rf "$data_dir"
    fi
    # The MaxMind key and the database it fetched (the installer wrote both).
    if grep -qs 'krokosha-site' /etc/GeoIP.conf; then
      rm -f /etc/GeoIP.conf /var/lib/GeoIP/GeoLite2-*.mmdb
    fi
    ok "data root removed: database, settings and secrets are gone"
  else
    warn "the data root ${data_dir:-/srv/krokosha} is kept (database, settings, secrets); --purge removes it"
  fi
  if id "$KROKOSHA_USER" >/dev/null 2>&1; then
    userdel "$KROKOSHA_USER" 2>/dev/null || true
  fi

  ok "the site is removed. Packages, firewall rules and certificates were left as they are"
}

main "$@"
