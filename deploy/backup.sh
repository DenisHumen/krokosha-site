#!/usr/bin/env bash
# Backs up everything that cannot be rebuilt from the repository (brief B8): the database, the
# mail (letters, mailboxes, the DKIM key), the files that came with requests, the settings with
# their secrets, the certificates. Runs every night (krokosha-backup.timer); by hand:
#
#   sudo /opt/krokosha/repo/deploy/backup.sh
#   sudo /opt/krokosha/repo/deploy/backup.sh --to /mnt/disk/krokosha --keep-daily 14 --keep-weekly 8
#
# A backup is a directory named after the moment it was made. Letters and files never change
# once written, so a backup shares them with the previous one through hard links: thirty
# backups of a mailbox take the room of one, and deleting any of them never hurts another.
#
# Backups next to the data survive a mistake, not a dead disk. BACKUP_RSYNC_TO in
# /etc/krokosha/env («user@host:/path», or a directory of another disk) makes every run end with
# a copy to there. To restore — on this server or on a new one — see restore.sh.
set -Eeuo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=deploy/lib/common.sh
source "$SCRIPT_DIR/lib/common.sh"

usage() { sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//' >&2; }

STARTED=$(date -u +%Y-%m-%dT%H:%M:%SZ)
STATUS_FILE=$KROKOSHA_STATE/status/backup.json
snapshot=''
work=''

# report OK ERROR — what the «system status» screen of the admin area shows.
report() {
  local ok=$1 error=${2:-} size=0 name=''
  if [[ -n $snapshot && -d $snapshot ]]; then
    size=$(du -sb "$snapshot" 2>/dev/null | cut -f1)
    name=$(basename "$snapshot")
  fi
  error=${error//\\/\\\\}
  error=${error//\"/\\\"}
  install -d -m 0750 -o "$KROKOSHA_USER" -g "$KROKOSHA_USER" "$(dirname "$STATUS_FILE")" 2>/dev/null || true
  printf '{"started_at":"%s","finished_at":"%s","ok":%s,"name":"%s","bytes":%s,"copied_to":"%s","error":"%s"}\n' \
    "$STARTED" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$ok" "$name" "${size:-0}" "${copied_to:-}" "$error" >"$STATUS_FILE.tmp"
  chmod 0644 "$STATUS_FILE.tmp"
  mv -f "$STATUS_FILE.tmp" "$STATUS_FILE"
}

# However the script ends, the status screen learns about it: a backup that silently stopped
# being made is worse than none.
finished=no
failed_at=''
on_exit() {
  local code=$?
  [[ $finished == yes ]] && return 0
  [[ -z $work || ! -d $work ]] || rm -rf "$work"
  snapshot=''
  report false "backup.sh stopped${failed_at:+ at line $failed_at} (exit code $code); details: journalctl -u krokosha-backup.service"
  # The owner hears about it — once a day, by mail and in Telegram.
  if [[ -x $KROKOSHA_ROOT/bin/krokosha-cli ]]; then
    printf 'backup.sh остановился%s, код выхода %s.\nПодробности: sudo journalctl -u krokosha-backup.service -n 50\n' "${failed_at:+ на строке $failed_at}" "$code" |
      "$KROKOSHA_ROOT/bin/krokosha-cli" alert --key "backup:$(date -u +%F)" --subject "Резервная копия не сделана" || true
  fi
}

main() {
  require_root
  [[ -f $KROKOSHA_ENV ]] || die "the site is not installed here: $KROKOSHA_ENV not found"

  local dest='' keep_daily keep_weekly copy_to data_dir
  keep_daily=$(env_get BACKUP_KEEP_DAILY)
  keep_weekly=$(env_get BACKUP_KEEP_WEEKLY)
  copy_to=$(env_get BACKUP_RSYNC_TO)
  data_dir=$(env_get KROKOSHA_DATA)
  data_dir=${data_dir:-/srv/krokosha}
  while (($#)); do
    case $1 in
      --to) dest=${2:?--to needs a directory}; shift ;;
      --keep-daily) keep_daily=${2:?--keep-daily needs a number}; shift ;;
      --keep-weekly) keep_weekly=${2:?--keep-weekly needs a number}; shift ;;
      -h | --help) usage; exit 0 ;;
      *) usage; die "unknown option: $1" ;;
    esac
    shift
  done
  dest=${dest:-$data_dir/backups}
  keep_daily=${keep_daily:-7}
  keep_weekly=${keep_weekly:-4}
  [[ $keep_daily =~ ^[1-9][0-9]*$ && $keep_weekly =~ ^[0-9]+$ ]] || die "--keep-daily wants a number from 1, --keep-weekly a number from 0"

  # One backup at a time: the nightly one and one started by hand must not share a directory.
  exec 9>/run/krokosha-backup.lock
  flock --nonblock 9 || die "another backup is running"

  trap 'failed_at=$LINENO' ERR
  trap on_exit EXIT
  dest=$(realpath --canonicalize-missing "$dest")
  install -d -m 0700 -o root -g root "$dest"
  local name previous
  name=$(date -u +%Y%m%d-%H%M%S)
  previous=$(find "$dest" -mindepth 1 -maxdepth 1 -type d -name '20*' | sort | tail -n 1)
  work=$dest/.partial-$name
  rm -rf "$work"
  install -d -m 0700 "$work"

  step "Database"
  local database
  database=$(env_get MYSQL_DATABASE)
  database=${database:-krokosha}
  # One consistent moment of every table, without stopping anything (InnoDB).
  docker exec krokosha-mysql-1 sh -c 'exec mysqldump --single-transaction --quick --routines --triggers --no-tablespaces \
      --default-character-set=utf8mb4 -uroot -p"$MYSQL_ROOT_PASSWORD" "$0"' "$database" 2>/dev/null | gzip -6 >"$work/mysql.sql.gz"
  gzip --test "$work/mysql.sql.gz"
  zcat "$work/mysql.sql.gz" | tail -n 1 | grep -q 'Dump completed' || die "the database dump is cut short"
  ok "$(du -h "$work/mysql.sql.gz" | cut -f1) — $(zcat "$work/mysql.sql.gz" | grep -c '^CREATE TABLE') tables"

  step "Settings and certificates"
  # /etc/krokosha lives in the data root; with the secrets in it, the archive is as private as they are.
  tar --create --gzip --file "$work/config.tar.gz" --directory "$data_dir" config
  if [[ -d /etc/letsencrypt/live ]]; then
    tar --create --gzip --file "$work/letsencrypt.tar.gz" --directory /etc letsencrypt
    ok "settings, certificates of Let's Encrypt"
  else
    ok "settings"
  fi

  step "Mail and files of requests"
  local part link
  for part in mail attachments; do
    [[ -d $data_dir/$part ]] || continue
    link=()
    [[ -z $previous || ! -d $previous/$part ]] || link=(--link-dest="$previous/$part")
    # Logs are not worth keeping; everything else is — letters, mailboxes, keys, what the spam
    # filter learned.
    rsync --archive --hard-links --delete --exclude='/logs/' "${link[@]}" "$data_dir/$part/" "$work/$part/"
    ok "$part: $(find "$work/$part" -type f | wc -l) files"
  done

  {
    printf 'krokosha backup %s\n' "$name"
    printf 'made: %s\nhost: %s\ndomain: %s\n' "$STARTED" "$(hostname -f 2>/dev/null || hostname)" "$(env_get DOMAIN)"
    printf 'code: %s\n' "$(git -C "$KROKOSHA_REPO" rev-parse --short HEAD 2>/dev/null || echo unknown)"
    printf 'mysql.sql.gz sha256: %s\n' "$(sha256sum "$work/mysql.sql.gz" | cut -d' ' -f1)"
    printf '\nRestore: sudo /opt/krokosha/repo/deploy/restore.sh --from <this directory>\n'
  } >"$work/MANIFEST"
  chmod -R go-rwx "$work/MANIFEST" "$work"/*.gz

  mv "$work" "$dest/$name"
  work=''
  snapshot=$dest/$name
  ln -sfn "$name" "$dest/latest"

  step "Old backups"
  rotate "$dest" "$keep_daily" "$keep_weekly"

  copied_to=''
  if [[ -n $copy_to ]]; then
    step "Copy to $copy_to"
    # -H keeps the hard links between backups: the copy is as small as the original.
    rsync --archive --hard-links --delete --exclude='.partial-*' -e 'ssh -o BatchMode=yes -o ConnectTimeout=20' "$dest/" "$copy_to/"
    copied_to=$copy_to
    ok "copied"
  else
    warn "BACKUP_RSYNC_TO is not set: the backups live on the same disk as the data. See deploy/README.md"
  fi

  finished=yes
  report true
  ok "backup $name: $(du -sh "$snapshot" | cut -f1) (shared with earlier backups where nothing changed)"
}

# rotate DIR DAILY WEEKLY — the newest DAILY backups stay; of the older ones, the first of each
# of the last WEEKLY weeks.
rotate() {
  local dir=$1 daily=$2 weekly=$3 all=() older=() kept_weeks=() backup week day oldest_week removed=0 index
  mapfile -t all < <(find "$dir" -mindepth 1 -maxdepth 1 -type d -name '20*' | sort)
  ((${#all[@]} > daily)) || { ok "nothing to remove: ${#all[@]} of $daily"; return 0; }
  older=("${all[@]:0:${#all[@]}-daily}")
  oldest_week=$(date -u -d "$weekly weeks ago" +%G%V)
  for backup in "${older[@]}"; do
    day=$(basename "$backup")
    week=$(date -u -d "${day:0:8}" +%G%V 2>/dev/null || echo 000000)
    index=" ${kept_weeks[*]:-} "
    if ((weekly > 0)) && [[ $week > $oldest_week || $week == "$oldest_week" ]] && [[ $index != *" $week "* ]]; then
      kept_weeks+=("$week")
      continue
    fi
    rm -rf "$backup"
    removed=$((removed + 1))
  done
  ok "removed: $removed, kept: $((${#all[@]} - removed))"
}

main "$@"
