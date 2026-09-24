package admin

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

const (
	activeWindow   = 5 * time.Minute // «now on the site» = seen in the last five minutes
	feedLength     = 40
	visitsPerPage  = 50
	liveTick       = 10 * time.Second // the visitor counter; doubles as the keep-alive of the stream
	liveRecheck    = time.Minute      // is the session still valid?
	liveMaxAge     = 30 * time.Minute // then the browser reconnects and authenticates again
	exportMaxBytes = 64 << 20
)

// --- the period switcher -------------------------------------------------------------------------

// periodView is what the templates need to draw the switcher and to keep the period in links.
// The queries («p=week&d=2026-09-14») are built by periodQuery; templates turn them into links
// with the «href» function.
type periodView struct {
	Period analytics.Period
	Route  string // the page the switcher belongs to: "/" or "/visits"
	Title  string // «14 сентября — 20 сентября 2026»
	Short  string // «14–20 сен 2026»
	Kind   string // «неделя»: what the heading calls the period
	Query  string // the period being shown
	Prev   string
	Next   string // empty when the next period would lie in the future
	Day    string
	Week   string
	Month  string
	From   string // values of the custom range form
	To     string
	Today  string
}

func periodQuery(period analytics.Period) string {
	values := url.Values{}
	switch period.Kind {
	case "custom":
		values.Set("p", "custom")
		values.Set("from", period.From.Format(time.DateOnly))
		values.Set("to", period.To.Format(time.DateOnly))
	default:
		values.Set("p", period.Kind)
		values.Set("d", period.From.Format(time.DateOnly))
	}
	return values.Encode()
}

func (h *Handler) period(r *http.Request, route string) periodView {
	query := r.URL.Query()
	reports := h.opts.Reports
	period := reports.ParsePeriod(query.Get("p"), query.Get("d"), query.Get("from"), query.Get("to"))
	today := reports.Today()

	view := periodView{
		Period: period,
		Route:  route,
		Today:  today.Format(time.DateOnly),
		Title:  periodTitle(period),
		Short:  periodShort(period),
		Kind:   periodKinds[period.Kind],
		Query:  periodQuery(period),
		Prev:   periodQuery(period.Shift(-1)),
		From:   period.From.Format(time.DateOnly),
		To:     period.To.Format(time.DateOnly),
	}
	if next := period.Shift(1); !next.From.After(today) {
		view.Next = periodQuery(next)
	}
	// Switching the scale keeps the place: the week and the month around the day being looked at.
	anchor := period.From.Format(time.DateOnly)
	if !period.To.After(today) && period.Kind != "day" {
		anchor = period.To.Format(time.DateOnly)
	}
	if period.To.After(today) {
		anchor = today.Format(time.DateOnly)
	}
	for kind, target := range map[string]*string{"day": &view.Day, "week": &view.Week, "month": &view.Month} {
		*target = url.Values{"p": {kind}, "d": {anchor}}.Encode()
	}
	return view
}

// --- overview ------------------------------------------------------------------------------------

type feedLine struct {
	Time    string `json:"time"`
	Visitor string `json:"visitor"`
	Path    string `json:"path"`
	Text    string `json:"text"`
}

// visitBar is a bar of the visits chart: its height is a size class (.h-N, the tallest is 78, so
// that the number above it fits), its label is shown under few bars or under every n-th of many.
type visitBar struct {
	Value  int
	Text   string // the number above the bar when it is not Value: «2,4 МБ»
	Label  string
	Title  string
	Height int
	Peak   bool
}

type overviewData struct {
	Period   periodView
	Overview *analytics.Overview
	Active   int
	Feed     []feedLine
	IsToday  bool
	// HasBots: the traffic reader has counted automated clients for this period; Bots is how many.
	HasBots bool
	Bots    int64

	Bars       []visitBar
	ShowValues bool   // few bars: the number above each one
	Peak       string // «пик: чт 19 сен»
	Change     string // «+12,4% к прошлой неделе»; "" when the period before had no visits
	// Requests of the period (without spam), how many of them are done, and how quickly the first
	// answer came; nil without the store of requests.
	Funnel     *leads.Funnel
	Requests   int
	Conversion float64 // requests per visitor of the period, %
	Contacts   float64 // page views that reached the «contacts» section, %
	// PrevView is the average time on a page in the period before; ViewBar compares with it.
	PrevView int
	ViewBar  float64
	Devices  []analytics.Share
}

