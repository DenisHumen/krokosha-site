package admin

import (
	"bufio"
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
)

// seed stores one visit of the report day: a paid click from Google to /uk/, two sections, a click
// on Telegram — and a campaign name that a spreadsheet would mistake for a formula.
func (s *site) seed() {
	s.t.Helper()
	started := reportDay.Add(-time.Hour) // 09:00
	result, err := s.db.Exec(`
		INSERT INTO analytics_pageviews (pageview_id, visitor, session_id, started_at, day, path, lang, referrer_host, referrer_kind,
			utm_source, utm_campaign, is_ad, ip_prefix, device, browser, os, duration_ms, max_scroll)
		VALUES (?, ?, ?, ?, ?, '/uk/', 'uk', 'google.com', 'search', '=HYPERLINK("https://evil.example")', '<b>mikrotik</b>', 1,
			'203.0.113.0/24', 'mobile', 'Safari', 'iOS', 42000, 75)`,
		[]byte("pageview"), []byte("visitor-visitor-"), []byte{0xAA, 0xBB, 0xCC, 0xDD, 1, 2, 3, 4}, started, started.Format(time.DateOnly))
	if err != nil {
		s.t.Fatal(err)
	}
	pageview, _ := result.LastInsertId()
	for i, event := range [][3]any{{"section", "hero", nil}, {"section", "skills", nil}, {"section_time", "skills", 30000},
		{"click", "cta-telegram", nil}, {"click", "skill-devops", nil}, {"egg", "konami", nil}} {
		if _, err := s.db.Exec(`INSERT INTO analytics_events (pageview, occurred_at, day, type, target, value) VALUES (?, ?, ?, ?, ?, ?)`,
			pageview, started.Add(time.Duration(i+1)*time.Second), started.Format(time.DateOnly), event[0], event[1], event[2]); err != nil {
			s.t.Fatal(err)
		}
	}
}

func TestOverviewShowsThePeriod(t *testing.T) {
	s := newSite(t)
	s.seed()
	s.signIn()

	page := s.do(http.MethodGet, prefix+"/", nil, nil)
	if page.status != http.StatusOK {
		t.Fatalf("overview: %d", page.status)
	}
	for _, want := range []string{
		"Суббота, 19 сентября 2026",                     // the report day, in words
		`id="active-count" aria-live="polite">3<`,       // «now on the site»
		`data-live="` + prefix + `/live"`,               // where the script connects
		`title="09:00 — 1 визит, из них по рекламе: 1"`, // the bar of the timeline
		"дошёл до секции «Навыки»",                      // the feed speaks the owner's language
		"открыл ветку навыков «devops»",
		"нашёл пасхалку konami",
		"Реклама", "Телефон", "Українська", "не определена", // ids became words
		`href="` + prefix + `/?d=2026-09-19&amp;p=week"`, // the switcher keeps the place
		`href="` + prefix + `/?d=2026-09-18&amp;p=day"`,  // ‹ yesterday
		`href="` + prefix + `/export/pageviews.csv?d=2026-09-19&amp;p=day"`,
		`src="` + prefix + `/static/admin.js"`,
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the overview lacks %q", want)
		}
	}
	// There is no tomorrow to go to.
	if strings.Contains(page.body, "d=2026-09-20") {
		t.Error("the switcher offers a day that has not come yet")
	}
	// Whatever a visitor typed into a UTM tag stays text, and the page needs no inline code.
	if strings.Contains(page.body, "<b>mikrotik") || !strings.Contains(page.body, "&lt;b&gt;mikrotik") {
		t.Error("a campaign name reached the page unescaped")
	}
	if regexp.MustCompile(`(?i)\sstyle=|<script>|\son[a-z]+=`).MatchString(page.body) {
		t.Error("the overview carries inline code or styles: the CSP would block them")
	}

	week := s.do(http.MethodGet, prefix+"/?p=week&d=2026-09-19", nil, nil)
	if !strings.Contains(week.body, "14 сентября — 20 сентября 2026") || !strings.Contains(week.body, `title="19.09, сб — 1 визит`) {
		t.Error("the week view lacks its title or its daily bars")
	}
	if got := s.do(http.MethodGet, prefix+"/?p=custom&from=2026-09-01&to=2026-09-19", nil, nil); !strings.Contains(got.body, "1 сентября — 19 сентября 2026") {
		t.Error("a custom range is not shown")
	}
	if got := s.do(http.MethodGet, prefix+"/?p=month&d=2026-08-05", nil, nil); !strings.Contains(got.body, "Август 2026") || !strings.Contains(got.body, "Нет данных за период.") {
		t.Error("an empty month should open and say so")
	}
	if got := s.do(http.MethodGet, prefix+"/?p=%27%22%3E&d=../../etc&from=x", nil, nil); got.status != http.StatusOK {
		t.Errorf("nonsense in the query: %d, want today's overview", got.status)
	}

	s.cookie = ""
	if got := s.do(http.MethodGet, prefix+"/?p=week", nil, nil); got.status != http.StatusSeeOther {
		t.Errorf("anonymous overview: %d", got.status)
	}
}

