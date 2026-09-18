#!/usr/bin/env bash
# Integration test of the installer. Runs in CI on a throw-away Ubuntu VM — NOT on a real server:
# it installs the site for a fake domain, uninstalls it, and plays with the firewall.
#
#   sudo deploy/ci/test-install.sh /path/to/checkout
set -euo pipefail

SOURCE=${1:?usage: test-install.sh /path/to/checkout}
DOMAIN=ci.krokosha.test
CURL=(curl --silent --show-error --max-time 20 --insecure
  --resolve "$DOMAIN:443:127.0.0.1" --resolve "$DOMAIN:80:127.0.0.1"
  --resolve "www.$DOMAIN:443:127.0.0.1" --resolve "www.$DOMAIN:80:127.0.0.1")

failures=0
check() { # check "description" command…
  local description=$1
  shift
  if "$@"; then
    printf '  ok    %s\n' "$description"
  else
    printf '  FAIL  %s\n' "$description"
    failures=$((failures + 1))
  fi
}
status() { "${CURL[@]}" --output /dev/null --write-out '%{http_code}' "$@"; }
# header URL NAME — value of a response header ("" if absent); HTTP/2 sends names in lower case.
header() {
  "${CURL[@]}" --output /dev/null --dump-header - "$1" | tr -d '\r' |
    awk -v name="$2:" 'tolower($1) == tolower(name) { sub(/^[^:]+: */, ""); print; exit }'
}
body() { "${CURL[@]}" "$@"; }

install_site() {
  "$SOURCE/deploy/install.sh" --domain "$DOMAIN" --email ci@example.com \
    --repo "$SOURCE" --branch ci-test --tls selfsigned --skip-dns-check --yes "$@"
}

echo "::group::A WireGuard interface that the installer must not break"
HAVE_WG=no
if apt-get install -y -qq wireguard-tools >/dev/null 2>&1 && ip link add wg-ci type wireguard 2>/dev/null; then
  wg set wg-ci listen-port 51999 private-key <(wg genkey)
  ip addr add 10.99.0.1/24 dev wg-ci
  ip link set wg-ci up
  sysctl -q -w net.ipv4.ip_forward=1
  HAVE_WG=yes
  echo "wg-ci is up on 51999/udp"
else
  echo "WireGuard is not available on this kernel — the VPN part of the test is skipped"
fi
echo "::endgroup::"

echo "::group::First installation"
install_site --allow 8443/tcp
echo "::endgroup::"

echo "Site"
check "home page is served over HTTPS" test "$(status "https://$DOMAIN/")" = 200
check "home page is the English one" grep -q '<html lang="en"' <(body "https://$DOMAIN/")
check "Ukrainian page" grep -q '<html lang="uk"' <(body "https://$DOMAIN/uk/")
check "Russian page" grep -q '<html lang="ru"' <(body "https://$DOMAIN/ru/")
check "canonical URL uses the installed domain" grep -q "rel=\"canonical\" href=\"https://$DOMAIN/\"" <(body "https://$DOMAIN/")
check "HTTP redirects to HTTPS" test "$(header "http://$DOMAIN/uk/" location)" = "https://$DOMAIN/uk/"
check "www redirects to the bare domain" test "$(header "https://www.$DOMAIN/ru/" location)" = "https://$DOMAIN/ru/"
check "a directory URL without a slash redirects" test "$(status "https://$DOMAIN/privacy")" = 301
check "unknown page is a 404" test "$(status "https://$DOMAIN/nope/")" = 404
check "404 under /uk/ is in Ukrainian" grep -q '<html lang="uk"' <(body "https://$DOMAIN/uk/nope/")
check "404 under /ru/ is in Russian" grep -q '<html lang="ru"' <(body "https://$DOMAIN/ru/nope/")
check "robots.txt" grep -q "^Sitemap: https://$DOMAIN/sitemap.xml" <(body "https://$DOMAIN/robots.txt")
check "sitemap.xml" grep -q "<loc>https://$DOMAIN/uk/</loc>" <(body "https://$DOMAIN/sitemap.xml")
check "requests for another host name get no answer" bash -c "! curl -s --max-time 5 -o /dev/null http://127.0.0.1/"
check "dotfiles are not served" test "$(status "https://$DOMAIN/.env")" = 404

echo "Headers"
check "X-Content-Type-Options" grep -qi nosniff <(header "https://$DOMAIN/" x-content-type-options)
check "frame-ancestors" grep -qi "frame-ancestors 'none'" <(header "https://$DOMAIN/" content-security-policy)
check "security headers survive on the 404 page" grep -qi nosniff <(header "https://$DOMAIN/nope/" x-content-type-options)
check "no HSTS with a self-signed certificate" test -z "$(header "https://$DOMAIN/" strict-transport-security)"
check "pages are revalidated" grep -qi no-cache <(header "https://$DOMAIN/" cache-control)
asset=$(body "https://$DOMAIN/" | grep -o '/_astro/[^" ]*\.css' | head -n 1)
check "hashed assets are cached for a year" grep -qi immutable <(header "https://$DOMAIN$asset" cache-control)
check "compression is on" grep -qiE 'content-encoding: (br|gzip)' <("${CURL[@]}" -H 'Accept-Encoding: br, gzip' -o /dev/null -D - "https://$DOMAIN/")
check "server version is hidden" test "$(header "https://$DOMAIN/" server)" = nginx

