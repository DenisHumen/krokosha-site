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

install_site() {
  "$SOURCE/deploy/install.sh" --domain "$DOMAIN" --email ci@example.com \
    --repo "$SOURCE" --branch ci-test --tls selfsigned --skip-dns-check --yes \
    --admin-path "$ADMIN_PATH" --admin-login ci-admin --admin-password-file "$ADMIN_PASSWORD_FILE" "$@"
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
check "notifications are queued in the same transaction" test "$(sql "SELECT COUNT(*) FROM outbox WHERE lead_id = 1 AND status = 'pending'")" = 3
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
before_rebuild=$(readlink -f /var/www/krokosha/current)
check "the «rebuild now» button is accepted" test "$(admin_post /status/rebuild --data-urlencode "csrf=$(csrf)")" = 303
site_was_rebuilt() { [[ $(readlink -f /var/www/krokosha/current) != "$before_rebuild" && ! -e /var/lib/krokosha/requests/rebuild ]]; }
check "…and a new release is published within two minutes" wait_for 120 site_was_rebuilt
check "the button is in the audit log" test "$(sql "SELECT COUNT(*) FROM audit_log WHERE action = 'admin.rebuild'")" = 1
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
secrets_before=$(grep -E '^(MYSQL_PASSWORD|REDIS_PASSWORD|ADMIN_PATH|APP_SECRET)=' /etc/krokosha/env | sha256sum)
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
check "the admin area still answers" test "$(status "$ADMIN/login")" = 200
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