func TestVisitsScreens(t *testing.T) {
	s := newSite(t)
	s.seed()
	s.signIn()

	list := s.do(http.MethodGet, prefix+"/visits", nil, nil)
	link := regexp.MustCompile(`href="(` + prefix + `/visits/[0-9a-f]{16})\?d=2026-09-19&amp;p=day"`).FindStringSubmatch(list.body)
	if list.status != http.StatusOK || link == nil {
		t.Fatalf("visits: %d, link to the visit: %v", list.status, link)
	}
	for _, want := range []string{"1 визит", "Реклама", "google.com", "контакт", "42 с", "75%", "Телефон"} {
		if !strings.Contains(list.body, want) {
			t.Errorf("the list of visits lacks %q", want)
		}
	}

	detail := s.do(http.MethodGet, link[1], nil, nil)
	if detail.status != http.StatusOK {
		t.Fatalf("visit: %d", detail.status)
	}
	for _, want := range []string{"203.0.113.0/24", "Українська", "открыл страницу", "на экране 42 с", "клик: Telegram", "09:00:04"} {
		if !strings.Contains(detail.body, want) {
			t.Errorf("the visit page lacks %q", want)
		}
	}
	for _, id := range []string{"0000000000000000", "nope", "aabbccdd0102030"} {
		if got := s.do(http.MethodGet, prefix+"/visits/"+id, nil, nil); got.status != http.StatusNotFound {
			t.Errorf("visit %q: %d, want 404", id, got.status)
		}
	}
}

