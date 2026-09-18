package admin

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
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
	Title  string
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

type overviewData struct {
	Period   periodView
	Overview *analytics.Overview
	Timeline template.HTML
	Active   int
	Feed     []feedLine
	IsToday  bool
	HasBots  bool // the traffic reader has counted automated clients for this period
}

func (h *Handler) feedLine(at time.Time, visitor, path, kind, target string) feedLine {
	return feedLine{Time: at.In(h.opts.Location).Format("15:04:05"), Visitor: visitor, Path: path, Text: describe(kind, target)}
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	period := h.period(r, "/")
	overview, err := h.opts.Reports.Overview(r.Context(), period.Period)
	if err != nil {
		h.fail(w, r, "cannot compute the overview", err)
		return
	}
	recent, err := h.opts.Reports.Recent(r.Context(), feedLength)
	if err != nil {
		h.fail(w, r, "cannot read the recent activity", err)
		return
	}
	bots := h.botClients(r.Context(), period.Period)
	data := overviewData{
		Period:   period,
		Overview: overview,
		Timeline: timelineChart(overview, bots),
		HasBots:  bots != nil,
		Active:   h.opts.Active(r.Context(), activeWindow),
		IsToday:  period.Period.Days() == 1 && period.Period.From.Equal(h.opts.Reports.Today()),
	}
	for _, item := range recent {
		data.Feed = append(data.Feed, h.feedLine(item.At, item.Visitor, item.Path, item.Type, item.Target))
	}
	// Ids become words here, so that one template draws every breakdown.
	rename(overview.Sources, named(sourceNames, "—"))
	rename(overview.Devices, named(deviceNames, "—"))
	rename(overview.Languages, named(languageNames, "не указан"))
	rename(overview.Countries, named(nil, "не определена"))
	rename(overview.Clicks, func(target string) string {
		return strings.TrimPrefix(describe(analytics.TypeClick, target), "клик: ")
	})
	h.render(w, r, http.StatusOK, "overview", view{Title: "Обзор", Nav: "overview", Data: data})
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

// spreadsheetSafe defuses cells that a spreadsheet would run as a formula. UTM values and link
// targets are typed by strangers; an exported file is opened by the owner.
func spreadsheetSafe(cell string) string {
	if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
		return "'" + cell
	}
	return cell
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
