// Command demolog prints a made-up nginx access log (format krokosha_json) for the last two weeks:
// people, crawlers and scanners. Used by dev/run-local.sh to fill the «server traffic» screen.
//
//	go run ./dev/demolog >access.json.log
package main

import (
	"bufio"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"time"
)

type client struct {
	weight  int
	agent   string
	paths   []string
	status  int
	network string
}

func main() {
	random := rand.New(rand.NewPCG(7, 11)) //nolint:gosec // demo data wants to be the same every time
	pages := []string{"/", "/", "/", "/uk/", "/uk/", "/ru/", "/privacy/", "/_astro/index.Bx81kQ.css", "/assets/analytics.js", "/favicon.svg", "/og/krokosha.png", "/api/e"}
	clients := []client{
		{40, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36", pages, 200, "203.0.113."},
		{16, "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1", pages, 200, "203.0.113."},
		{8, "Mozilla/5.0 (X11; Linux x86_64; rv:142.0) Gecko/20100101 Firefox/142.0", pages, 200, "203.0.113."},
		{12, "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", []string{"/", "/uk/", "/ru/", "/robots.txt", "/sitemap.xml", "/privacy/"}, 200, "66.249.66."},
		{5, "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm) Chrome/116.0.1938.76 Safari/537.36", []string{"/", "/uk/", "/sitemap.xml"}, 200, "40.77.167."},
		{3, "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2; +https://openai.com/gptbot)", []string{"/", "/ru/"}, 200, "20.171.206."},
		{2, "TelegramBot (like TwitterBot)", []string{"/", "/uk/"}, 200, "149.154.161."},
		{2, "Mozilla/5.0 (compatible; AhrefsBot/7.0; +http://ahrefs.com/robot/)", []string{"/", "/old-portfolio/", "/blog/"}, 404, "54.36.148."},
		{7, "python-requests/2.32.3", []string{"/.env", "/.env.production", "/wp-login.php", "/wp-admin/setup-config.php", "/.git/config", "/phpmyadmin/index.php", "/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php", "/actuator/health"}, 404, "198.51.100."},
		{3, "Mozilla/5.0 zgrab/0.x", []string{"/", "/admin/", "/config.json", "/backup.sql.gz"}, 404, "192.0.2."},
		{2, "curl/8.5.0", []string{"/", "/api/health"}, 200, "198.51.100."},
	}
	total := 0
	for _, c := range clients {
		total += c.weight
	}

	now := time.Now().UTC()
	var lines []string
	for range 6000 {
		pick := random.IntN(total)
		var who client
		for _, c := range clients {
			if pick < c.weight {
				who = c
				break
			}
			pick -= c.weight
		}
		// Recent days are busier; people come by day, machines around the clock.
		daysAgo := int(14 * random.Float64() * random.Float64())
		hour := random.IntN(24)
		if who.status == 200 && who.network == "203.0.113." {
			hour = 6 + random.IntN(15)
		}
		at := time.Date(now.Year(), now.Month(), now.Day(), hour, random.IntN(60), random.IntN(60), 0, time.UTC).AddDate(0, 0, -daysAgo)
		if at.After(now) {
			at = at.AddDate(0, 0, -1)
		}
		path, status := who.paths[random.IntN(len(who.paths))], who.status
		if status == 200 && (path == "/old-portfolio/" || path == "/blog/") {
			status = 404
		}
		if path == "/api/e" {
			status = 204
		}
		size := 300 + random.IntN(600)
		if status == 200 {
			size = 4000 + random.IntN(60000)
		}
		seconds := 0.001 + random.Float64()*0.008
		if random.IntN(40) == 0 {
			seconds = 0.2 + random.Float64()*1.5
		}
		lines = append(lines, fmt.Sprintf(`{"time":"%s","remote_addr":"%s%d","host":"krokosha.xyz","method":"GET","uri":"%s","protocol":"HTTP/2.0","status":%d,"bytes_sent":%d,"request_time":%.3f,"referer":"","user_agent":"%s"}`,
			at.Format("2006-01-02T15:04:05+00:00"), who.network, 1+random.IntN(200), path, status, size, seconds, who.agent))
	}
	sort.Strings(lines) // the time comes first in a line, so this is chronological order

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for _, line := range lines {
		fmt.Fprintln(out, line)
	}
}