echo "Server"
check "release symlink" test -f /var/www/krokosha/current/index.html
check "site user has no shell" test "$(getent passwd krokosha | cut -d: -f7)" = /usr/sbin/nologin
check "settings are private" test "$(stat -c '%a %U' /etc/krokosha/env)" = "600 root"
check "rebuild timer is enabled" systemctl is-enabled --quiet krokosha-sync.timer
check "JSON access log is written" bash -c "tail -n 1 /var/log/krokosha/nginx-access.json.log | python3 -c 'import json,sys; json.loads(sys.stdin.read())'"

echo "Data, database, API"
check "/etc/krokosha is a link into the data root" test "$(readlink -f /etc/krokosha)" = /srv/krokosha/config
check "MySQL data lives in the data root" test -d /srv/krokosha/mysql/krokosha
check "Redis data lives in the data root" bash -c "ls /srv/krokosha/redis | grep -q ."
check "MySQL root password is not in the service environment" bash -c "! grep -q MYSQL_ROOT_PASSWORD /etc/krokosha/env"
check "MySQL listens on loopback only" bash -c "ss -Htln 'sport = :3306' | awk '{print \$4}' | grep -qx '127.0.0.1:3306' && ! ss -Htln 'sport = :3306' | grep -qE '(0\.0\.0\.0|\*|\[::\]):3306'"
check "Redis listens on loopback only" bash -c "ss -Htln 'sport = :6379' | awk '{print \$4}' | grep -qx '127.0.0.1:6379' && ! ss -Htln 'sport = :6379' | grep -qE '(0\.0\.0\.0|\*|\[::\]):6379'"
check "Redis refuses clients without the password" bash -c "docker exec krokosha-redis-1 redis-cli ping 2>&1 | grep -q NOAUTH"
check "API answers on loopback with details" grep -q '"mysql":"ok"' <(curl -s --max-time 5 http://127.0.0.1:8080/api/health)
check "API sees Redis" grep -q '"redis":"ok"' <(curl -s --max-time 5 http://127.0.0.1:8080/api/health)
check "API is reachable through nginx" test "$(status "https://$DOMAIN/api/health")" = 200
check "visitors get the bare status only" bash -c "! curl -sk --max-time 5 --resolve $DOMAIN:443:127.0.0.1 https://$DOMAIN/api/health | grep -q version"
check "API is not reachable from outside nginx" bash -c "ss -Htln 'sport = :8080' | awk '{print \$4}' | grep -qx '127.0.0.1:8080'"
check "database schema is migrated" bash -c "docker exec krokosha-mysql-1 sh -c 'mysql -N -uroot -p\"\$MYSQL_ROOT_PASSWORD\" krokosha -e \"SELECT COUNT(*) FROM schema_migrations\"' 2>/dev/null | grep -qE '^[1-9]'"
check "API service restarts cleanly" bash -c "systemctl restart krokosha-api && for i in \$(seq 1 30); do curl -sf --max-time 2 http://127.0.0.1:8080/api/health >/dev/null && exit 0; sleep 1; done; exit 1"
echo "  memory: $(docker stats --no-stream --format '{{.Name}} {{.MemUsage}}' | tr '\n' ';')"

echo "Analytics"
BROWSER='Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36'
sql() { docker exec krokosha-mysql-1 sh -c "mysql -N -uroot -p\"\$MYSQL_ROOT_PASSWORD\" krokosha -e \"$1\"" 2>/dev/null; }
beacon() { # beacon BODY [curl options…] → HTTP status of POST /api/e through nginx
  local body=$1
  shift
  "${CURL[@]}" --output /dev/null --write-out '%{http_code}' --request POST "https://$DOMAIN/api/e" \
    --header 'Content-Type: application/json' --user-agent "$BROWSER" --data "$body" "$@"
}
view='{"v":1,"id":"00112233aabbccdd","p":"/uk/","l":"uk","r":"www.google.com","u":{"s":"google","m":"cpc"},"e":[{"t":"pageview","o":0},{"t":"click","x":"cta-telegram","o":700},{"t":"scroll","v":50,"o":900}]}'
check "the statistics script is served" grep -q 'Never collected' <(body "https://$DOMAIN/assets/analytics.js")
check "a page view is accepted" test "$(beacon "$view")" = 204
check "Do Not Track is honoured" test "$(beacon "${view/00112233aabbccdd/00112233aabbcc01}" --header 'DNT: 1')" = 204
check "bots are not counted" test "$(beacon "${view/00112233aabbccdd/00112233aabbcc02}" --user-agent 'Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)')" = 204
check "pages of other sites cannot post" test "$(beacon "$view" --header 'Origin: https://evil.example')" = 403
check "a malformed batch is refused" test "$(beacon '{"v":1,"cookie":"x"}')" = 400
sleep 3 # the writer stores in batches, once a second
check "exactly one page view was stored" test "$(sql 'SELECT COUNT(*) FROM analytics_pageviews')" = 1
check "with its click event" test "$(sql "SELECT COUNT(*) FROM analytics_events WHERE type='click' AND target='cta-telegram'")" = 1
check "paid search traffic is recognised" test "$(sql "SELECT CONCAT(referrer_kind, ' ', is_ad, ' ', max_scroll) FROM analytics_pageviews")" = "search 1 50"
check "the address is stored truncated" bash -c "docker exec krokosha-mysql-1 sh -c 'mysql -N -uroot -p\"\$MYSQL_ROOT_PASSWORD\" krokosha -e \"SELECT ip_prefix FROM analytics_pageviews\"' 2>/dev/null | grep -qE '/(24|48)$'"

echo "Firewall"
check "UFW is active" bash -c "ufw status | grep -q 'Status: active'"
check "SSH stays open" bash -c "ufw status | grep -qE '^22/tcp +ALLOW'"
check "HTTP and HTTPS are open" bash -c "ufw status | grep -qE '^443/tcp +ALLOW' && ufw status | grep -qE '^80/tcp +ALLOW'"
check "--allow port is open" bash -c "ufw status | grep -qE '^8443/tcp +ALLOW'"
if [[ $HAVE_WG == yes ]]; then
  check "WireGuard port stays open" bash -c "ufw status | grep -qE '^51999/udp +ALLOW'"
  check "WireGuard clients are still routed" bash -c "ufw status | grep -qE 'ALLOW FWD +Anywhere on wg-ci'"
  check "packet forwarding is still on" test "$(sysctl -n net.ipv4.ip_forward)" = 1
fi

echo "::group::Second run: must change nothing and break nothing"
before=$(readlink -f /var/www/krokosha/current)
secrets_before=$(grep -E '^(MYSQL_PASSWORD|REDIS_PASSWORD|ADMIN_PATH)=' /etc/krokosha/env | sha256sum)
"$SOURCE/deploy/install.sh" --from-env --yes
echo "::endgroup::"
echo "Idempotency"
check "site still answers" test "$(status "https://$DOMAIN/")" = 200
check "settings survived" grep -q "^FIREWALL_ALLOW=8443/tcp" /etc/krokosha/env
check "API still answers" grep -q '"status":"ok"' <(curl -s --max-time 5 http://127.0.0.1:8080/api/health)
check "the skipped DNS check is remembered" grep -q "^SKIP_DNS_CHECK=yes" /etc/krokosha/env
check "a new release was published" test "$(readlink -f /var/www/krokosha/current)" != "$before"
check "generated secrets were kept" test "$(grep -E '^(MYSQL_PASSWORD|REDIS_PASSWORD|ADMIN_PATH)=' /etc/krokosha/env | sha256sum)" = "$secrets_before"
check "firewall has no duplicate rules" test "$(ufw status | grep -cE '^443/tcp +ALLOW')" = 1

echo "::group::update.sh"
/opt/krokosha/repo/deploy/update.sh
echo "::endgroup::"
check "site answers after update.sh" test "$(status "https://$DOMAIN/")" = 200

echo "Rollback"
live=$(readlink -f /var/www/krokosha/current)
/opt/krokosha/repo/deploy/rollback.sh >/dev/null 2>&1
check "rollback switched to an older release" test "$(readlink -f /var/www/krokosha/current)" \< "$live"
check "site answers after the rollback" test "$(status "https://$DOMAIN/")" = 200
check "rollback paused the timer" bash -c "! systemctl is-active --quiet krokosha-sync.timer"
check "at most three releases are kept" test "$(find /var/www/krokosha/releases -mindepth 1 -maxdepth 1 -type d | wc -l)" -le 3

echo "::group::Uninstall"
/opt/krokosha/repo/deploy/uninstall.sh --yes --purge
echo "::endgroup::"
check "files are gone" bash -c "[[ ! -e /opt/krokosha && ! -e /var/www/krokosha && ! -e /etc/krokosha && ! -e /srv/krokosha ]]"
check "containers are gone" bash -c "! docker ps -a --format '{{.Names}}' | grep -q '^krokosha-'"
check "API unit is gone" bash -c "! systemctl cat krokosha-api.service >/dev/null 2>&1"
check "user is gone" bash -c "! id krokosha >/dev/null 2>&1"
check "nginx configuration is still valid" nginx -t

if ((failures > 0)); then
  printf '\n%d check(s) failed\n' "$failures"
  journalctl -u krokosha-sync.service -n 60 --no-pager || true
  exit 1
fi
printf '\nAll checks passed\n'