func (h *Handler) feedLine(at time.Time, visitor, path, kind, target string) feedLine {
	return feedLine{Time: at.In(h.opts.Location).Format("15:04:05"), Visitor: visitor, Path: path, Text: describe(kind, target)}
}

// overviewFeed is how many lines of activity the page starts with; the tile shows the newest.
const overviewFeed = 12

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	period := h.period(r, "/")
	overview, err := h.opts.Reports.Overview(ctx, period.Period)
	if err != nil {
		h.fail(w, r, "cannot compute the overview", err)
		return
	}
	recent, err := h.opts.Reports.Recent(ctx, overviewFeed)
	if err != nil {
		h.fail(w, r, "cannot read the recent activity", err)
		return
	}
	before, err := h.opts.Reports.Totals(ctx, period.Period.Shift(-1))
	if err != nil {
		h.fail(w, r, "cannot count the period before", err)
		return
	}
	bots := h.botClients(ctx, period.Period)
	data := overviewData{
		Period:   period,
		Overview: overview,
		HasBots:  bots != nil,
		Active:   h.opts.Active(ctx, activeWindow),
		IsToday:  period.Period.Days() == 1 && period.Period.From.Equal(h.opts.Reports.Today()),
		PrevView: before.AvgViewMs,
		Devices:  overview.Devices,
	}
	for _, count := range bots {
		data.Bots += count
	}
	data.Bars, data.ShowValues, data.Peak = visitBars(overview, bots)
	if before.Visits > 0 {
		data.Change = fmt.Sprintf("%s%% %s", signedDecimal(float64(overview.Totals.Visits-before.Visits)*100/float64(before.Visits)), comparedWith[period.Period.Kind])
	}
	if before.AvgViewMs > 0 {
		data.ViewBar = math.Min(100, float64(overview.Totals.AvgViewMs)*100/float64(before.AvgViewMs))
	}
	for _, section := range overview.Sections {
		if section.Name == "contacts" {
			data.Contacts = section.Reach
		}
	}
	if h.opts.Leads != nil {
		from, to := period.Period.From, period.Period.To.AddDate(0, 0, 1)
		if data.Funnel, err = h.opts.Leads.Funnel(ctx, from, to); err != nil {
			h.fail(w, r, "cannot count the requests of the period", err)
			return
		}
		data.Requests = data.Funnel.Total - data.Funnel.Spam
		data.Conversion = percentOf(data.Requests, overview.Totals.Visitors)
	}
	for _, item := range recent {
		data.Feed = append(data.Feed, h.feedLine(item.At, item.Visitor, item.Path, item.Type, item.Target))
	}
	// Ids become words here, so that one template draws every breakdown.
	rename(overview.Sources, named(sourceNames, "—"))
	rename(overview.Devices, named(deviceNames, "—"))
	rename(overview.Languages, named(languageNames, "не указан"))
	rename(overview.Countries, countryName)
	rename(overview.Clicks, func(target string) string {
		return strings.TrimPrefix(describe(analytics.TypeClick, target), "клик: ")
	})
	h.render(w, r, http.StatusOK, "overview", view{Title: "Обзор · " + period.Kind, Nav: "overview", Data: data})
}

// comparedWith ends «+12,4% …»: what the period is compared with.
var comparedWith = map[string]string{"day": "к прошлому дню", "week": "к прошлой неделе", "month": "к прошлому месяцу", "custom": "к прошлому периоду"}

