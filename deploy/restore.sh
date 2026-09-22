#!/usr/bin/env bash
# Puts a backup made by backup.sh back — on the same server after an accident, or on a new one
# when the site moves (deploy/README.md, «Переезд на другой сервер»).
#
#   sudo /opt/krokosha/repo/deploy/restore.sh --from /srv/krokosha/backups/latest
#   sudo /opt/krokosha/repo/deploy/restore.sh --from /root/20260920-033000 --yes
#
# The site must be installed first (deploy/install.sh, the same domain): a fresh installation has
# its own database and cache passwords, and keeps them. What comes from the backup: the database,
# the mail (letters, mailboxes, the DKIM key), the files of requests, and the settings that must
# stay what they were — the secret that signs reply addresses and links, the path of the admin
# area, the password of the service mailbox, the bot's token and the address of the Bot API server
# it was made for. The installer then runs once more, so that nginx, the mail server and the API
# pick all of it up.
#
# Everything the installed site has in those places is REPLACED.
set -Eeuo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=deploy/lib/common.sh
source "$SCRIPT_DIR/lib/common.sh"

usage() { sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//' >&2; }

# Settings that travel with a backup. Everything else — passwords of the local database and
# cache, paths, the domain, the way TLS is done — belongs to the installation.
RESTORED_KEYS='^(APP_SECRET|ADMIN_PATH|MAIL_SERVICE_PASSWORD|MAILBOX|MAIL_NAME|TELEGRAM_BOT_TOKEN|TELEGRAM_API_URL|TELEGRAM_MODE|TELEGRAM_REMIND_MINUTES|TELEGRAM_DIGEST_AT|GITHUB_TOKEN|LEADS_[A-Z_]+|ANALYTICS_[A-Z_]+|BACKUP_[A-Z_]+|INDEXNOW|INDEXNOW_[A-Z_]+|MAXMIND_[A-Z_]+)='

main() {
  require_root
  local from='' assume_yes=no
  while (($#)); do
    case $1 in
      --from) from=${2:?--from needs the directory of a backup}; shift ;;
      --yes | -y) assume_yes=yes ;;
      -h | --help) usage; exit 0 ;;
      *) usage; die "unknown option: $1" ;;
    esac
    shift
  done
  [[ -n $from ]] || { usage; die "which backup? --from DIRECTORY"; }
  from=$(realpath "$from")
  [[ -f $from/MANIFEST && -f $from/mysql.sql.gz && -f $from/config.tar.gz ]] || die "$from is not a backup made by backup.sh"
  [[ -f $KROKOSHA_ENV && -d $KROKOSHA_REPO ]] || die "install the site first (deploy/install.sh with the same domain), then restore"
  gzip --test "$from/mysql.sql.gz" || die "the database dump in $from is damaged"

  local data_dir domain old_domain
  data_dir=$(env_get KROKOSHA_DATA)
  data_dir=${data_dir:-/srv/krokosha}
  domain=$(env_get DOMAIN)
  old_domain=$(sed -n 's/^domain: //p' "$from/MANIFEST")
  [[ -z $old_domain || $old_domain == "$domain" ]] ||
    warn "the backup is of $old_domain, this installation serves $domain: mailboxes and the DKIM key are made for the old name"

  step "Backup of $(sed -n 's/^made: //p' "$from/MANIFEST") from $(sed -n 's/^host: //p' "$from/MANIFEST")"
  if [[ $assume_yes != yes ]]; then
    [[ -t 0 ]] || die "no terminal to ask for confirmation: pass --yes"
    printf '    The database, the mail and the files of requests of %s will be REPLACED by this backup.\n    Type the domain to go on: ' "$domain" >&2
    local answer
    read -r answer
    [[ $answer == "$domain" ]] || die "not confirmed; nothing was changed"
  fi

  step "Stopping the services"
  systemctl stop krokosha-api.service krokosha-sync.timer 2>/dev/null || true
  local mail=no
  if docker ps --all --format '{{.Names}}' | grep -qx krokosha-mail-1; then
    mail=yes
    docker stop --time 60 krokosha-mail-1 >/dev/null
  fi
  ok "the API$([[ $mail == yes ]] && echo ' and the mail server') stopped; the site itself stays up"

  step "Database"
  local database
  database=$(env_get MYSQL_DATABASE)
  database=${database:-krokosha}
  docker exec krokosha-mysql-1 sh -c 'exec mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -e "DROP DATABASE IF EXISTS \`$0\`; CREATE DATABASE \`$0\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"' "$database" 2>/dev/null
  zcat "$from/mysql.sql.gz" | docker exec --interactive krokosha-mysql-1 sh -c 'exec mysql --default-character-set=utf8mb4 -uroot -p"$MYSQL_ROOT_PASSWORD" "$0"' "$database" 2>/dev/null
  ok "$(docker exec krokosha-mysql-1 sh -c 'exec mysql -N -uroot -p"$MYSQL_ROOT_PASSWORD" -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = \"$0\""' "$database" 2>/dev/null) tables"

  step "Mail and files of requests"
  local part
  for part in mail attachments; do
    [[ -d $from/$part ]] || continue
    install -d "$data_dir/$part"
    # The logs of this installation are not in the backup and stay where they are.
    rsync --archive --hard-links --delete --exclude='/logs/' "$from/$part/" "$data_dir/$part/"
    ok "$part: $(find "$data_dir/$part" -type f | wc -l) files"
  done
  if [[ -d $data_dir/attachments ]]; then
    chown -R "$KROKOSHA_USER:$KROKOSHA_USER" "$data_dir/attachments"
  fi

  step "Settings"
  local unpacked key line restored=0
  unpacked=$(mktemp -d)
  tar --extract --gzip --file "$from/config.tar.gz" --directory "$unpacked" config/env
  while IFS= read -r line; do
    [[ $line =~ $RESTORED_KEYS ]] || continue
    key=${line%%=*}
    env_set "$key" "${line#*=}"
    restored=$((restored + 1))
  done <"$unpacked/config/env"
  rm -rf "$unpacked"
  ok "$restored settings taken from the backup; the passwords of this installation's database and cache are kept"
  if [[ -f $from/letsencrypt.tar.gz && ! -d /etc/letsencrypt/live/$domain ]]; then
    tar --extract --gzip --file "$from/letsencrypt.tar.gz" --directory /etc
    ok "certificates of Let's Encrypt (no new ones have to be asked for)"
  fi

  step "Applying"
  # nginx serves the admin area under the restored secret path, the mail server reads the
  # restored mailboxes, the API starts with the restored secret: the installer does all of that.
  "$SCRIPT_DIR/install.sh" --from-env --yes
  ok "restored. Look at the admin area; letters and requests are as they were on $(sed -n 's/^made: //p' "$from/MANIFEST")"
}

main "$@"
