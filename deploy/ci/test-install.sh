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

echo "::group::First installation"
install_site --allow 8443/tcp --telegram-token-file "$BOT_TOKEN_FILE" --telegram-api http://127.0.0.1:8088 2>&1 | tee "$INSTALL_LOG"
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
check "…waits in the outbox until the mail server is set up" test "$(sql "SELECT CONCAT(l.status, ' ', o.status) FROM leads l JOIN outbox o ON o.lead_id = l.id AND o.kind = 'lead.reply' WHERE l.id = 1")" = "waiting_client pending"
check "a note for colleagues" test "$(admin_post /leads/1/note --data-urlencode "csrf=$(csrf)" --data-urlencode 'text=Клиент из теста установки.')" = 303
check "the status screen shows the queue of notifications" grep -q 'в очереди: ' <(admin_get "$ADMIN/status")
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
check "the bot and its owner survived" bash -c "grep -q '^TELEGRAM_BOT_TOKEN=.' /etc/krokosha/env && krokosha-cli bot users | grep -q 'owner'"
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