// visitBars turns the timeline into bars: people's visits (the bots of the server log go to the
// tooltip), the tallest one marked as the peak.
func visitBars(overview *analytics.Overview, bots []int64) (bars []visitBar, values bool, peak string) {
	timeline := overview.Timeline
	highest, top := 0, -1
	for i, bucket := range timeline {
		if total := bucket.Organic + bucket.Ads; total > highest {
			highest, top = total, i
		}
	}
	every := 1
	switch count := len(timeline); {
	case overview.Hourly:
		every = 3
	case count > 120:
		every = 30
	case count > 45:
		every = 7
	case count > 14:
		every = 5
	}
	for i, bucket := range timeline {
		total := bucket.Organic + bucket.Ads
		bar := visitBar{Value: total, Title: bucketTitle(bucketLabel(bucket.Start, overview.Hourly), bucket, overview.Hourly), Peak: i == top}
		if len(bots) == len(timeline) && bots[i] > 0 {
			bar.Title += fmt.Sprintf("; ботов: %d", bots[i])
		}
		if highest > 0 {
			bar.Height = int(math.Round(float64(total) * 78 / float64(highest)))
		}
		if i%every == 0 {
			bar.Label = barLabel(bucket.Start, overview.Hourly, len(timeline))
		}
		bars = append(bars, bar)
	}
	if top >= 0 {
		peak = "пик: " + barLabel(timeline[top].Start, overview.Hourly, 0)
		if !overview.Hourly {
			peak = "пик: " + weekdays[timeline[top].Start.Weekday()] + " " + fmt.Sprintf("%d %s", timeline[top].Start.Day(), shortMonths[timeline[top].Start.Month()])
		}
	}
	return bars, len(timeline) <= 14, peak
}

// barLabel is what stands under a bar: «чт» for a week, «19» for a month, «14:00» for a day.
func barLabel(start time.Time, hourly bool, buckets int) string {
	switch {
	case hourly:
		return start.Format("15:04")
	case buckets > 0 && buckets <= 7:
		return weekdays[start.Weekday()]
	case buckets > 0 && buckets <= 45:
		return fmt.Sprint(start.Day())
	default:
		return start.Format("02.01")
	}
}

// signedDecimal: «+12,4», «−3», «0».
func signedDecimal(value float64) string {
	switch {
	case value > 0.05:
		return "+" + decimal(value)
	case value < -0.05:
		return "−" + decimal(-value)
	default:
		return "0"
	}
}

func rename(shares []analytics.Share, name func(string) string) {
	for i := range shares {
		shares[i].Name = name(shares[i].Name)
	}
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	if errors.Is(err, context.Canceled) {
		return // the visitor left; nobody is waiting for a page
	}
	h.opts.Log.Error(what, "error", err)
	h.render(w, r, http.StatusInternalServerError, "error", view{Title: "Ошибка", Error: "Не получилось прочитать данные. Подробности — в журнале сервиса: journalctl -u krokosha-api"})
}

// --- the live feed (Server-Sent Events) ----------------------------------------------------------

// live streams «now on the site» and new activity to the open dashboard. The stream never
// counts as activity of the administrator (auth.Check), ends with the session, and is cut after
// half an hour so that the browser comes back and proves its session again.
func (h *Handler) live(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("X-Accel-Buffering", "no") // nginx: pass every event on at once
	w.WriteHeader(http.StatusOK)

	send := func(event string, payload any) bool {
		body, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	feed, cancel := h.opts.Feed()
	defer cancel()
	ctx := r.Context()
	tick := time.NewTicker(liveTick)
	defer tick.Stop()
	recheck := time.NewTicker(liveRecheck)
	defer recheck.Stop()
	deadline := time.NewTimer(liveMaxAge)
	defer deadline.Stop()

	if !send("active", map[string]int{"count": h.opts.Active(ctx, activeWindow)}) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-recheck.C:
			if _, err := h.opts.Auth.Check(ctx, cookie.Value); err != nil {
				send("bye", map[string]string{"reason": "session"})
				return
			}
		case <-tick.C:
			if !send("active", map[string]int{"count": h.opts.Active(ctx, activeWindow)}) {
				return
			}
		case item, open := <-feed:
			if !open {
				return
			}
			if !send("activity", h.feedLine(item.At, item.Visitor, item.Path, item.Type, item.Target)) {
				return
			}
		}
	}
}

// --- visits --------------------------------------------------------------------------------------

type visitsData struct {
	Period periodView
	Visits []analytics.Visit
	Total  int
	Page   int
	Pages  int
	Prev   int // page numbers, 0 = none
	Next   int
}