func TestExportIsSafeToOpenInASpreadsheet(t *testing.T) {
	s := newSite(t)
	s.seed()
	s.signIn()

	got := s.do(http.MethodGet, prefix+"/export/pageviews.csv?p=day&d=2026-09-19", nil, nil)
	if got.status != http.StatusOK || !strings.HasPrefix(got.header.Get("Content-Type"), "text/csv") {
		t.Fatalf("export: %d %s", got.status, got.header.Get("Content-Type"))
	}
	if disposition := got.header.Get("Content-Disposition"); disposition != `attachment; filename="krokosha-pageviews-2026-09-19_2026-09-19.csv"` {
		t.Errorf("Content-Disposition = %s", disposition)
	}
	body, hasBOM := strings.CutPrefix(got.body, "\xEF\xBB\xBF")
	if !hasBOM {
		t.Error("no byte order mark: a spreadsheet would garble Cyrillic")
	}
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows: %d, %v", len(rows), err)
	}
	columns := map[string]string{}
	for i, name := range rows[0] {
		columns[name] = rows[1][i]
	}
	if columns["utm_source"] != `'=HYPERLINK("https://evil.example")` {
		t.Errorf("a formula was exported as is: %q", columns["utm_source"])
	}
	if columns["path"] != "/uk/" || columns["day"] != "2026-09-19" || columns["ip_prefix"] != "203.0.113.0/24" || columns["visible_ms"] != "42000" {
		t.Errorf("exported row: %v", columns)
	}

	events := s.do(http.MethodGet, prefix+"/export/events.csv?p=day&d=2026-09-19", nil, nil)
	if lines := strings.Count(events.body, "\n"); events.status != http.StatusOK || lines != 1+6 {
		t.Errorf("events export: %d, %d lines", events.status, lines)
	}
	if got := s.do(http.MethodGet, prefix+"/export/admin_users.csv", nil, nil); got.status != http.StatusNotFound {
		t.Errorf("exporting another table: %d, want 404", got.status)
	}

	var logged int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'admin.export' AND actor = 'denis'`).Scan(&logged); err != nil || logged != 2 {
		t.Errorf("exports in the audit log: %d, %v", logged, err)
	}
	s.cookie = ""
	if got := s.do(http.MethodGet, prefix+"/export/pageviews.csv", nil, nil); got.status != http.StatusSeeOther {
		t.Errorf("anonymous export: %d", got.status)
	}
}

func TestLiveFeedStreamsToTheDashboard(t *testing.T) {
	s := newSite(t)
	s.signIn()
	web := httptest.NewServer(s.handler)
	defer web.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	open := func(cookie string) *http.Response {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, web.URL+prefix+"/live", nil)
		if err != nil {
			t.Fatal(err)
		}
		if cookie != "" {
			request.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		}
		response, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	anonymous := open("")
	anonymous.Body.Close()
	if anonymous.StatusCode != http.StatusSeeOther {
		t.Errorf("the feed without a session: %d", anonymous.StatusCode)
	}

	stream := open(s.cookie)
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK || !strings.HasPrefix(stream.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("the feed: %d %s", stream.StatusCode, stream.Header.Get("Content-Type"))
	}
	if stream.Header.Get("X-Accel-Buffering") != "no" {
		t.Error("nginx would buffer the stream")
	}

	lines := bufio.NewScanner(stream.Body)
	next := func() (event, data string) {
		for lines.Scan() {
			switch line := lines.Text(); {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "" && event != "":
				return event, data
			}
		}
		t.Fatalf("the stream ended: %v", lines.Err())
		return "", ""
	}

	if event, data := next(); event != "active" || data != `{"count":3}` {
		t.Errorf("first event: %s %s", event, data)
	}
	s.feed <- analytics.Live{At: reportDay, Visitor: "a1b2c3d4", Path: "/ru/", Type: analytics.TypeClick, Target: "cta-email"}
	if event, data := next(); event != "activity" || data != `{"time":"10:00:00","visitor":"a1b2c3d4","path":"/ru/","text":"клик: Почта"}` {
		t.Errorf("activity event: %s %s", event, data)
	}
}

func TestChartsAndWords(t *testing.T) {
	for n, want := range map[int]string{1: "1 визит", 2: "2 визита", 5: "5 визитов", 11: "11 визитов", 21: "21 визит", 112: "112 визитов", 0: "0 визитов"} {
		if got := plural(n, "визит", "визита", "визитов"); got != want {
			t.Errorf("plural(%d) = %q, want %q", n, got, want)
		}
	}
	for ms, want := range map[int64]string{0: "—", 400: "—", 900: "1 с", 59_000: "59 с", 471_000: "7 мин 51 с", 3_720_000: "1 ч 02 мин"} {
		if got := duration(ms); got != want {
			t.Errorf("duration(%d) = %q, want %q", ms, got, want)
		}
	}
	for value, want := range map[float64]string{0: "0%", 0.4: "<1%", 12.5: "12%", 66.67: "67%", 100: "100%"} {
		if got := formatPercent(value); got != want {
			t.Errorf("formatPercent(%v) = %q, want %q", value, got, want)
		}
	}
	for input, want := range map[[2]string]string{
		{"click", "social-github"}:    "клик: GitHub",
		{"click", "cta-discuss"}:      "клик: «Обсудить проект»",
		{"click", "project-wg-easy"}:  "открыл проект wg-easy",
		{"click", "lang-uk"}:          "переключил язык: Українська",
		{"click", "something-new"}:    "клик: something-new",
		{"outbound", "t.me"}:          "ушёл на t.me",
		{"section", "never-heard-of"}: "дошёл до секции «never-heard-of»",
		{"pageview", ""}:              "открыл страницу",
	} {
		if got := describe(input[0], input[1]); got != want {
			t.Errorf("describe(%q, %q) = %q, want %q", input[0], input[1], got, want)
		}
	}
	// Countries come from the database as codes and are shown by the names of CLDR.
	for code, want := range map[string]string{"UA": "Украина", "DE": "Германия", "US": "Соединенные Штаты", "": "не определена", "ZZ": "неизвестный регион", "??": "??"} {
		if got := countryName(code); got != want {
			t.Errorf("countryName(%q) = %q, want %q", code, got, want)
		}
	}
	if svg := string(ring(140, "javascript:alert(1)")); !strings.Contains(svg, `stroke-dasharray="100.0 0.0"`) || !strings.Contains(svg, "ring-accent") {
		t.Errorf("ring must clamp its input: %s", svg)
	}
	if svg := string(bar(-5, "cyan")); !strings.Contains(svg, `width="0.0"`) || !strings.Contains(svg, "bar-cyan") {
		t.Errorf("bar must clamp its input: %s", svg)
	}
}

// A period older than the raw data is shown from the sums of its days, and says so.
func TestOverviewOfAPeriodOlderThanTheRawData(t *testing.T) {
	s := newSite(t)
	if _, err := s.db.Exec(`INSERT INTO analytics_daily (day, visitors, visits, ad_visits, pageviews, view_ms_sum, view_ms_count, actions,
			scroll25, scroll50, scroll75, scroll100, aggregated_at) VALUES ('2025-01-15', 40, 44, 11, 97, 900000, 90, 12, 80, 60, 40, 20, NOW(3))`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO analytics_daily_breakdown (day, dimension, name, n) VALUES
			('2025-01-15', 'page', '/uk/', 61), ('2025-01-15', 'page', '/', 36), ('2025-01-15', 'source', 'ads', 11), ('2025-01-15', 'source', 'search', 33),
			('2025-01-15', 'section', 'hero', 90), ('2025-01-15', 'section_ms', 'hero', 400000), ('2025-01-15', 'contact', 'telegram', 7)`); err != nil {
		t.Fatal(err)
	}
	s.signIn()
	for _, query := range []string{"?p=day&d=2025-01-15", "?p=month&d=2025-01-15", "?p=custom&from=2025-01-01&to=2025-03-31"} {
		page := s.do(http.MethodGet, prefix+"/"+query, nil, nil)
		if page.status != http.StatusOK {
			t.Fatalf("%s: %d", query, page.status)
		}
		for _, want := range []string{"показаны дневные итоги", "/uk/", "97"} {
			if !strings.Contains(page.body, want) {
				t.Errorf("%s: the page lacks %q", query, want)
			}
		}
	}
	// A recent period says nothing of the kind.
	if page := s.do(http.MethodGet, prefix+"/?p=week", nil, nil); strings.Contains(page.body, "показаны дневные итоги") {
		t.Error("a recent week is shown as sums")
	}
}

func TestSpreadsheetSafe(t *testing.T) {
	for in, want := range map[string]string{
		"":                            "",
		"mikrotik":                    "mikrotik",
		"price - 100, as agreed":      "price - 100, as agreed",
		"=SUM(A1)":                    "'=SUM(A1)",
		"-5":                          "'-5",
		"@cmd":                        "'@cmd",
		`x;=HYPERLINK("http://evil")`: `x;'=HYPERLINK("http://evil")`,
		"Ivan; -cmd|' /C calc'!A1":    "Ivan; '-cmd|' /C calc'!A1",
		`a;"=1+1"`:                    `a;"'=1+1"`,
		"first line\n=cmd\n- a point": "first line\n'=cmd\n'- a point",
		"a\t+1":                       "a\t'+1",
		"a;\r=1":                      "a;'\r'=1",
	} {
		if got := spreadsheetSafe(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
