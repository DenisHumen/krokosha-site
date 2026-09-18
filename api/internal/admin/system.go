package admin

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/nginxlog"
	"github.com/DenisHumen/krokosha-site/api/internal/sysstatus"
)

// TrafficReports is what the «server traffic» screen needs (nginxlog.Reports).
type TrafficReports interface {
	Traffic(ctx context.Context, period analytics.Period) (*nginxlog.Traffic, error)
	Timeline(ctx context.Context, period analytics.Period) ([]nginxlog.Bucket, error)
}

// SystemStatus is what the «system status» screen needs (sysstatus.Service).
type SystemStatus interface {
	Collect(ctx context.Context) *sysstatus.Status
	RequestRebuild() error
}

// --- server traffic ------------------------------------------------------------------------------

type statusShare struct {
	Name    string
	Tone    string
	Count   int
	Percent float64
}

type trafficData struct {
	Period   periodView
	Traffic  *nginxlog.Traffic
	Requests template.HTML
	Bytes    template.HTML
	Statuses []statusShare
	BotShare float64
	PolledAt time.Time
}

func (h *Handler) traffic(w http.ResponseWriter, r *http.Request) {
	period := h.period(r, "/traffic")
	report, err := h.opts.Traffic.Traffic(r.Context(), period.Period)
	if err != nil {
		h.fail(w, r, "cannot compute the traffic report", err)
		return
	}

	people, bots, sent := make([]int64, len(report.Timeline)), make([]int64, len(report.Timeline)), make([]int64, len(report.Timeline))
	var starts []time.Time
	for i, bucket := range report.Timeline {
		starts = append(starts, bucket.Start)
		people[i], bots[i], sent[i] = int64(bucket.Requests-bucket.Bots), int64(bucket.Bots), bucket.Bytes
	}
	label := func(i int) string {
		text := bucketLabel(report.Timeline[i].Start, report.Hourly)
		if !report.Hourly {
			text += ", " + weekdays[report.Timeline[i].Start.Weekday()]
		}
		return text
	}
	data := trafficData{
		Period:  period,
		Traffic: report,
		Requests: barChart(barChartSpec{
			Summary: fmt.Sprintf("Запросы к серверу: всего %d, из них от ботов и программ %d", report.Totals.Requests, report.Totals.Bots),
			Starts:  starts, Hourly: report.Hourly,
			Series: []barSeries{{"chart-bar-people", people}, {"chart-bar-bots", bots}},
			Title: func(i int) string {
				b := report.Timeline[i]
				return fmt.Sprintf("%s — %s, из них боты и программы: %d; не найдено: %d; ошибок сервера: %d",
					label(i), plural(b.Requests, "запрос", "запроса", "запросов"), b.Bots, b.NotFound, b.Errors)
			},
			Axis: compactCount,
		}),
		Bytes: barChart(barChartSpec{
			Summary: "Передано данных: всего " + formatBytes(report.Totals.Bytes),
			Starts:  starts, Hourly: report.Hourly, Bytes: true,
			Series: []barSeries{{"chart-bar-bytes", sent}},
			Title:  func(i int) string { return label(i) + " — " + formatBytes(report.Timeline[i].Bytes) },
			Axis:   formatBytes,
		}),
		BotShare: percentOf(report.Totals.Bots, report.Totals.Requests),
		PolledAt: h.opts.LogPolled(),
	}
	if data.PolledAt.IsZero() {
		data.PolledAt = report.ReadAt
	}
	for i, class := range []struct{ name, tone string }{{"2xx — отдано", "accent"}, {"3xx — перенаправления", "cyan"}, {"4xx — ошибки запроса", "pink"}, {"5xx — ошибки сервера", "error"}} {
		data.Statuses = append(data.Statuses, statusShare{class.name, class.tone, report.Totals.Status[i], percentOf(report.Totals.Status[i], report.Totals.Requests)})
	}
	h.render(w, r, http.StatusOK, "traffic", view{Title: "Трафик сервера", Nav: "traffic", Data: data})
}

func percentOf(part, whole int) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) * 100 / float64(whole)
}

// compactCount keeps axis labels short: 12500 → «12,5 тыс».
func compactCount(value int64) string {
	switch {
	case value >= 1_000_000:
		return decimal(float64(value)/1_000_000) + " млн"
	case value >= 10_000:
		return decimal(float64(value)/1000) + " тыс"
	default:
		return fmt.Sprint(value)
	}
}

// decimal prints one digit after a decimal comma, and none when it would be a zero.
func decimal(value float64) string {
	return strings.TrimSuffix(strings.Replace(fmt.Sprintf("%.1f", value), ".", ",", 1), ",0")
}

// botClients returns the «bots» series of the overview's timeline, or nil when the traffic
// reader has nothing for the period (then the chart simply has no such colour).
func (h *Handler) botClients(ctx context.Context, period analytics.Period) []int64 {
	if h.opts.Traffic == nil {
		return nil
	}
	buckets, err := h.opts.Traffic.Timeline(ctx, period)
	if err != nil {
		h.opts.Log.Error("cannot read the bots of the timeline", "error", err)
		return nil
	}
	series := make([]int64, len(buckets))
	seen := false
	for i, bucket := range buckets {
		series[i] = int64(bucket.BotClients)
		seen = seen || bucket.BotClients > 0
	}
	if !seen {
		return nil
	}
	return series
}

// --- system status -------------------------------------------------------------------------------

type statusData struct {
	Status *sysstatus.Status
	Now    time.Time
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	data := statusData{Status: h.opts.System.Collect(r.Context()), Now: time.Now()}
	h.render(w, r, http.StatusOK, "status", view{Title: "Статус системы", Nav: "status", Data: data})
}

// rebuild is the «rebuild now» button (brief B6).
func (h *Handler) rebuild(w http.ResponseWriter, r *http.Request) {
	session := sessionOf(r)
	if err := h.opts.System.RequestRebuild(); err != nil {
		h.opts.Log.Error("cannot request a rebuild", "error", err)
		h.render(w, r, http.StatusInternalServerError, "error", view{Title: "Ошибка", Nav: "status",
			Error: "Не получилось передать запрос на пересборку. На сервере: sudo systemctl start krokosha-sync.service"})
		return
	}
	h.opts.Auth.Audit(r.Context(), session.User.Login, "admin.rebuild", "", "", h.attemptMeta(r).IPPrefix)
	http.Redirect(w, r, h.opts.Prefix+"/status?ok=rebuild", http.StatusSeeOther)
}
