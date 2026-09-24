package admin

import (
	"context"
	"fmt"
	"math"
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
	// Vitals are the processor, the disk and the backup, for the header of every page.
	Vitals() sysstatus.Vitals
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
	Requests []stackBar // people above, bots below
	Bytes    []visitBar
	Axis     []string // labels under the chart of requests: five of them, evenly spread
	Statuses []statusShare
	BotShare float64
	Error5xx float64 // share of 5xx, %
	PolledAt time.Time
}

// stackBar is a column of two parts: Top above Bottom, both size classes of a fixed-height chart.
type stackBar struct {
	Title       string
	Top, Bottom int
}

func (h *Handler) traffic(w http.ResponseWriter, r *http.Request) {
	period := h.period(r, "/traffic")
	report, err := h.opts.Traffic.Traffic(r.Context(), period.Period)
	if err != nil {
		h.fail(w, r, "cannot compute the traffic report", err)
		return
	}

	label := func(i int) string {
		text := bucketLabel(report.Timeline[i].Start, report.Hourly)
		if !report.Hourly {
			text += ", " + weekdays[report.Timeline[i].Start.Weekday()]
		}
		return text
	}
	data := trafficData{
		Period:   period,
		Traffic:  report,
		BotShare: percentOf(report.Totals.Bots, report.Totals.Requests),
		Error5xx: percentOf(report.Totals.Status[3], report.Totals.Requests),
		PolledAt: h.opts.LogPolled(),
	}
	var most int
	var mostBytes int64
	for _, bucket := range report.Timeline {
		most, mostBytes = max(most, bucket.Requests), max(mostBytes, bucket.Bytes)
	}
	for i, bucket := range report.Timeline {
		bar := stackBar{Title: fmt.Sprintf("%s — %s, из них боты и программы: %d; не найдено: %d; ошибок сервера: %d",
			label(i), plural(bucket.Requests, "запрос", "запроса", "запросов"), bucket.Bots, bucket.NotFound, bucket.Errors)}
		if most > 0 {
			bar.Top = int(math.Round(float64(bucket.Requests-bucket.Bots) * 100 / float64(most)))
			bar.Bottom = int(math.Round(float64(bucket.Bots) * 100 / float64(most)))
		}
		data.Requests = append(data.Requests, bar)
		sent := visitBar{Title: label(i) + " — " + formatBytes(bucket.Bytes), Text: formatBytes(bucket.Bytes)}
		if mostBytes > 0 {
			sent.Height = int(math.Round(float64(bucket.Bytes) * 78 / float64(mostBytes)))
			sent.Peak = bucket.Bytes == mostBytes
		}
		if n := len(report.Timeline); n <= 14 || i%max(1, n/8) == 0 {
			sent.Label = barLabel(bucket.Start, report.Hourly, n)
		}
		data.Bytes = append(data.Bytes, sent)
	}
	if n := len(report.Timeline); n > 0 {
		for _, i := range []int{0, n / 4, n / 2, 3 * n / 4, n - 1} {
			data.Axis = append(data.Axis, barLabel(report.Timeline[i].Start, report.Hourly, n))
		}
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