func (h *Handler) visits(w http.ResponseWriter, r *http.Request) {
	period := h.period(r, "/visits")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)
	visits, total, err := h.opts.Reports.Visits(r.Context(), period.Period, visitsPerPage, (page-1)*visitsPerPage)
	if err != nil {
		h.fail(w, r, "cannot list the visits", err)
		return
	}
	data := visitsData{Period: period, Visits: visits, Total: total, Page: page, Pages: (total + visitsPerPage - 1) / visitsPerPage}
	if page > 1 {
		data.Prev = page - 1
	}
	if page < data.Pages {
		data.Next = page + 1
	}
	h.render(w, r, http.StatusOK, "visits", view{Title: "Визиты", Nav: "visits", Data: data})
}

type visitData struct {
	Detail *analytics.VisitDetail
	Back   string // query of the list the visitor came from
}

func (h *Handler) visit(w http.ResponseWriter, r *http.Request) {
	detail, err := h.opts.Reports.Visit(r.Context(), r.PathValue("id"))
	if errors.Is(err, analytics.ErrNoVisit) {
		h.render(w, r, http.StatusNotFound, "error", view{Title: "Визит не найден", Nav: "visits", Error: "Такого визита нет: возможно, он старше срока хранения."})
		return
	}
	if err != nil {
		h.fail(w, r, "cannot read the visit", err)
		return
	}
	h.render(w, r, http.StatusOK, "visit", view{Title: "Визит", Nav: "visits", Data: visitData{Detail: detail, Back: h.period(r, "/visits").Query}})
}

// --- CSV export ----------------------------------------------------------------------------------

// export streams page views or events of the period as CSV (brief B6).
func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	period := h.period(r, "/").Period
	table := strings.TrimSuffix(r.PathValue("table"), ".csv")
	var stream func(context.Context, analytics.Period, func([]string) error) error
	switch table {
	case "pageviews":
		stream = h.opts.Reports.ExportPageviews
	case "events":
		stream = h.opts.Reports.ExportEvents
	default:
		http.NotFound(w, r)
		return
	}

	session := sessionOf(r)
	h.opts.Auth.Audit(r.Context(), session.User.Login, "admin.export", table,
		period.From.Format(time.DateOnly)+" — "+period.To.Format(time.DateOnly), h.attemptMeta(r).IPPrefix)

	name := fmt.Sprintf("krokosha-%s-%s_%s.csv", table, period.From.Format(time.DateOnly), period.To.Format(time.DateOnly))
	header := w.Header()
	header.Set("Content-Type", "text/csv; charset=utf-8")
	header.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write([]byte("\xEF\xBB\xBF")) // spreadsheet programs read UTF-8 only when told so

	limited := &limitedWriter{writer: w, left: exportMaxBytes}
	out := csv.NewWriter(limited)
	err := stream(r.Context(), period, func(row []string) error {
		for i, cell := range row {
			row[i] = spreadsheetSafe(cell)
		}
		return out.Write(row)
	})
	out.Flush()
	if err != nil && !errors.Is(err, context.Canceled) {
		// The status line is gone already; the log is the only place left to say so.
		h.opts.Log.Error("export was cut short", "table", table, "error", err)
	}
}

// spreadsheetSafe defuses cells that a spreadsheet would run as a formula. UTM values, link
// targets, names and descriptions are typed by strangers; an exported file is opened by the owner.
// A spreadsheet may split the file at commas — or, as Excel does in Ukrainian and Russian settings,
// at semicolons, and there a cell also starts after every «;» and every line break inside a field
// (a quote in the middle of a line opens nothing). Wherever a cell may start, what would begin a
// formula — after any spaces and quotes — gets a «'» in front of it.
func spreadsheetSafe(cell string) string {
	var out strings.Builder
	start := true // a cell may start here
	for i := 0; i < len(cell); i++ {
		c := cell[i]
		if start && c != ' ' && c != '"' {
			if strings.IndexByte("=+-@\t\r", c) >= 0 {
				out.WriteByte('\'')
			}
			start = false
		}
		out.WriteByte(c)
		if strings.IndexByte(";\n\r\t", c) >= 0 {
			start = true
		}
	}
	return out.String()
}

type limitedWriter struct {
	writer http.ResponseWriter
	left   int
}

func (l *limitedWriter) Write(data []byte) (int, error) {
	if len(data) > l.left {
		return 0, errors.New("the export is larger than the limit; choose a shorter period")
	}
	l.left -= len(data)
	return l.writer.Write(data)
}
