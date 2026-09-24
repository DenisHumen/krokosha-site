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

# The first administrator is created without a terminal: the password comes from a file.
ADMIN_PATH=/_ci-Admin42
ADMIN_PASSWORD='ci: correct horse battery staple'
ADMIN_PASSWORD_FILE=$(mktemp)
printf '%s\n' "$ADMIN_PASSWORD" >"$ADMIN_PASSWORD_FILE"

MAILBOX_PASSWORD='ci: a mailbox password of some length'
MAILBOX_PASSWORD_FILE=$(mktemp)
printf '%s\n' "$MAILBOX_PASSWORD" >"$MAILBOX_PASSWORD_FILE"

install_site() {
  "$SOURCE/deploy/install.sh" --domain "$DOMAIN" --email ci@example.com \
    --mailbox "owner@$DOMAIN" --mail-name 'CI Owner' --mailbox-password-file "$MAILBOX_PASSWORD_FILE" \
    --repo "$SOURCE" --branch ci-test --tls selfsigned --skip-dns-check --yes \
    --admin-path "$ADMIN_PATH" --admin-login ci-admin --admin-password-file "$ADMIN_PASSWORD_FILE" \
    --dbip-url http://127.0.0.1:8090 --netmap-offline "$@"
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

# The Telegram bot: the installer checks the token with Telegram, the API talks to it all the
# time. Here «Telegram» is a small pretend server that writes down every call.
BOT_TOKEN='42424242:CI-token-that-must-not-show-up-in-logs'
BOT_TOKEN_FILE=$(mktemp) BOT_CALLS=$(mktemp) INSTALL_LOG=$(mktemp)
printf '%s\n' "$BOT_TOKEN" >"$BOT_TOKEN_FILE"
chmod 0666 "$BOT_CALLS"
python3 "$SOURCE/deploy/ci/mock-telegram.py" "$BOT_TOKEN" "$BOT_CALLS" 8088 &
MOCK_TELEGRAM=$!
trap 'kill "$MOCK_TELEGRAM" 2>/dev/null || true' EXIT
bot_called() { grep -c "\"method\": \"$1\"" "$BOT_CALLS" || true; }

# IndexNow: the engines are a small pretend server that writes every submission down.
INDEXNOW_CALLS=$(mktemp)
chmod 0666 "$INDEXNOW_CALLS"
python3 "$SOURCE/deploy/ci/mock-indexnow.py" "$INDEXNOW_CALLS" 8089 &
MOCK_INDEXNOW=$!
trap 'kill "$MOCK_TELEGRAM" "$MOCK_INDEXNOW" 2>/dev/null || true' EXIT

# DB-IP: a pretend download.db-ip.com, a directory of files served over HTTP. At first it has
# last month's file only — what the real one looks like early on the first day of a month.
DBIP_DIR=$(mktemp -d)
chmod 0755 "$DBIP_DIR"
THIS_MONTH=$(date -u +%Y-%m)
LAST_MONTH=$(date -u -d "$(date -u +%Y-%m-15) -1 month" +%Y-%m)
dbip_publish() { # dbip_publish MONTH NETWORK=CC:City… — a database of DB-IP's type, gzipped as DB-IP serves it
  local month=$1
  shift
  MMDB_TYPE=DBIP-City-Lite python3 "$SOURCE/deploy/ci/mmdb.py" "$DBIP_DIR/new.mmdb" "$@"
  gzip -c "$DBIP_DIR/new.mmdb" >"$DBIP_DIR/dbip-city-lite-$month.mmdb.gz"
  rm -f "$DBIP_DIR/new.mmdb"
}
dbip_publish "$LAST_MONTH" 127.0.0.0/8=PL:Warsaw
python3 -m http.server 8090 --bind 127.0.0.1 --directory "$DBIP_DIR" >/dev/null 2>&1 &
MOCK_DBIP=$!
trap 'kill "$MOCK_TELEGRAM" "$MOCK_INDEXNOW" "$MOCK_DBIP" 2>/dev/null || true' EXIT
# dbip_in_use MONTH — the API is told to read DB-IP City Lite of that month, and a timer keeps it fresh.
dbip_in_use() {
  [[ $(sed -n 's/^GEOIP_DB=//p' /etc/krokosha/env) == /var/lib/GeoIP/dbip-city-lite.mmdb &&
    $(stat -c '%a %U' /var/lib/GeoIP/dbip-city-lite.mmdb) == '644 root' &&
    $(cat /var/lib/GeoIP/dbip-city-lite.mmdb.month) == "$1" ]] &&
    systemctl is-enabled --quiet krokosha-dbip.timer
}
submissions() { wc -l <"$INDEXNOW_CALLS" | tr -d ' '; }
submission() { sed -n "${1}p" "$INDEXNOW_CALLS"; } # the Nth, as the engine received it

# GeoLite2: a key MaxMind will not accept. The installer must keep it to itself, say that the
# database could not be fetched, and go on with DB-IP City Lite meanwhile.
MAXMIND_KEY='CI0000_a_fake_key_that_must_not_show_up_in_logs'
MAXMIND_KEY_FILE=$(mktemp)
printf '%s\n' "$MAXMIND_KEY" >"$MAXMIND_KEY_FILE"

echo "::group::First installation"
install_site --allow 8443/tcp --telegram-token-file "$BOT_TOKEN_FILE" --telegram-api http://127.0.0.1:8088 \
  --maxmind-account 999999 --maxmind-key-file "$MAXMIND_KEY_FILE" --indexnow-api http://127.0.0.1:8089/indexnow 2>&1 | tee "$INSTALL_LOG"
echo "::endgroup::"
INDEXNOW_KEY=$(sed -n 's/^INDEXNOW_KEY=//p' /etc/krokosha/env)

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
# Google Search asks for the icon the home page links; browsers and crawlers ask for /favicon.ico.
check "the icon for Google Search is a PNG" test "$(header "https://$DOMAIN/favicon-96.png" content-type)" = image/png
check "favicon.ico is served" test "$(header "https://$DOMAIN/favicon.ico" content-type)" = image/x-icon
# Let's Encrypt checks every name of the certificate over plain HTTP — mail.<domain> included,
# at issue and at every renewal. A name that port 80 does not answer fails the whole certificate.
acme_probe=/var/www/krokosha/acme/.well-known/acme-challenge/ci-probe
install -D -m 0644 /dev/null "$acme_probe" && echo ci-acme >"$acme_probe"
for name in "$DOMAIN" "www.$DOMAIN" "mail.$DOMAIN"; do
  check "Let's Encrypt can reach its challenge under $name" test "$(curl -s --max-time 5 -H "Host: $name" http://127.0.0.1/.well-known/acme-challenge/ci-probe)" = ci-acme
done
rm -f "$acme_probe"
check "…while the mail name leads nowhere else than the site" test "$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' --max-time 5 -H "Host: mail.$DOMAIN" http://127.0.0.1/anything)" = "301 https://$DOMAIN/"
check "sitemap.xml" grep -q "<loc>https://$DOMAIN/uk/</loc>" <(body "https://$DOMAIN/sitemap.xml")
check "the map of the internet" grep -q 'data-netmap' <(body "https://$DOMAIN/uk/map/")
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

echo "Contact form"
# A visitor without JavaScript: a plain form post, answered with a page of the site itself.
lead() { # → «STATUS LOCATION» of a plain POST to /api/leads
  "${CURL[@]}" --output /dev/null --write-out '%{http_code} %{redirect_url}' --user-agent "$BROWSER" \
    --request POST "https://$DOMAIN/api/leads" \
    --data-urlencode 'name=Иван Петров' --data-urlencode 'contact_method=email' --data-urlencode 'contact_value=ivan@company.test' \
    --data-urlencode 'direction=networks' --data-urlencode 'description=Нужно перестроить сеть офиса на 40 мест: MikroTik и два VLAN.' \
    --data-urlencode 'budget=2' --data-urlencode 'consent=on' --data-urlencode 'lang=ru'
}
check "the form is on the page and posts to the API" grep -q '<form method="post" action="/api/leads"' <(body "https://$DOMAIN/ru/")
check "the puzzle against spam is handed out" grep -q '"algorithm":"SHA-256"' <(body "https://$DOMAIN/api/leads/challenge")
answer=$(lead)
check "a plain form post is accepted and redirected" grep -qE "^303 https://$DOMAIN/api/leads/thanks\?t=[A-Za-z0-9_-]{22}$" <<<"$answer"
thanks=$(body "${answer#303 }")
check "the thank-you page is the site's own, in the visitor's language" grep -q '<html lang="ru"' <<<"$thanks"
check "…with the number of the request filled in" grep -q 'Заявка #K-0001 принята' <<<"$thanks"
check "…and no marks left over" bash -c "! grep -q '%%' <<<\"\$1\"" _ "$thanks"
check "a made-up link shows nobody's request" test "$(header "https://$DOMAIN/api/leads/thanks?t=AAAAAAAAAAAAAAAAAAAAAA" location)" = /thanks/
check "the request is stored" test "$(sql "SELECT CONCAT(status, ' ', lang, ' ', contact_value) FROM leads WHERE id = 1")" = "new ru ivan@company.test"
check "with the truncated address only" bash -c "docker exec krokosha-mysql-1 sh -c 'mysql -N -uroot -p\"\$MYSQL_ROOT_PASSWORD\" krokosha -e \"SELECT ip_prefix FROM leads\"' 2>/dev/null | grep -qE '/(24|48)$'"
# Three of them: a letter to the owner, a confirmation to the client, a card in Telegram. With the
# mail server running they start leaving at once, so the state of each is not asked about here.
check "notifications are queued in the same transaction" test "$(sql "SELECT COUNT(*) FROM outbox WHERE lead_id = 1")" = 3
lead_json() { # lead_json NAME METHOD CONTACT DESCRIPTION [curl options…] → the JSON answer
  local name=$1 method=$2 contact=$3 description=$4
  shift 4
  "${CURL[@]}" --user-agent "$BROWSER" --header 'Accept: application/json' --request POST "https://$DOMAIN/api/leads" \
    --data-urlencode "name=$name" --data-urlencode "contact_method=$method" --data-urlencode "contact_value=$contact" \
    --data-urlencode 'direction=devops' --data-urlencode "description=$description" --data-urlencode 'consent=on' --data-urlencode 'lang=en' "$@"
}
check "a script gets JSON" grep -q '"id":"K-0002"' <(lead_json Second telegram @second_client 'CI/CD for a small project, two environments.')
check "mistakes are explained, not stored" grep -q '"contact_value":"invalid_email"' <(lead_json Third email not-an-email 'CI/CD for a small project, two environments.')
check "a robot that fills the hidden field gets the usual answer" grep -q '"id":"K-0003"' <(lead_json Bot email bot@spam.test 'Buy cheap traffic for your website today' --data-urlencode 'website=http://spam.test')
check "…but is filed as spam, without notifications" test "$(sql "SELECT CONCAT(l.status, ' ', (SELECT COUNT(*) FROM outbox o WHERE o.lead_id = l.id)) FROM leads l WHERE l.id = 3")" = "spam 0"
check "the fourth request within an hour is sent back to the form's explanation" grep -q '^303 .*/ru/#form-error-rate$' <(lead)
check "…and a script is told 429" grep -q '"error":"rate_limited"' <(lead_json Fifth email fifth@company.test 'One more request within the same hour.')
check "forms posted from other sites are refused" grep -q 'cross-origin' <(lead_json Evil email evil@company.test 'Posted by a page of another site.' --header 'Origin: https://evil.example')

echo "Admin area"
ADMIN="https://$DOMAIN$ADMIN_PATH"
JAR=$(mktemp)
admin_get() { "${CURL[@]}" --cookie "$JAR" --user-agent "$BROWSER" "$@"; }
admin_post() { # admin_post PATH [curl options…] → HTTP status; keeps the session cookie in $JAR
  local path=$1
  shift
  "${CURL[@]}" --output /dev/null --write-out '%{http_code}' --cookie "$JAR" --cookie-jar "$JAR" \
    --user-agent "$BROWSER" --request POST "$ADMIN$path" "$@"
}
csrf() { admin_get "$ADMIN/account" | sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' | head -n 1; }
check "the secret path is the one asked for" grep -qx "ADMIN_PATH=$ADMIN_PATH" /etc/krokosha/env
check "the installer created the administrator" grep -q '^ci-admin ' <(krokosha-cli admin list)
check "login form is served under the secret path" grep -q 'name="password"' <(body "$ADMIN/login")
check "the admin area is not indexed" grep -qi noindex <(header "$ADMIN/login" x-robots-tag)
check "the admin area is never cached" grep -qi no-store <(header "$ADMIN/login" cache-control)
csp=$(header "$ADMIN/login" content-security-policy)
check "its CSP takes scripts and styles from the site only" grep -q "script-src 'self'; style-src 'self'" <<<"$csp"
check "its CSP allows no inline code" bash -c "! grep -q unsafe-inline" <<<"$csp"
check "its stylesheet is served" test "$(status "$ADMIN/static/admin.css")" = 200
check "the bare secret path redirects into the area" test "$(header "$ADMIN" location)" = "$ADMIN_PATH/"
check "anonymous visitors are sent to the login form" test "$(header "$ADMIN/" location)" = "$ADMIN_PATH/login"
check "a guessed path is an ordinary 404" test "$(status "https://$DOMAIN/_admin/login")" = 404
check "plain HTTP never reaches the admin area" test "$(header "http://$DOMAIN$ADMIN_PATH/login" location)" = "$ADMIN/login"
check "the secret path stays out of the traffic log" bash -c "! grep -q -- '$ADMIN_PATH' /var/log/krokosha/nginx-access.json.log"
check "a wrong password is refused" test "$(admin_post /login --data-urlencode login=ci-admin --data-urlencode 'password=wrong wrong wrong')" = 401
check "the right password signs in" test "$(admin_post /login --data-urlencode login=ci-admin --data-urlencode "password=$ADMIN_PASSWORD")" = 303
check "the session cookie is Secure, HttpOnly and host-only" grep -qP '^#HttpOnly_\S+\tFALSE\t/\tTRUE\t\d+\t__Host-ks\t' "$JAR"
check "the overview opens after signing in" grep -q 'ci-admin' <(admin_get "$ADMIN/")
# Reports follow the owner's day (content/site.yaml → timezone), this machine runs on UTC: around
# midnight the visit recorded above may belong to either day, so both are asked for.
# (grep -q stops reading at the first match; curl's complaint about the closed pipe is noise.)
both_days() { { admin_get "$ADMIN$1" && admin_get "$ADMIN$1?p=day&d=$(date -u +%F)"; } 2>/dev/null || true; }
check "the overview shows the visit recorded above" grep -q '<title>[0-9:]* — 1 визит, из них по рекламе: 1[;<]' <(both_days /)
check "the list of visits" grep -q 'google.com' <(both_days /visits)
check "CSV export of page views" grep -q ',/uk/,uk,search,google.com,google,cpc,' <(both_days /export/pageviews.csv)
check "CSV export of events" grep -q ',click,cta-telegram,' <(both_days /export/events.csv)
# nginx must pass the live feed on event by event: the first one arrives at once, not when a buffer fills.
check "the live feed streams through nginx" grep -q '^event: active' <(admin_get --no-buffer --max-time 3 "$ADMIN/live" 2>/dev/null || true)
check "the owner's own page view is answered like any other" test "$(beacon "${view/00112233aabbccdd/00112233aabbcc03}" --cookie "$JAR")" = 204
sleep 2
check "but it is not counted: the owner is not a visitor" test "$(sql 'SELECT COUNT(*) FROM analytics_pageviews')" = 1

echo "Server traffic and system status"
# The API follows nginx's access log; it looks at it every ten seconds.
wait_for() { # wait_for SECONDS command… — true as soon as the command succeeds
  local deadline=$((SECONDS + $1))
  shift
  until "$@" 2>/dev/null; do
    ((SECONDS < deadline)) || return 1
    sleep 2
  done
}
traffic_is_counted() { [[ $(sql 'SELECT COALESCE(SUM(requests), 0) FROM traffic_minutes') -ge 20 ]]; }
check "requests from nginx's log are counted" wait_for 40 traffic_is_counted
check "page addresses are kept without query strings" test "$(sql "SELECT COUNT(*) FROM traffic_paths WHERE path LIKE '%?%'")" = 0
check "the admin area stays out of the traffic statistics" test "$(sql "SELECT COUNT(*) FROM traffic_paths WHERE path LIKE '$ADMIN_PATH%'")" = 0
check "the scanner-like requests of this test are noticed" test "$(sql "SELECT COUNT(*) FROM traffic_probes WHERE pattern = '.env'")" -ge 1
check "networks of scanners are truncated" test "$(sql "SELECT COUNT(*) FROM traffic_probes WHERE ip_prefix NOT LIKE '%/24' AND ip_prefix NOT LIKE '%/48'")" = 0
check "the traffic screen" grep -q 'curl' <(both_days /traffic)
check "the overview's timeline shows bots from the server log" grep -q 'chart-bar-bots' <(both_days /)

check "the build left its report" python3 -c "import json; r = json.load(open('/var/lib/krokosha/status/sync.json')); assert r['ok'] and r['step'] == 'done' and r['release'], r"
status_page=$(admin_get "$ADMIN/status")
check "the status screen names the live release" grep -q "$(basename "$(readlink -f /var/www/krokosha/current)")" <<<"$status_page"
check "…and describes the certificate nginx serves" grep -q 'не доверенный' <<<"$status_page"
check "the API may write rebuild requests, and only there" bash -c "systemctl show krokosha-api.service -p ReadWritePaths | grep -q /var/lib/krokosha/requests && systemctl show krokosha-api.service -p ProtectSystem | grep -q strict"
# npm runs third-party code in the build: nothing it may write is run by root or built into a program.
build_writes=$(systemctl show krokosha-sync.service -p ReadWritePaths --value)
check "the build may write web/ of the repository, not the rest of it" bash -c "grep -qw /opt/krokosha/repo/web <<<'$build_writes' && ! grep -qE '(^| )/opt/krokosha/repo( |$)' <<<'$build_writes' && ! grep -qE '(^| )/var/lib/krokosha( |$)' <<<'$build_writes'"
check "…nor read the files of requests" bash -c "systemctl show krokosha-sync.service -p InaccessiblePaths --value | grep -q /srv/krokosha/attachments"
check "the API's environment is root's to read, not its own user's" test "$(stat -c %U "/proc/$(systemctl show krokosha-api.service -p MainPID --value)/environ")" = root
before_rebuild=$(readlink -f /var/www/krokosha/current)
check "the «rebuild now» button is accepted" test "$(admin_post /status/rebuild --data-urlencode "csrf=$(csrf)")" = 303
site_was_rebuilt() { [[ $(readlink -f /var/www/krokosha/current) != "$before_rebuild" && ! -e /var/lib/krokosha/requests/rebuild ]]; }
check "…and a new release is published within two minutes" wait_for 120 site_was_rebuilt
check "the button is in the audit log" test "$(sql "SELECT COUNT(*) FROM audit_log WHERE action = 'admin.rebuild'")" = 1

echo "IndexNow"
key_served() { [[ $INDEXNOW_KEY =~ ^[0-9a-f]{32}$ && $(body "https://$DOMAIN/$INDEXNOW_KEY.txt") == "$INDEXNOW_KEY" ]]; }
check "the installer generated a key and the site serves it" key_served
check "the first release told the engines about every page of the sitemap, with the key and where it is served" test "$(submission 1)" = "{\"host\": \"$DOMAIN\", \"key\": \"$INDEXNOW_KEY\", \"keyLocation\": \"https://$DOMAIN/$INDEXNOW_KEY.txt\", \"urlList\": [\"https://$DOMAIN/\", \"https://$DOMAIN/uk/\", \"https://$DOMAIN/ru/\", \"https://$DOMAIN/map/\", \"https://$DOMAIN/uk/map/\", \"https://$DOMAIN/ru/map/\"]}"
check "…the rebuild above, with nothing changed, told them nothing" test "$(submissions)" = 1
# A change of the English home page only: the engines hear about that page and about nothing
# else (a page whose HTML is byte for byte the release before did not change).
sed -i 's/en: "Networks & network hardware"/en: "Networks \& network hardware (CI)"/' /opt/krokosha/repo/content/site.yaml
before_rebuild=$(readlink -f /var/www/krokosha/current)
check "a rebuild after a change of one page" test "$(admin_post /status/rebuild --data-urlencode "csrf=$(csrf)")" = 303
check "…publishes a new release" wait_for 120 site_was_rebuilt
check "…and tells the engines about that page only" test "$(submissions) $(submission 2 | python3 -c 'import json, sys; print(*json.load(sys.stdin)["urlList"])')" = "2 https://$DOMAIN/"
runuser -u krokosha -- git -C /opt/krokosha/repo checkout --quiet -- content/site.yaml

echo "GeoIP"
check "the MaxMind account and key are in the settings and in /etc/GeoIP.conf, for root only" bash -c "grep -q '^MAXMIND_ACCOUNT_ID=999999\$' /etc/krokosha/env && grep -q '^MAXMIND_LICENSE_KEY=$MAXMIND_KEY\$' /etc/krokosha/env && grep -q '^AccountID 999999\$' /etc/GeoIP.conf && grep -q '^LicenseKey $MAXMIND_KEY\$' /etc/GeoIP.conf && [[ \$(stat -c '%a %U' /etc/GeoIP.conf) == '600 root' ]]"
check "…and the key shows up nowhere in the installer's output" bash -c "! grep -qF '$MAXMIND_KEY' '$INSTALL_LOG'"
check "a key MaxMind refuses: the installer said so and went on" grep -q 'could not be fetched yet' "$INSTALL_LOG"
check "GeoLite2 is tried twice a week" systemctl is-enabled --quiet krokosha-geoipupdate.timer
check "meanwhile the free DB-IP City Lite is used: last month's, as this month's is not out yet" dbip_in_use "$LAST_MONTH"
check "…fetched from the address given to the installer" grep -q '^DBIP_URL=http://127.0.0.1:8090$' /etc/krokosha/env
geo_status() { # geo_status TEXT — the line «База GeoIP» of the status screen says TEXT
  local page
  page=$(admin_get "$ADMIN/status")
  grep -q "$1" <<<"$page" || { grep -o 'База GeoIP.\{0,160\}' <<<"$page" | head -n 2; return 1; }
}
check "the status screen names the database" geo_status 'DBIP-City-Lite от 10.09.2026'
check "…and the admin area credits DB-IP, as its licence asks" grep -q '>IP Geolocation by DB-IP</a>' <(admin_get "$ADMIN/")
geo_recorded() { # geo_recorded ID 'CC City' — a page view with that id gets that place
  [[ $(beacon "${view/00112233aabbccdd/$1}") == 204 ]] || return 1
  sleep 3
  [[ $(sql "SELECT CONCAT(country, ' ', city) FROM analytics_pageviews WHERE pageview_id = UNHEX('$1')") == "$2" ]]
}
check "a page view gets its country and city from the database" geo_recorded 00112233aabbcc77 'PL Warsaw'
check "…the address itself is still not stored" test "$(sql "SELECT COUNT(*) FROM analytics_pageviews WHERE ip_prefix NOT LIKE '%/24' AND ip_prefix NOT LIKE '%/48'")" = 0
check "the overview names the country" grep -q 'Польша' <(both_days /)
# After the second run of the installer the admin area is read once more. By then this test has
# signed out and run into the limit of sign-ins (five wrong attempts per login and per network),
# so a session for that is opened now.
GEO_JAR=$(mktemp)
geo_login=$("${CURL[@]}" --output /dev/null --write-out '%{http_code}' --cookie-jar "$GEO_JAR" --user-agent "$BROWSER" \
  --request POST "$ADMIN/login" --data-urlencode login=ci-admin --data-urlencode "password=$ADMIN_PASSWORD")
check "…a session is kept for the checks after the second run" test "$geo_login" = 303
geo_page_says() { # geo_page_says PATH TEXT — that page of the admin area, read in that session, says TEXT
  local page
  page=$("${CURL[@]}" --cookie "$GEO_JAR" --user-agent "$BROWSER" "$ADMIN$1")
  grep -q "$2" <<<"$page" || { grep -o 'База GeoIP.\{0,160\}\|<footer.\{0,300\}\|<title>[^<]*' <<<"$page" | head -n 3; return 1; }
}
# geoipupdate would bring the real GeoLite2 one day; a file written by the test stands in for it.
# The next run of the installer (the second run below) is to switch the API over to it.
MMDB_TYPE=GeoLite2-City python3 "$SOURCE/deploy/ci/mmdb.py" /var/lib/GeoIP/GeoLite2-City.mmdb 127.0.0.0/8=UA:Kyiv

echo "Map of the internet"
# Offline in CI (--netmap-offline): a small world stands in for the internet, nothing is downloaded.
check "the map is rebuilt every night" systemctl is-enabled --quiet krokosha-netmap.timer
check "the installer kept it offline" grep -q '^NETMAP_FETCH=off$' /etc/krokosha/env
check "before the first map the page's data is not there" test "$(status "https://$DOMAIN/netmap/data/overview.json")" = 404
check "…and the API says the map is not ready" test "$(status "https://$DOMAIN/api/net/route?from=81.0.0.10&to=82.0.0.20")" = 503
cp -r "$SOURCE/api/internal/netmap/testdata/world/." /var/lib/krokosha/netmap/
chown -R krokosha:krokosha /var/lib/krokosha/netmap
netmap_sync() { systemctl start krokosha-netmap.service || { journalctl -u krokosha-netmap.service -n 40 --no-pager; return 1; }; }
check "the nightly job builds the map, the way its unit runs it" netmap_sync
check "…notes the run: 6 networks, 8 links" test "$(sql "SELECT CONCAT(ok, ' ', networks, ' ', links) FROM netmap_sync ORDER BY id DESC LIMIT 1")" = "1 6 8"
check "…and keeps the map in MySQL" test "$(sql "SELECT COUNT(*) FROM netmap_link WHERE gone_at IS NULL")" = 8
netmap_file=$(body "https://$DOMAIN/netmap/data/overview.json" | python3 -c 'import json, sys; print(json.load(sys.stdin)["file"])' 2>/dev/null || true)
check "the page finds the overview through its manifest" test "$(status "https://$DOMAIN/netmap/data/$netmap_file")" = 200
netmap_headers() { "${CURL[@]}" --output /dev/null --dump-header - --header 'Accept-Encoding: gzip' "$1" | tr -d '\r'; }
check "…sent gzipped as the job wrote it, and kept for good" bash -c "grep -qi '^content-encoding: gzip' <<<\"\$1\" && grep -qi '^cache-control: public, max-age=31536000, immutable' <<<\"\$1\"" _ "$(netmap_headers "https://$DOMAIN/netmap/data/$netmap_file")"
route_answers() { body "https://$DOMAIN/api/net/route?from=81.0.0.10&to=82.0.0.20" | grep -q '"asn":7000'; }
check "the API picks the new map up by itself and draws routes on it" wait_for 90 route_answers
check "addresses people trace stay out of the access log" bash -c "grep -q '\"uri\":\"/api/net/route\"' /var/log/krokosha/nginx-access.json.log && ! grep -q '81\.0\.0\.10' /var/log/krokosha/nginx-access.json.log"
check "the status screen describes the map" grep -q 'сетей: 6 · связей: 8' <(admin_get "$ADMIN/status")
check "a second run with nothing new" netmap_sync
check "…writes nothing" test "$(sql "SELECT CONCAT(ok, ' ', added, ' ', gone, ' ', changed) FROM netmap_sync ORDER BY id DESC LIMIT 1") $(sql "SELECT COUNT(*) FROM netmap_change")" = "1 0 0 0 0"

echo "Requests in the admin area"
check "the list shows the requests sent above" grep -q '#K-0001' <(admin_get "$ADMIN/leads")
check "spam is kept apart" bash -c "! grep -q '#K-0003' <<<\"\$1\"" _ "$(admin_get "$ADMIN/leads")"
check "…but can be looked at" grep -q '#K-0003' <(admin_get "$ADMIN/leads?status=spam")
check "the board" grep -q 'data-board' <(admin_get "$ADMIN/leads?view=board")
check "the card shows what the visitor wrote" grep -q 'MikroTik и два VLAN' <(admin_get "$ADMIN/leads/1")
check "ready-made answers are offered in the client's language" grep -q 'Нужны детали' <(admin_get "$ADMIN/leads/1")
check "taking a request" test "$(admin_post /leads/1/status --data-urlencode "csrf=$(csrf)" --data-urlencode 'status=in_progress')" = 303
check "…is recorded with the name of who took it" test "$(sql "SELECT CONCAT(status, ' ', assignee) FROM leads WHERE id = 1")" = "in_progress ci-admin"
check "a forbidden change of status is refused" test "$(admin_post /leads/1/status --data-urlencode "csrf=$(csrf)" --data-urlencode 'status=spam')" = 409
check "an answer to the client" test "$(admin_post /leads/1/reply --data-urlencode "csrf=$(csrf)" --data-urlencode 'text=Спасибо, изучу и отвечу до конца дня.')" = 303
reply_left() { [[ $(sql "SELECT CONCAT(l.status, ' ', o.status) FROM leads l JOIN outbox o ON o.lead_id = l.id AND o.kind = 'lead.reply' WHERE l.id = 1") == "waiting_client sent" ]]; }
check "…leaves through the site's own mail server" wait_for 30 reply_left
check "a note for colleagues" test "$(admin_post /leads/1/note --data-urlencode "csrf=$(csrf)" --data-urlencode 'text=Клиент из теста установки.')" = 303
check "the status screen shows the queue of notifications" grep -q 'Уведомления (outbox)' <(admin_get "$ADMIN/status")
check "the templates editor" grep -q 'Not my field' <(admin_get "$ADMIN/templates")
check "requests as CSV" grep -q '^K-0002,' <(admin_get "$ADMIN/leads/export.csv")
check "deleting a client's data needs the number typed in" test "$(admin_post /leads/2/delete --data-urlencode "csrf=$(csrf)" --data-urlencode 'confirm=K-0001')" = 400
check "…and then removes everything about the request" test "$(admin_post /leads/2/delete --data-urlencode "csrf=$(csrf)" --data-urlencode 'confirm=K-0002')" = 303
check "…the conversation and the queued notifications included" test "$(sql 'SELECT (SELECT COUNT(*) FROM leads WHERE id = 2) + (SELECT COUNT(*) FROM lead_messages WHERE lead_id = 2) + (SELECT COUNT(*) FROM outbox WHERE lead_id = 2)')" = 0
check "…leaving one line in the journal" test "$(sql "SELECT CONCAT(actor, ' ', subject) FROM audit_log WHERE action = 'lead.delete'")" = "ci-admin K-0002"
echo "Files with a request"
# The form takes no files unless content/site.yaml says so (contacts.form.attachments). The API is
# asked directly here, each time «from» another address: this machine has used up its three
# requests an hour above.
pdf=$(mktemp --suffix=.pdf) program=$(mktemp --suffix=.pdf) megabyte=$(mktemp --suffix=.txt) toobig=$(mktemp --suffix=.txt)
printf '%%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%%%EOF\n' >"$pdf"
{ printf 'MZ\220\000\003\000\000\000'; head -c 4096 /dev/urandom; } >"$program"
head -c 1048576 /dev/zero | tr '\0' 'x' >"$megabyte"
head -c 34000000 /dev/zero | tr '\0' 'x' >"$toobig"
form_fields=(--form 'name=Файловый клиент' --form 'contact_method=email' --form 'contact_value=files@company.test'
  --form 'direction=servers' --form 'description=Схема стойки и список оборудования — во вложении.' --form 'consent=on' --form 'lang=ru')
lead_files() { # lead_files ADDRESS [curl options…] → the JSON answer of the API itself
  local address=$1
  shift
  curl --silent --max-time 30 --header 'Accept: application/json' --header "Host: $DOMAIN" --header "X-Real-IP: $address" \
    "${form_fields[@]}" "$@" http://127.0.0.1:8080/api/leads
}
check "files are refused while the form takes none" grep -q '"files":"files_disabled"' <(lead_files 198.51.100.21 --form "files=@$pdf")
sed -i 's/^\( *attachments:\) false/\1 true /' /opt/krokosha/repo/content/site.yaml
sleep 6 # the API looks at site.yaml every five seconds
check "a request with files is accepted once the content allows them" grep -q '"ok":true' \
  <(lead_files 198.51.100.22 --form "files=@$pdf;filename=Схема стойки.pdf" --form "files=@$megabyte;filename=notes.txt")
file_lead=$(sql 'SELECT MAX(lead_id) FROM lead_attachments')
file_id=$(sql 'SELECT MIN(id) FROM lead_attachments')
check "the files are in the data root, under names of their own" test "$(find /srv/krokosha/attachments -type f -regex '.*/[0-9a-f]+' | wc -l)" = 2
check "…for the service's eyes only" test "$(find /srv/krokosha/attachments -type f ! -perm 600 | wc -l) $(stat -c '%U %a' /srv/krokosha/attachments)" = "0 krokosha 700"
check "a program called .pdf is told by what is inside it" grep -q '"files":"file_type"' <(lead_files 198.51.100.23 --form "files=@$program;filename=invoice.pdf")
check "four files are one too many" grep -q '"files":"too_many_files"' \
  <(lead_files 198.51.100.24 --form "files=@$pdf" --form "files=@$pdf" --form "files=@$pdf" --form "files=@$pdf")
check "refused files leave nothing behind" test "$(find /srv/krokosha/attachments -type f | wc -l)" = 2
# Through nginx. This address is over its hourly limit, so the API's «too many» is the proof that
# nginx let the megabyte through; nginx's own refusal would be a 413 without JSON.
check "nginx lets a form with files through to the API" grep -q '"error":"rate_limited"' \
  <(body --header 'Accept: application/json' "${form_fields[@]}" --form "files=@$megabyte" "https://$DOMAIN/api/leads")
check "…and stops what no three files add up to" test "$(status "${form_fields[@]}" --form "files=@$toobig" "https://$DOMAIN/api/leads")" = 413
check "the rest of the API still takes small requests only" test "$(status --request POST --data-binary "@$megabyte" "https://$DOMAIN/api/e")" = 413
check "the card offers the files" grep -q "/leads/$file_lead/files/$file_id\" download>Схема стойки.pdf" <(admin_get "$ADMIN/leads/$file_lead")
downloaded=$(mktemp)
check "the admin area hands a file out — as a download, whatever is inside" bash -c "grep -qi '^content-disposition: attachment' <<<\"\$1\" && grep -qi '^content-type: application/octet-stream' <<<\"\$1\"" _ \
  "$(admin_get --output "$downloaded" --dump-header - "$ADMIN/leads/$file_lead/files/$file_id")"
check "…byte for byte" cmp -s "$downloaded" "$pdf"
check "nobody else gets it" test "$(status "$ADMIN/leads/$file_lead/files/$file_id")" = 303
check "files are never reachable as pages of the site" test "$(status "https://$DOMAIN/attachments/")" = 404
check "deleting the client's data" test "$(admin_post "/leads/$file_lead/delete" --data-urlencode "csrf=$(csrf)" --data-urlencode "confirm=K-$(printf '%04d' "$file_lead")")" = 303
check "…removes the files too" test "$(find /srv/krokosha/attachments -type f | wc -l)" = 0
sed -i 's/^\( *attachments:\) true /\1 false/' /opt/krokosha/repo/content/site.yaml
check "the content is as it was" test -z "$(runuser -u krokosha -- git -C /opt/krokosha/repo status --porcelain)"
rm -f "$pdf" "$program" "$megabyte" "$toobig" "$downloaded"

echo "Telegram bot"
check "the installer asked Telegram whose token it is" test "$(bot_called getMe)" -ge 1
check "the token is in the settings file only" bash -c "grep -q '^TELEGRAM_BOT_TOKEN=$BOT_TOKEN\$' /etc/krokosha/env && ! grep -rqF '$BOT_TOKEN' '$INSTALL_LOG' /var/log/krokosha 2>/dev/null"
check "…and never in the service's log" bash -c "! journalctl -u krokosha-api.service --no-pager | grep -qF '$BOT_TOKEN'"
webhook_is_set() { [[ $(bot_called setWebhook) -ge 1 ]]; }
check "the bot registered a webhook with Telegram" wait_for 30 webhook_is_set
# The service started more than once while the installer worked: the last registration counts.
webhook=$(python3 - "$BOT_CALLS" <<'PY'
import json, sys
calls = [json.loads(line) for line in open(sys.argv[1], encoding="utf-8")]
last = [call["params"] for call in calls if call["method"] == "setWebhook"][-1]
print(last["url"], last["secret_token"])
PY
)
webhook_url=${webhook% *} webhook_secret=${webhook#* }
check "…under an address nobody can guess, with a secret of its own" grep -qE "^https://$DOMAIN/api/telegram/[0-9a-f]{32} [0-9a-f]{64}$" <<<"$webhook"
owner_code=$(grep -o 'start=i_[a-z2-7]*' "$INSTALL_LOG" | head -n 1 | cut -d= -f2)
check "the installation ends with an invitation for the bot's owner" grep -qE '^i_[a-z2-7]{20}$' <<<"$owner_code"
deliver() { # deliver SECRET JSON → HTTP status of a delivery to the webhook, through nginx
  local secret=$1 update=$2 proof=()
  [[ -z $secret ]] || proof=(--header "X-Telegram-Bot-Api-Secret-Token: $secret")
  "${CURL[@]}" --output /dev/null --write-out '%{http_code}' --header 'Content-Type: application/json' "${proof[@]}" --data "$update" "$webhook_url"
}
start_update() { printf '{"update_id":%s,"message":{"message_id":%s,"date":0,"text":"/start %s","from":{"id":%s,"first_name":"%s","language_code":"ru"},"chat":{"id":%s,"type":"private"}}}' "$1" "$1" "$2" "$3" "$4" "$3"; }
check "a delivery without Telegram's secret is refused" test "$(deliver '' "$(start_update 1 "$owner_code" 7001 Mallory)")" = 403
check "a guessed address is an ordinary 404" test "$(status --request POST "https://$DOMAIN/api/telegram/$(printf '0%.0s' {1..32})")" = 404
check "nobody got in that way" test -z "$(krokosha-cli bot users)"
check "Telegram's own delivery is accepted" test "$(deliver "$webhook_secret" "$(start_update 2 "$owner_code" 7002 Denis)")" = 200
owner_joined() { krokosha-cli bot users | grep -qP '^\d+\towner\tactive\tDenis\t'; }
check "the owner is in — by the one-time invitation" wait_for 20 owner_joined
owner_welcomed() { grep -q 'Доступ открыт, Denis' "$BOT_CALLS"; }
check "…and was told so" wait_for 10 owner_welcomed
check "a second person comes with the same invitation" test "$(deliver "$webhook_secret" "$(start_update 3 "$owner_code" 7003 Mallory)")" = 200
sleep 2
check "…and is not let in" test "$(krokosha-cli bot users | wc -l)" = 1
# Cards of requests. The request of the contact-form test above has been waiting in the queue:
# there was nobody in the bot to show it to. The owner joining sends it on its way.
card_of() { grep -c "sendMessage.*Заявка #$1</b>" "$BOT_CALLS" || true; }
first_card_arrived() { [[ $(card_of K-0001) -ge 1 ]]; }
check "a request that came before anybody joined reaches the owner at once" wait_for 30 first_card_arrived
check "…as a card with buttons" bash -c "grep 'sendMessage' '$BOT_CALLS' | grep 'Заявка #K-0001</b>' | grep -q 'l:reply:1'"
check "spam is not announced" test "$(card_of K-0003)" = 0
fresh=$(lead_files 198.51.100.31 | sed -n 's/.*"id":"K-0*\([0-9]*\)".*/\1/p')
fresh_card_arrived() { [[ $(card_of "K-$(printf '%04d' "$fresh")") -ge 1 ]]; }
check "a new request comes to Telegram within seconds" wait_for 30 fresh_card_arrived
press() { printf '{"update_id":%s,"callback_query":{"id":"cb%s","from":{"id":%s,"first_name":"%s"},"data":"%s","message":{"message_id":1,"chat":{"id":%s,"type":"private"}}}}' "$1" "$1" "$2" "$3" "$4" "$2"; }
check "«take» pressed in Telegram" test "$(deliver "$webhook_secret" "$(press 10 7002 Denis "l:take:$fresh")")" = 200
taken_in_telegram() { [[ $(sql "SELECT CONCAT(status, ' ', assignee) FROM leads WHERE id = $fresh") == "in_progress Denis" ]]; }
check "…takes the request, under the name from Telegram" wait_for 20 taken_in_telegram
card_redrawn() { grep -q 'editMessageText.*В работе · взял(а) Denis' "$BOT_CALLS"; }
check "…and the card is redrawn" wait_for 20 card_redrawn
check "a stranger's press does nothing" bash -c "[[ \$1 == 200 ]] && sleep 2 && grep -q 'answerCallbackQuery.*Нет доступа' '$BOT_CALLS'" _ "$(deliver "$webhook_secret" "$(press 11 7003 Mallory "l:done:$fresh")")"
check "…the request is as it was" test "$(sql "SELECT status FROM leads WHERE id = $fresh")" = in_progress
check "what is done in the admin area shows in Telegram" bash -c "[[ \$1 == 303 ]]" _ "$(admin_post "/leads/$fresh/status" --data-urlencode "csrf=$(csrf)" --data-urlencode 'status=done')"
card_finished() { grep -q "editMessageText.*Заявка #K-$(printf '%04d' "$fresh").*Завершена" "$BOT_CALLS"; }
check "…the card says «done»" wait_for 20 card_finished
check "deleting the client's data" test "$(admin_post "/leads/$fresh/delete" --data-urlencode "csrf=$(csrf)" --data-urlencode "confirm=K-$(printf '%04d' "$fresh")")" = 303
card_wiped() { grep -q "editMessageText.*Заявка #K-$(printf '%04d' "$fresh"): данные клиента удалены" "$BOT_CALLS"; }
check "…wipes what the bot wrote about the request in Telegram" wait_for 30 card_wiped
# The client's side (brief B10.5): the «thank you» page offers to continue in Telegram, and the
# bot relays between the client and the staff.
relay_answer=$(lead_files 198.51.100.41)
relay_lead=$(sed -n 's/.*"id":"K-0*\([0-9]*\)".*/\1/p' <<<"$relay_answer")
relay_number="K-$(printf '%04d' "$relay_lead")"
client_link=$(sed -n 's/.*"telegram_url":"\([^"]*\)".*/\1/p' <<<"$relay_answer")
check "a request is answered with a link into the bot" grep -qE '^https://t\.me/krokosha_ci_bot\?start=c_[A-Za-z0-9_-]{22}$' <<<"$client_link"
message() { printf '{"update_id":%s,"message":{"message_id":%s,"date":0,"text":"%s","from":{"id":%s,"first_name":"%s","language_code":"ru"},"chat":{"id":%s,"type":"private"}}}' "$1" "$1" "$4" "$2" "$3" "$2"; }
said_to() { grep 'sendMessage' "$BOT_CALLS" | grep "\"chat_id\": $1" | grep -c "$2" || true; } # said_to CHAT TEXT
check "the client opens it" test "$(deliver "$webhook_secret" "$(message 20 8001 Ivan "/start ${client_link##*start=}")")" = 200
client_welcomed() { [[ $(said_to 8001 "Заявка #$relay_number у нас") -ge 1 ]]; }
check "…and is told the state of their own request" wait_for 20 client_welcomed
check "somebody else with the same link" test "$(deliver "$webhook_secret" "$(message 21 8002 Mallory "/start ${client_link##*start=}")")" = 200
link_refused() { [[ $(said_to 8002 'уже открыта в другом аккаунте') -ge 1 ]]; }
check "…gets nothing out of it" wait_for 20 link_refused
check "the client writes in Telegram" test "$(deliver "$webhook_secret" "$(message 22 8001 Ivan 'Забыл сказать: стойка уже куплена.')")" = 200
client_message_stored() { [[ $(sql "SELECT COUNT(*) FROM lead_messages WHERE lead_id = $relay_lead AND direction = 'in' AND channel = 'telegram'") == 1 ]]; }
check "…the message lands in the conversation of the request" wait_for 20 client_message_stored
staff_told() { [[ $(said_to 7002 "#$relay_number</b>.*пишет в Telegram") -ge 1 ]]; }
check "…and the staff hears about it" wait_for 30 staff_told
check "an answer from the admin area" test "$(admin_post "/leads/$relay_lead/reply" --data-urlencode "csrf=$(csrf)" --data-urlencode 'text=Отлично, тогда начнём с сети.')" = 303
client_answered() { [[ $(said_to 8001 'Отлично, тогда начнём с сети.') -ge 1 ]]; }
check "…reaches the client in Telegram" wait_for 30 client_answered
check "…and is marked as delivered" test "$(sql "SELECT delivery FROM lead_messages WHERE lead_id = $relay_lead AND direction = 'out'")" = sent
check "the client asks for the staff's lists" test "$(deliver "$webhook_secret" "$(message 23 8001 Ivan '/leads')")" = 200
client_kept_out() { [[ $(said_to 8001 "Заявка #$relay_number, статус") -ge 1 && $(said_to 8001 'Открытые заявки') == 0 ]]; }
check "…and sees only their own request" wait_for 20 client_kept_out
check "/leads for the owner" test "$(deliver "$webhook_secret" "$(message 24 7002 Denis '/leads')")" = 200
owner_list() { [[ $(said_to 7002 'Открытые заявки') -ge 1 ]]; }
check "…lists what is open" wait_for 20 owner_list
check "the webhook's address stays out of the traffic log" bash -c "! grep -q '/api/telegram/' /var/log/krokosha/nginx-access.json.log"
check "CLI: an invitation for a colleague" grep -qE '^/start i_[a-z2-7]{20}$' <(krokosha-cli bot invite 2>/dev/null)
check "the bot's page in the admin area" grep -q '@krokosha_ci_bot' <(admin_get "$ADMIN/bot")

# Answers by mail (brief B10.5), on the real mail server and while the admin session of this test
# is still open. The client's mailbox is on this very server: what the site sends them can be
# read, and they can answer it.
echo "Answers by mail"
mailcheck() { python3 "$SOURCE/deploy/ci/mailcheck.py" "$@"; }
SERVICE_PASSWORD=$(sed -n 's/^MAIL_SERVICE_PASSWORD=//p' /etc/krokosha/env)
CLIENT_PASSWORD='ci: the password of a client'
check "the service reads its mailbox" grep -q "ящик <span class=\"mono\">leads@$DOMAIN</span> на связи" <(admin_get "$ADMIN/status")
check "a mailbox for the client of this test" bash -c "printf '%s\n' '$CLIENT_PASSWORD' | krokosha-mailbox add client@$DOMAIN --password-stdin 2>/dev/null"
# Asked inside the container, not by signing in: failed logins would get this machine banned by
# the mail server's own fail2ban.
client_known() { docker exec krokosha-mail-1 doveadm user "client@$DOMAIN" >/dev/null 2>&1 && docker exec krokosha-mail-1 postmap -q "client@$DOMAIN" texthash:/etc/postfix/vmailbox >/dev/null 2>&1; }
check "…which the mail server learns about by itself" wait_for 120 client_known
mail_lead=$(curl --silent --max-time 30 --header 'Accept: application/json' --header "Host: $DOMAIN" --header 'X-Real-IP: 198.51.100.41' \
  --data-urlencode 'name=Почтовый клиент' --data-urlencode 'contact_method=email' --data-urlencode "contact_value=client@$DOMAIN" \
  --data-urlencode 'direction=devops' --data-urlencode 'description=Нужна настройка почтового сервера для небольшого офиса.' \
  --data-urlencode 'consent=on' --data-urlencode 'lang=ru' http://127.0.0.1:8080/api/leads | grep -oE '"id":"K-[0-9]+"' | grep -oE '[0-9]+' | sed 's/^0*//')
check "a request of a client with a mailbox" test -n "$mail_lead"
mail_number=$(printf 'K-%04d' "${mail_lead:-0}")
reply_to=$(mailcheck header "client@$DOMAIN" "$CLIENT_PASSWORD" "$mail_number" Reply-To)
check "the confirmation asks for answers at the signed address of the request" grep -qE "<leads\+k-[0-9]{4}\.[a-z2-7]{16}@$DOMAIN>" <<<"$reply_to"
signed=$(grep -oE "leads\+k-[0-9]+\.[a-z2-7]{16}@[a-z0-9.-]+" <<<"$reply_to" | head -n 1)
answer_pdf=$(mktemp --suffix=.pdf)
printf '%%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%%%EOF\n' >"$answer_pdf"
check "the client answers the letter, with a file" mailcheck send "client@$DOMAIN" "$CLIENT_PASSWORD" "client@$DOMAIN" "$signed" \
  "$(printf 'Да, два VLAN: офис и гости.\n\nOn Mon, 1 Sep 2026 at 10:04, CI Owner <owner@%s> wrote:\n> ЭТО СТАРОЕ ПИСЬМО\n> его в переписке быть не должно' "$DOMAIN")" "$answer_pdf"
letters_in() { [[ $(sql "SELECT COUNT(*) FROM lead_messages WHERE lead_id = $mail_lead AND channel = 'email' AND direction = 'in'") == "$1" ]]; }
check "…and the answer joins the conversation of the request" wait_for 90 letters_in 1
card=$(admin_get "$ADMIN/leads/$mail_lead")
check "…the client's own words" grep -q 'Да, два VLAN: офис и гости.' <<<"$card"
check "…without the conversation quoted below them" bash -c "! grep -q 'СТАРОЕ ПИСЬМО' <<<\"\$1\"" _ "$card"
check "…with the file, inspected like the files of the form" test "$(sql "SELECT CONCAT(kind, ' ', size) FROM lead_attachments WHERE lead_id = $mail_lead")" = "pdf $(stat -c %s "$answer_pdf")"
check "…and the letter left the mailbox" mailcheck absent "leads@$DOMAIN" "$SERVICE_PASSWORD" 'VLAN'
staff_heard() { [[ $(said_to 7002 'пишет письмом') -ge 1 ]]; }
check "the staff hears about the letter in Telegram" wait_for 30 staff_heard
check "a known client writes to the bare address" mailcheck send "client@$DOMAIN" "$CLIENT_PASSWORD" "client@$DOMAIN" "leads@$DOMAIN" 'Забыл спросить про сроки.'
check "…the letter goes to their latest request" wait_for 90 letters_in 2
# Anybody can type a number into an address; the signature is what opens a request.
check "a letter with a made-up signature" mailcheck send "owner@$DOMAIN" "$MAILBOX_PASSWORD" "owner@$DOMAIN" "leads+k-0001.aaaaaaaaaaaaaaaa@$DOMAIN" 'Письмо с подделанным номером заявки. FORGED-NUMBER'
letter_waits() { grep -q 'подделанным номером' <(admin_get "$ADMIN/inbox"); }
check "…waits for a person on the «Входящие» screen" wait_for 90 letter_waits
check "…and is in nobody's conversation" test "$(sql "SELECT COUNT(*) FROM lead_messages WHERE lead_id = 1 AND channel = 'email' AND direction = 'in'")" = 0
waiting_id=$(sql "SELECT MAX(id) FROM inbox_letters WHERE outcome = 'unmatched'")
check "a person puts it into a request" test "$(admin_post "/inbox/$waiting_id/attach" --data-urlencode "csrf=$(csrf)" --data-urlencode "lead=$mail_number")" = 303
check "…where it then is" grep -q 'подделанным номером' <(admin_get "$ADMIN/leads/$mail_lead")
check "…and nowhere else" mailcheck absent "leads@$DOMAIN" "$SERVICE_PASSWORD" 'FORGED-NUMBER'
# The first request of this test came from an address at a domain that does not exist: the mail
# server returned the confirmation, and the report found its request by the signed address.
came_back() { [[ $(sql "SELECT COUNT(*) FROM lead_events WHERE lead_id = 1 AND action = 'undelivered'") -ge 1 ]]; }
check "a letter that could not be delivered is reported in its request" wait_for 120 came_back
rm -f "$answer_pdf"

check "a form without the CSRF token is refused" test "$(admin_post /account/totp/begin)" = 403
check "a form posted by another site is refused" test "$(admin_post /account/totp/begin --header 'Origin: https://evil.example' --data-urlencode "csrf=$(csrf)")" = 403
check "the genuine form works" test "$(admin_post /account/totp/begin --header "Origin: https://$DOMAIN" --data-urlencode "csrf=$(csrf)")" = 303
check "the QR code for the authenticator is a PNG" grep -qi image/png <(admin_get --output /dev/null --dump-header - "$ADMIN/account/totp/qr.png")
check "signing out ends the session" test "$(admin_post /logout --data-urlencode "csrf=$(csrf)")" = 303
check "the old cookie is worthless afterwards" test "$(header "$ADMIN/" location)" = "$ADMIN_PATH/login"

check "krokosha-cli is on the PATH" bash -c "command -v krokosha-cli >/dev/null"
check "CLI: a second administrator" bash -c "printf '%s\n' 'another long password' | krokosha-cli admin create Second --password-stdin >/dev/null && krokosha-cli admin list | grep -q '^second '"
check "CLI: a short password is refused" bash -c "! printf 'short\n' | krokosha-cli admin create third --password-stdin >/dev/null 2>&1"
check "CLI: an account can be blocked" krokosha-cli admin disable second
check "a blocked account cannot sign in" test "$(admin_post /login --data-urlencode login=second --data-urlencode 'password=another long password')" = 401
check "passwords are stored as argon2id hashes" test "$(sql "SELECT COUNT(*) FROM admin_users WHERE password_hash LIKE '_argon2id_v=19_m=65536,t=3,p=2_%'")" = 2
check "sign-ins and failures are in the audit log" test "$(sql "SELECT COUNT(DISTINCT action) FROM audit_log WHERE action IN ('admin.login', 'admin.login-failed', 'admin.create')")" = 3

# Two independent brakes. The API's own: ten attempts per login in 15 minutes (asked directly,
# past nginx). And nginx's: the login form answers a flood with 429 before the API sees it.
api_login() {
  curl --silent --output /dev/null --write-out '%{http_code}' --max-time 20 --user-agent "$BROWSER" \
    --request POST "http://127.0.0.1:8080$ADMIN_PATH/login" --data-urlencode login=ci-admin --data-urlencode "password=$1"
}
last=0
for _ in $(seq 1 12); do last=$(api_login 'guess guess guess'); done
check "the API cuts off password guessing" test "$last" = 429
check "even the right password waits then" test "$(api_login "$ADMIN_PASSWORD")" = 429
for _ in $(seq 1 25); do last=$(status "$ADMIN/login"); done
check "nginx limits the login form on its own" test "$last" = 429
check "the limit does not touch the site itself" test "$(status "https://$DOMAIN/")" = 200
check "fail2ban watches the admin area" bash -c "fail2ban-client status krokosha-admin | grep -q nginx-admin.json.log"
check "fail2ban recognises the failed logins" bash -c "fail2ban-regex /var/log/krokosha/nginx-admin.json.log /etc/fail2ban/filter.d/krokosha-admin.conf | grep -qE '^Failregex: [1-9][0-9]* total'"

echo "Mail server"
check "the mail server runs and says it is healthy" test "$(docker inspect --format '{{.State.Health.Status}}' krokosha-mail-1)" = healthy
check "its letters, state and keys live in the data root" test -d /srv/krokosha/mail/data -a -d /srv/krokosha/mail/state -a -s /srv/krokosha/mail/config/postfix-accounts.cf
check "mailboxes: the site's own, the owner's and the test's client" test "$(krokosha-mailbox list | sort | paste -sd ' ')" = "client@$DOMAIN leads@$DOMAIN owner@$DOMAIN"
check "passwords are kept as hashes" bash -c "! grep -qF -e '$MAILBOX_PASSWORD' -e '$SERVICE_PASSWORD' /srv/krokosha/mail/config/postfix-accounts.cf && [[ \$(grep -c '|{SHA512-CRYPT}\\\$6\\\$' /srv/krokosha/mail/config/postfix-accounts.cf) == 3 ]]"
check "the site is told how to send and how to read" bash -c "grep -q '^SMTP_ADDR=127.0.0.1:587\$' /etc/krokosha/env && grep -q '^IMAP_ADDR=127.0.0.1:993\$' /etc/krokosha/env && grep -q '^MAIL_FROM=CI Owner <owner@$DOMAIN>\$' /etc/krokosha/env && grep -q '^MAIL_INBOX=leads@$DOMAIN\$' /etc/krokosha/env"
check "a DKIM key was made" test -s "/srv/krokosha/mail/config/rspamd/dkim/rsa-2048-mail-$DOMAIN.private.txt"
check "the installation ends with the DNS records to enter" bash -c "grep -q 'MX .*10 mail.$DOMAIN\\.' '$INSTALL_LOG' && grep -q 'v=spf1 mx -all' '$INSTALL_LOG' && grep -q 'mail._domainkey .*v=DKIM1' '$INSTALL_LOG' && grep -q '_dmarc' '$INSTALL_LOG' && grep -q 'PTR ' '$INSTALL_LOG'"
check "…and keeps them in a file" grep -q 'mail._domainkey' /srv/krokosha/mail/DNS.txt
check "the certificate names the mail server too" bash -c "openssl s_client -connect 127.0.0.1:993 -servername mail.$DOMAIN </dev/null 2>/dev/null | openssl x509 -noout -ext subjectAltName | grep -q 'DNS:mail.$DOMAIN'"
check "the owner's mail program can sign in" mailcheck login "owner@$DOMAIN" "$MAILBOX_PASSWORD"
check "…with the right password only" mailcheck nologin "owner@$DOMAIN" 'not the password at all'
# The request of the contact-form test was announced by mail: through the site's own server,
# signed, into the owner's mailbox.
check "the letter about a request is in the owner's mailbox" mailcheck find "owner@$DOMAIN" "$MAILBOX_PASSWORD" 'MikroTik'
letter_about_request() { docker exec krokosha-mail-1 sh -c "grep -rl 'X-Krokosha-Lead: K-0001' /var/mail/$DOMAIN/owner/ | head -n 1"; }
check "…sent in the owner's name, under the envelope of the service mailbox" bash -c "docker exec krokosha-mail-1 cat \"\$1\" | grep -qi '^Return-Path: <leads@$DOMAIN>' && docker exec krokosha-mail-1 cat \"\$1\" | grep -q '^From: .*owner@$DOMAIN'" _ "$(letter_about_request)"
check "…and signed with the domain's DKIM key" bash -c "docker exec krokosha-mail-1 cat \"\$1\" | tr -d '\r\n\t ' | grep -qi 'DKIM-Signature:[^:]*d=$DOMAIN;'" _ "$(letter_about_request)"
check "an account cannot send under somebody else's address" mailcheck spoof "leads@$DOMAIN" "$SERVICE_PASSWORD" "owner@$DOMAIN" "owner@$DOMAIN"
check "strangers cannot relay through the server" mailcheck relay stranger@example.org victim@example.net
check "an answer to a request's address reaches the service mailbox" mailcheck send "owner@$DOMAIN" "$MAILBOX_PASSWORD" "owner@$DOMAIN" "leads+k-0001.aaaaaaaaaaaaaaaa@$DOMAIN" 'an answer by mail, as a client would send it'
check "…whatever follows the plus sign" mailcheck find "leads@$DOMAIN" "$SERVICE_PASSWORD" 'an answer by mail'
check "mail programs find their settings on the site" grep -q "<hostname>mail.$DOMAIN</hostname>" <(body "https://$DOMAIN/.well-known/autoconfig/mail/config-v1.1.xml")
check "a second mailbox" bash -c "printf '%s\n' 'another long password' | krokosha-mailbox add Second@$DOMAIN --password-stdin 2>/dev/null && krokosha-mailbox list | grep -qx 'second@$DOMAIN'"
check "…a short password is refused" bash -c "! printf 'short\n' | krokosha-mailbox add third@$DOMAIN --password-stdin 2>/dev/null"
check "…and the site's own mailbox cannot be removed" bash -c "! krokosha-mailbox del leads@$DOMAIN 2>/dev/null"
check "…not even as «lead.@»: addresses are strings, not patterns" bash -c "! krokosha-mailbox del lead.@$DOMAIN 2>/dev/null && krokosha-mailbox list | grep -qx 'leads@$DOMAIN'"
check "the mail ports are open in the firewall" bash -c "ufw status | grep -qE '^25/tcp +ALLOW' && ufw status | grep -qE '^993/tcp +ALLOW'"

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

echo "Backups and the certificate watch"
check "the nightly backup and the daily look at the certificates are scheduled" bash -c "systemctl is-enabled --quiet krokosha-backup.timer && systemctl is-enabled --quiet krokosha-certwatch.timer"
# The site user may put a link in the place of any name in status/ — of the report, of the
# temporary file root used to write it under a fixed name: root must not write where they point.
runuser -u krokosha -- ln -sfn /etc/krokosha-ci-canary /var/lib/krokosha/status/backup.json.tmp
runuser -u krokosha -- ln -sfn /etc/krokosha-ci-canary /var/lib/krokosha/status/backup.json
check "a backup by hand" bash -c "'$SOURCE/deploy/backup.sh' >/dev/null 2>&1"
check "…writes its report as the site user, not through a link the site user put there" bash -c "[[ ! -e /etc/krokosha-ci-canary && ! -L /var/lib/krokosha/status/backup.json && \$(stat -c %U /var/lib/krokosha/status/backup.json) == krokosha ]]"
rm -f /var/lib/krokosha/status/backup.json.tmp
first_backup=/srv/krokosha/backups/$(readlink /srv/krokosha/backups/latest)
check "…has the database, the settings, the mail and the files of requests" bash -c "gzip -t '$first_backup/mysql.sql.gz' && zcat '$first_backup/mysql.sql.gz' | grep -q 'CREATE TABLE .leads.' && tar -tzf '$first_backup/config.tar.gz' | grep -qx config/env && test -s '$first_backup/mail/config/postfix-accounts.cf' && test -d '$first_backup/attachments' && test -s '$first_backup/MANIFEST'"
check "…with the tables of the map but not their rows: they are built again every night" bash -c "zcat '$first_backup/mysql.sql.gz' | grep -q 'CREATE TABLE .netmap_link.' && ! zcat '$first_backup/mysql.sql.gz' | grep -q 'INSERT INTO .netmap_'"
check "…for root's eyes only: the secrets are in it" test "$(stat -c '%U %a' /srv/krokosha/backups) $(stat -c '%a' "$first_backup/config.tar.gz")" = "root 700 600"
check "…and reports to the status screen" grep -q '"ok":true' /var/lib/krokosha/status/backup.json
sleep 1
check "a second backup" bash -c "'$SOURCE/deploy/backup.sh' >/dev/null 2>&1"
second_backup=/srv/krokosha/backups/$(readlink /srv/krokosha/backups/latest)
a_letter=$(cd "$first_backup" && find mail/data -type f -path '*/cur/*' -o -type f -path '*/new/*' | head -n 1)
check "…shares the letters that did not change with the first: a month of backups takes the room of one" bash -c "[[ -n '$a_letter' && '$first_backup' != '$second_backup' && \$(stat -c %i '$first_backup/$a_letter') == \$(stat -c %i '$second_backup/$a_letter') ]]"
check "…without being the same file as the live letter" bash -c "[[ \$(stat -c %i '/srv/krokosha/$a_letter') != \$(stat -c %i '$second_backup/$a_letter') ]]"
check "old backups go: --keep-daily 1 leaves one" bash -c "sleep 1; '$SOURCE/deploy/backup.sh' --keep-daily 1 --keep-weekly 0 >/dev/null 2>&1 && [[ \$(find /srv/krokosha/backups -mindepth 1 -maxdepth 1 -type d | wc -l) == 1 ]]"
check "the certificate watch finds what the site and the mail server really serve" bash -c "'$SOURCE/deploy/bin/krokosha-certwatch' >/dev/null 2>&1 && grep -q '\"name\":\"site\",\"host\":\"$DOMAIN\",\"days_left\":[0-9]' /var/lib/krokosha/status/certwatch.json && grep -q '\"name\":\"mail\",\"host\":\"mail.$DOMAIN\",\"days_left\":[0-9]' /var/lib/krokosha/status/certwatch.json && grep -q '\"ok\":true' /var/lib/krokosha/status/certwatch.json"
# A certificate «about to expire» that nothing renews (this one is self-signed): the watch
# fails, says so on the status screen, and the owner hears about it — once.
check "a certificate that expires and does not renew makes the watch fail" bash -c "! CERTWATCH_RENEW_BELOW_DAYS=100000 '$SOURCE/deploy/bin/krokosha-certwatch' >/dev/null 2>&1 && grep -q '\"ok\":false' /var/lib/krokosha/status/certwatch.json"
CERTWATCH_RENEW_BELOW_DAYS=100000 "$SOURCE/deploy/bin/krokosha-certwatch" >/dev/null 2>&1 || true
check "…the owner is told by mail and in Telegram, once a day" test "$(sql "SELECT COUNT(*) FROM outbox WHERE kind = 'system.alert'")" = 2
check "…the letter arrives" mailcheck find "owner@$DOMAIN" "$MAILBOX_PASSWORD" 'certbot renew --dry-run'
"$SOURCE/deploy/bin/krokosha-certwatch" >/dev/null 2>&1 || true

echo "::group::Second run: must change nothing and break nothing"
before=$(readlink -f /var/www/krokosha/current)
secrets_before=$(grep -E '^(MYSQL_PASSWORD|REDIS_PASSWORD|ADMIN_PATH|APP_SECRET)=' /etc/krokosha/env | sha256sum)
mail_before=$(sha256sum /srv/krokosha/mail/config/postfix-accounts.cf "/srv/krokosha/mail/config/rspamd/dkim/rsa-2048-mail-$DOMAIN.private.txt" | sha256sum)
"$SOURCE/deploy/install.sh" --from-env --yes
echo "::endgroup::"
echo "Idempotency"
check "site still answers" test "$(status "https://$DOMAIN/")" = 200
check "settings survived" grep -q "^FIREWALL_ALLOW=8443/tcp" /etc/krokosha/env
check "API still answers" grep -q '"status":"ok"' <(curl -s --max-time 5 http://127.0.0.1:8080/api/health)
check "the skipped DNS check is remembered" grep -q "^SKIP_DNS_CHECK=yes" /etc/krokosha/env
check "a new release was published" test "$(readlink -f /var/www/krokosha/current)" != "$before"
check "generated secrets were kept" test "$(grep -E '^(MYSQL_PASSWORD|REDIS_PASSWORD|ADMIN_PATH|APP_SECRET)=' /etc/krokosha/env | sha256sum)" = "$secrets_before"
check "administrators survived" test "$(krokosha-cli admin list | wc -l)" = 2
check "the mail server, its mailboxes and its key survived" bash -c "[[ \$(docker inspect --format '{{.State.Health.Status}}' krokosha-mail-1) == healthy ]] && [[ \$(sha256sum /srv/krokosha/mail/config/postfix-accounts.cf \"/srv/krokosha/mail/config/rspamd/dkim/rsa-2048-mail-$DOMAIN.private.txt\" | sha256sum) == '$mail_before' ]]"
check "…and rspamd can still read that key, the mail server running on" docker exec krokosha-mail-1 su _rspamd -s /bin/sh -c "cat /tmp/docker-mailserver/rspamd/dkim/rsa-2048-mail-$DOMAIN.private.txt >/dev/null"
check "the bot and its owner survived" bash -c "grep -q '^TELEGRAM_BOT_TOKEN=.' /etc/krokosha/env && krokosha-cli bot users | grep -q 'owner'"
check "the admin area still answers" test "$(status "$ADMIN/login")" = 200
check "firewall has no duplicate rules" test "$(ufw status | grep -cE '^443/tcp +ALLOW')" = 1
check "GeoLite2 has come: the API reads it now, and DB-IP is gone" bash -c "[[ \$(sed -n 's/^GEOIP_DB=//p' /etc/krokosha/env) == /var/lib/GeoIP/GeoLite2-City.mmdb && ! -e /var/lib/GeoIP/dbip-city-lite.mmdb ]] && ! systemctl is-enabled --quiet krokosha-dbip.timer"
check "…a page view gets its place from GeoLite2" geo_recorded 00112233aabbcc78 'UA Kyiv'
check "…the status screen names it" geo_page_says /status 'GeoLite2-City от 10.09.2026'
check "…and the admin area credits MaxMind" geo_page_says / 'GeoLite2 data created by MaxMind'
check "…instead of DB-IP" bash -c "! grep -q 'IP Geolocation by DB-IP' <<<\"\$1\"" _ "$("${CURL[@]}" --cookie "$GEO_JAR" --user-agent "$BROWSER" "$ADMIN/")"

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

# The worst day: the server is gone. A backup that was copied elsewhere, a new installation, restore.sh.
echo "::group::A lost server: backup elsewhere, a new installation, restore"
elsewhere=$(mktemp -d)
"$SOURCE/deploy/backup.sh" --to "$elsewhere"
saved=$elsewhere/$(readlink "$elsewhere/latest")
facts() { sql "SELECT CONCAT((SELECT COUNT(*) FROM leads), ' requests, ', (SELECT COUNT(*) FROM lead_messages), ' messages, ', (SELECT COUNT(*) FROM lead_attachments), ' files, ', (SELECT COUNT(*) FROM admin_users), ' administrators, ', (SELECT COUNT(*) FROM bot_users), ' in the bot, ', (SELECT COUNT(*) FROM analytics_pageviews), ' page views')"; }
facts_before=$(facts)
kept_before=$(grep -E '^(APP_SECRET|ADMIN_PATH|MAIL_SERVICE_PASSWORD)=' /etc/krokosha/env | sha256sum)
mail_before=$(sha256sum /srv/krokosha/mail/config/postfix-accounts.cf "/srv/krokosha/mail/config/rspamd/dkim/rsa-2048-mail-$DOMAIN.private.txt" | sha256sum)
files_before=$(find /srv/krokosha/attachments -type f | wc -l)
webhooks_before=$(bot_called setWebhook)
/opt/krokosha/repo/deploy/uninstall.sh --yes --purge
# DB-IP has published this month's file by now.
dbip_publish "$THIS_MONTH" 127.0.0.0/8=CZ:Prague
install_site
database_password=$(sed -n 's/^MYSQL_PASSWORD=//p' /etc/krokosha/env)
echo "::endgroup::"
echo "Restore"
check "a new installation knows nothing of the old one" test "$(sql 'SELECT COUNT(*) FROM leads')" = 0
check "…has no MaxMind key and no use for geoipupdate" bash -c "[[ ! -e /etc/GeoIP.conf ]] && ! systemctl is-enabled --quiet krokosha-geoipupdate.timer"
check "…and takes DB-IP City Lite of this month instead" dbip_in_use "$THIS_MONTH"
check "…and has secrets of its own" test "$(grep -E '^(APP_SECRET|ADMIN_PATH|MAIL_SERVICE_PASSWORD)=' /etc/krokosha/env | sha256sum)" != "$kept_before"
check "restore.sh puts the backup back" bash -c "'$SOURCE/deploy/restore.sh' --from '$saved' --yes >'$elsewhere/restore.log' 2>&1 || { tail -n 30 '$elsewhere/restore.log'; exit 1; }"
check "requests, conversations, files, administrators, the bot's people and the statistics are back" test "$(facts)" = "$facts_before"
check "…with the secret that signs addresses and links, the path of the admin area, the password of the service mailbox" test "$(grep -E '^(APP_SECRET|ADMIN_PATH|MAIL_SERVICE_PASSWORD)=' /etc/krokosha/env | sha256sum)" = "$kept_before"
check "…while the database password stays the new installation's own" test "$(sed -n 's/^MYSQL_PASSWORD=//p' /etc/krokosha/env)" = "$database_password"
webhook_again() { [[ $(bot_called setWebhook) -gt $webhooks_before ]]; }
check "the bot is back: the restored token was checked with Telegram, the webhook registered anew" wait_for 30 webhook_again
check "the MaxMind key is back, /etc/GeoIP.conf is written again" bash -c "grep -q '^LicenseKey $MAXMIND_KEY\$' /etc/GeoIP.conf && systemctl is-enabled --quiet krokosha-geoipupdate.timer"
check "…and until it brings GeoLite2, DB-IP stays" dbip_in_use "$THIS_MONTH"
check "the mailboxes and the DKIM key are back: nothing to change in DNS" test "$(sha256sum /srv/krokosha/mail/config/postfix-accounts.cf "/srv/krokosha/mail/config/rspamd/dkim/rsa-2048-mail-$DOMAIN.private.txt" | sha256sum)" = "$mail_before"
check "the files of requests are back, for the service's eyes only" test "$(find /srv/krokosha/attachments -type f | wc -l) $(find /srv/krokosha/attachments -type f ! -perm 600 | wc -l) $(stat -c '%U' /srv/krokosha/attachments)" = "$files_before 0 krokosha"
check "the API is up on the restored data" grep -q '"mysql":"ok"' <(curl -s --max-time 5 http://127.0.0.1:8080/api/health)
check "the administrator signs in with the old password" test "$(admin_post /login --data-urlencode login=ci-admin --data-urlencode "password=$ADMIN_PASSWORD")" = 303
check "…and finds the requests" grep -q '#K-0001' <(admin_get "$ADMIN/leads?status=all")
check "the mail server is healthy, and the client's mailbox opens with its old password" bash -c "[[ \$(docker inspect --format '{{.State.Health.Status}}' krokosha-mail-1) == healthy ]] && python3 '$SOURCE/deploy/ci/mailcheck.py' login 'client@$DOMAIN' '$CLIENT_PASSWORD'"
mailbox_read() { grep -q 'на связи' <(admin_get "$ADMIN/status"); }
check "the service reads its mailbox again" wait_for 60 mailbox_read
rm -rf "$elsewhere"

# The site gets another domain (deploy/README.md, «Смена домена»): a new --domain is all it takes.
# The old name works on while it points here — its pages redirect, its addresses get mail — and is
# let go with --old-domain none (or by itself once it points elsewhere; this test skips DNS).
NEW_DOMAIN=moved.krokosha.test
CURL+=(--resolve "$NEW_DOMAIN:443:127.0.0.1" --resolve "$NEW_DOMAIN:80:127.0.0.1" --resolve "www.$NEW_DOMAIN:443:127.0.0.1")
letters_before=$(mailcheck count "owner@$DOMAIN" "$MAILBOX_PASSWORD")
webhooks_before=$(bot_called setWebhook)
echo "::group::A new domain"
"$SOURCE/deploy/install.sh" --from-env --yes --domain "$NEW_DOMAIN" 2>&1 | tee "$INSTALL_LOG"
echo "::endgroup::"
echo "A new domain"
check "the site answers under the new name" test "$(status "https://$NEW_DOMAIN/")" = 200
check "…and calls itself by it" grep -q "rel=\"canonical\" href=\"https://$NEW_DOMAIN/\"" <(body "https://$NEW_DOMAIN/")
check "the name it had is remembered as the old one" grep -qx "OLD_DOMAIN=$DOMAIN" /etc/krokosha/env
check "a page at the old name redirects to the same page at the new one" test "$(header "https://$DOMAIN/uk/map/?from=1.1.1.1" location)" = "https://$NEW_DOMAIN/uk/map/?from=1.1.1.1"
check "…over plain HTTP as well" test "$(header "http://$DOMAIN/ru/" location)" = "https://$NEW_DOMAIN/ru/"
check "…and from www. and mail. of the old name" test "$(header "https://www.$DOMAIN/" location) $(curl -s -o /dev/null -w '%{redirect_url}' --max-time 5 -H "Host: mail.$DOMAIN" http://127.0.0.1/x)" = "https://$NEW_DOMAIN/ https://$NEW_DOMAIN/x"
install -D -m 0644 /dev/null "$acme_probe" && echo ci-acme >"$acme_probe"
for name in "$NEW_DOMAIN" "mail.$NEW_DOMAIN" "$DOMAIN" "mail.$DOMAIN"; do
  check "Let's Encrypt reaches its challenge under $name" test "$(curl -s --max-time 5 -H "Host: $name" http://127.0.0.1/.well-known/acme-challenge/ci-probe)" = ci-acme
done
rm -f "$acme_probe"
check "the mailboxes moved to the new domain" test "$(krokosha-mailbox list | sort | paste -sd ' ')" = "client@$NEW_DOMAIN leads@$NEW_DOMAIN owner@$NEW_DOMAIN second@$NEW_DOMAIN"
check "…each with its password" mailcheck login "owner@$NEW_DOMAIN" "$MAILBOX_PASSWORD"
check "…and its letters" test "$(mailcheck count "owner@$NEW_DOMAIN" "$MAILBOX_PASSWORD")" -ge "$letters_before"
check "…of which there were some" test "$letters_before" -ge 1
check "the site writes in the owner's name at the new domain and reads its mailbox there" bash -c "grep -q '^MAIL_FROM=CI Owner <owner@$NEW_DOMAIN>\$' /etc/krokosha/env && grep -q '^MAILBOX=owner@$NEW_DOMAIN\$' /etc/krokosha/env && grep -q '^MAIL_INBOX=leads@$NEW_DOMAIN\$' /etc/krokosha/env"
check "a letter to an old address" mailcheck send "client@$NEW_DOMAIN" "$CLIENT_PASSWORD" "client@$NEW_DOMAIN" "owner@$DOMAIN" 'Written to the old address. OLD-NAME-LETTER'
old_address_works() { mailcheck find "owner@$NEW_DOMAIN" "$MAILBOX_PASSWORD" OLD-NAME-LETTER; }
check "…arrives in the moved mailbox" wait_for 60 old_address_works
check "an answer to a request's address at the old name reaches the service mailbox" mailcheck send "owner@$NEW_DOMAIN" "$MAILBOX_PASSWORD" "owner@$NEW_DOMAIN" "leads+k-0001.aaaaaaaaaaaaaaaa@$DOMAIN" 'Answered to the old name. OLD-NAME-ANSWER'
old_answer_arrived() { mailcheck find "leads@$NEW_DOMAIN" "$SERVICE_PASSWORD" OLD-NAME-ANSWER; }
check "…whatever follows the plus sign" wait_for 90 old_answer_arrived
check "the new domain has a DKIM key, and letters of both names are signed" bash -c "test -s /srv/krokosha/mail/config/rspamd/dkim/rsa-2048-mail-$NEW_DOMAIN.private.txt && grep -q '^    $NEW_DOMAIN {' /srv/krokosha/mail/config/rspamd/override.d/dkim_signing.conf && grep -q '^    $DOMAIN {' /srv/krokosha/mail/config/rspamd/override.d/dkim_signing.conf"
check "a letter from the new domain" mailcheck send "owner@$NEW_DOMAIN" "$MAILBOX_PASSWORD" "owner@$NEW_DOMAIN" "owner@$NEW_DOMAIN" 'Signed by the new domain. NEW-NAME-SIGNED'
new_letter() { docker exec krokosha-mail-1 sh -c "grep -rl 'NEW-NAME-SIGNED' /var/mail/$NEW_DOMAIN/owner/ | head -n 1"; }
signed_by_new() { [[ -n $(new_letter) ]] && docker exec krokosha-mail-1 cat "$(new_letter)" | tr -d '\r\n\t ' | grep -qi "DKIM-Signature:[^:]*d=$NEW_DOMAIN;"; }
check "…is signed with its key" wait_for 60 signed_by_new
check "the mail server calls itself by the new name" test "$(docker inspect --format '{{.Config.Hostname}}' krokosha-mail-1)" = "mail.$NEW_DOMAIN"
check "…with a certificate for that name" bash -c "openssl s_client -connect 127.0.0.1:993 -servername mail.$NEW_DOMAIN </dev/null 2>/dev/null | openssl x509 -noout -ext subjectAltName | grep -q 'DNS:mail.$NEW_DOMAIN'"
check "the DNS records to enter are the new domain's" grep -q "MX .*10 mail.$NEW_DOMAIN\\." /srv/krokosha/mail/DNS.txt
webhook_moved() { [[ $(bot_called setWebhook) -gt $webhooks_before ]] && grep 'setWebhook' "$BOT_CALLS" | tail -n 1 | grep -q "https://$NEW_DOMAIN/api/telegram/"; }
check "the bot's webhook moved to the new name" wait_for 30 webhook_moved
check "the admin area is there, under the same secret path" grep -q 'name="password"' <(body "https://$NEW_DOMAIN$ADMIN_PATH/login")
check "the engines were told about the pages under the new name" grep -q "\"host\": \"$NEW_DOMAIN\"" "$INDEXNOW_CALLS"
echo "::group::The old name is let go"
"$SOURCE/deploy/install.sh" --from-env --yes --old-domain none 2>&1 | tee "$INSTALL_LOG"
echo "::endgroup::"
echo "The old name let go"
check "it is forgotten" grep -qx 'OLD_DOMAIN=' /etc/krokosha/env
check "…nothing answers under it" bash -c "! curl -s --max-time 5 -o /dev/null -H 'Host: $DOMAIN' http://127.0.0.1/"
check "…and its addresses get no mail" bash -c "! grep -q '@$DOMAIN ' /srv/krokosha/mail/config/postfix-virtual.cf"
check "…while the site serves on under its name" test "$(status "https://$NEW_DOMAIN/")" = 200
check "…and the mailboxes stay where they moved" test "$(krokosha-mailbox list | sort | paste -sd ' ')" = "client@$NEW_DOMAIN leads@$NEW_DOMAIN owner@$NEW_DOMAIN second@$NEW_DOMAIN"

echo "::group::Uninstall"
/opt/krokosha/repo/deploy/uninstall.sh --yes --purge
echo "::endgroup::"
check "files are gone" bash -c "[[ ! -e /opt/krokosha && ! -e /var/www/krokosha && ! -e /etc/krokosha && ! -e /srv/krokosha ]]"
check "the MaxMind key and both databases are gone with them" bash -c "[[ ! -e /etc/GeoIP.conf && ! -e /var/lib/GeoIP/GeoLite2-City.mmdb && ! -e /var/lib/GeoIP/dbip-city-lite.mmdb && ! -e /var/lib/GeoIP/dbip-city-lite.mmdb.month ]]"
check "…and so is the DB-IP timer" bash -c "! systemctl cat krokosha-dbip.timer >/dev/null 2>&1"
check "the map's timer is gone" bash -c "! systemctl cat krokosha-netmap.timer >/dev/null 2>&1"
check "containers are gone" bash -c "! docker ps -a --format '{{.Names}}' | grep -q '^krokosha-'"
check "API unit is gone" bash -c "! systemctl cat krokosha-api.service >/dev/null 2>&1"
check "the rebuild unit is gone" bash -c "! systemctl cat krokosha-rebuild.path >/dev/null 2>&1"
check "the CLI link and the fail2ban filter are gone" bash -c "[[ ! -e /usr/local/bin/krokosha-cli && ! -L /usr/local/bin/krokosha-cli && ! -e /etc/fail2ban/filter.d/krokosha-admin.conf ]]"
check "fail2ban still runs" systemctl is-active --quiet fail2ban
check "user is gone" bash -c "! id krokosha >/dev/null 2>&1"
check "nginx configuration is still valid" nginx -t

if ((failures > 0)); then
  printf '\n%d check(s) failed\n' "$failures"
  journalctl -u krokosha-sync.service -n 60 --no-pager || true
  exit 1
fi
printf '\nAll checks passed\n'
