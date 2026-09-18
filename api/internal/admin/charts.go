package admin

import (
	"fmt"
	"html"
	"html/template"
	"math"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
)

// Charts are drawn on the server as SVG. The admin area's CSP allows no inline styles and needs
// no chart library: shapes get classes, colours live in admin.css. Every number below is computed
// here; every piece of text goes through html.EscapeString.

const (
	chartWidth   = 960.0
	chartHeight  = 220.0
	chartLeft    = 34.0 // room for the axis labels
	chartBottom  = 22.0
	chartTop     = 8.0
	minimumScale = 4 // a single visit should not look like a full house
)

// niceCeil rounds a maximum up to a number that makes readable grid lines.
func niceCeil(value int) int {
	if value <= minimumScale {
		return minimumScale
	}
	magnitude := math.Pow(10, math.Floor(math.Log10(float64(value))))
	for _, step := range []float64{1, 2, 2.5, 5, 10} {
		if candidate := step * magnitude; candidate >= float64(value) {
			return int(candidate)
		}
	}
	return value
}

// timelineChart draws visits per hour (or per day) as stacked bars: organic below, paid on top.
func timelineChart(overview *analytics.Overview) template.HTML {
	buckets := overview.Timeline
	if len(buckets) == 0 {
		return ""
	}
	highest := 0
	for _, bucket := range buckets {
		highest = max(highest, bucket.Organic+bucket.Ads)
	}
	scale := niceCeil(highest)
	plotWidth, plotHeight := chartWidth-chartLeft, chartHeight-chartTop-chartBottom
	slot := plotWidth / float64(len(buckets))
	barWidth := math.Max(slot*0.72, 1)

	var svg strings.Builder
	fmt.Fprintf(&svg, `<svg class="chart" viewBox="0 0 %.0f %.0f" role="img" aria-label="%s">`,
		chartWidth, chartHeight, html.EscapeString(timelineSummary(overview)))

	// Grid: the floor, the middle, the top.
	for _, level := range []int{0, scale / 2, scale} {
		y := chartTop + plotHeight - plotHeight*float64(level)/float64(scale)
		fmt.Fprintf(&svg, `<line class="chart-grid" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`, chartLeft, y, chartWidth, y)
		fmt.Fprintf(&svg, `<text class="chart-label" x="%.1f" y="%.1f" text-anchor="end">%d</text>`, chartLeft-6, y+3.5, level)
	}

	every := labelEvery(len(buckets), overview.Hourly)
	for i, bucket := range buckets {
		x := chartLeft + slot*float64(i) + (slot-barWidth)/2
		organic := plotHeight * float64(bucket.Organic) / float64(scale)
		ads := plotHeight * float64(bucket.Ads) / float64(scale)
		floor := chartTop + plotHeight

		label := bucketLabel(bucket.Start, overview.Hourly)
		fmt.Fprintf(&svg, `<g><title>%s</title>`, html.EscapeString(bucketTitle(label, bucket, overview.Hourly)))
		// An invisible full-height target: the tooltip works on empty hours too.
		fmt.Fprintf(&svg, `<rect class="chart-slot" x="%.1f" y="%.1f" width="%.1f" height="%.1f"/>`, chartLeft+slot*float64(i), chartTop, slot, plotHeight)
		if bucket.Organic > 0 {
			fmt.Fprintf(&svg, `<rect class="chart-bar" x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="1.5"/>`, x, floor-organic, barWidth, organic)
		}
		if bucket.Ads > 0 {
			fmt.Fprintf(&svg, `<rect class="chart-bar chart-bar-ads" x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="1.5"/>`, x, floor-organic-ads, barWidth, ads)
		}
		svg.WriteString(`</g>`)
		if i%every == 0 {
			fmt.Fprintf(&svg, `<text class="chart-label" x="%.1f" y="%.1f" text-anchor="middle">%s</text>`,
				chartLeft+slot*float64(i)+slot/2, chartHeight-6, html.EscapeString(label))
		}
	}
	svg.WriteString(`</svg>`)
	return template.HTML(svg.String()) //nolint:gosec // built above from numbers and escaped text
}

func labelEvery(buckets int, hourly bool) int {
	switch {
	case hourly:
		return 2
	case buckets <= 14:
		return 1
	case buckets <= 45:
		return 3
	case buckets <= 120:
		return 7
	default:
		return 30
	}
}

func bucketLabel(start time.Time, hourly bool) string {
	if hourly {
		return start.Format("15:00")
	}
	return start.Format("02.01")
}

func bucketTitle(label string, bucket analytics.Bucket, hourly bool) string {
	total := bucket.Organic + bucket.Ads
	if !hourly {
		label += ", " + weekdays[bucket.Start.Weekday()]
	}
	title := fmt.Sprintf("%s — %s", label, plural(total, "визит", "визита", "визитов"))
	if bucket.Ads > 0 {
		title += fmt.Sprintf(", из них по рекламе: %d", bucket.Ads)
	}
	return title
}

func timelineSummary(overview *analytics.Overview) string {
	unit := "по дням"
	if overview.Hourly {
		unit = "по часам"
	}
	return fmt.Sprintf("Визиты %s: всего %d, из них по рекламе %d", unit, overview.Totals.Visits, overview.Totals.AdVisits)
}

// ring is a conversion «score»: a circle filled to the given percentage.
func ring(percent float64, tone string) template.HTML {
	const radius = 15.9155 // circumference of exactly 100: the dash array is the percentage itself
	filled := math.Max(0, math.Min(percent, 100))
	if tone != "cyan" && tone != "pink" {
		tone = "accent"
	}
	return template.HTML(fmt.Sprintf( //nolint:gosec // numbers and a class name from the list above
		`<svg class="ring" viewBox="0 0 36 36" aria-hidden="true">`+
			`<circle class="ring-track" cx="18" cy="18" r="%.4f"/>`+
			`<circle class="ring-value ring-%s" cx="18" cy="18" r="%.4f" stroke-dasharray="%.1f %.1f" transform="rotate(-90 18 18)"/>`+
			`</svg>`, radius, tone, radius, filled, 100-filled))
}

// bar is a thin horizontal progress bar.
func bar(percent float64, tone string) template.HTML {
	filled := math.Max(0, math.Min(percent, 100))
	if tone != "cyan" && tone != "pink" {
		tone = "accent"
	}
	return template.HTML(fmt.Sprintf( //nolint:gosec // numbers and a class name from the list above
		`<svg class="bar" viewBox="0 0 100 4" preserveAspectRatio="none" aria-hidden="true">`+
			`<rect class="bar-track" width="100" height="4"/><rect class="bar-value bar-%s" width="%.1f" height="4"/></svg>`, tone, filled))
}

// --- words ---------------------------------------------------------------------------------------

var weekdays = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}

var months = [...]string{"", "января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}

// plural picks the Russian form for a number: 1 визит, 2 визита, 5 визитов.
func plural(n int, one, few, many string) string {
	form := many
	switch {
	case n%100 >= 11 && n%100 <= 14:
	case n%10 == 1:
		form = one
	case n%10 >= 2 && n%10 <= 4:
		form = few
	}
	return fmt.Sprintf("%d %s", n, form)
}

// duration prints milliseconds the way the reference dashboard does: «7 мин 51 с».
func duration(ms int64) string {
	seconds := (ms + 500) / 1000
	switch {
	case seconds <= 0:
		return "—"
	case seconds < 60:
		return fmt.Sprintf("%d с", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%d мин %02d с", seconds/60, seconds%60)
	default:
		return fmt.Sprintf("%d ч %02d мин", seconds/3600, seconds%3600/60)
	}
}

func periodTitle(period analytics.Period) string {
	day := func(t time.Time) string { return fmt.Sprintf("%d %s", t.Day(), months[t.Month()]) }
	switch {
	case period.Days() == 1:
		return fmt.Sprintf("%s, %s %d", weekdayNames[period.From.Weekday()], day(period.From), period.From.Year())
	case period.Kind == "month":
		return fmt.Sprintf("%s %d", monthNames[period.From.Month()], period.From.Year())
	case period.From.Year() != period.To.Year():
		return fmt.Sprintf("%s %d — %s %d", day(period.From), period.From.Year(), day(period.To), period.To.Year())
	default:
		return fmt.Sprintf("%s — %s %d", day(period.From), day(period.To), period.To.Year())
	}
}

var weekdayNames = [...]string{"Воскресенье", "Понедельник", "Вторник", "Среда", "Четверг", "Пятница", "Суббота"}

var monthNames = [...]string{"", "Январь", "Февраль", "Март", "Апрель", "Май", "Июнь", "Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь"}

// Names of things the site reports by id (docs/contract.md).
var (
	sectionNames = map[string]string{
		"hero": "Первый экран", "stats": "Цифры", "services": "Услуги", "skills": "Навыки", "projects": "Проекты",
		"contacts": "Контакты", "footer": "Подвал", "curtain": "Занавес", "not-found": "Страница 404",
	}
	sourceNames = map[string]string{
		"direct": "Прямые заходы", "search": "Поиск", "social": "Соцсети", "other": "Другие сайты", "ads": "Реклама",
	}
	deviceNames   = map[string]string{"desktop": "Компьютер", "mobile": "Телефон", "tablet": "Планшет"}
	languageNames = map[string]string{"en": "English", "uk": "Українська", "ru": "Русский", "": "не указан"}
	contactNames  = map[string]string{"telegram": "Telegram", "email": "Почта", "github": "GitHub"}
)

func named(names map[string]string, fallback string) func(string) string {
	return func(id string) string {
		if name, ok := names[id]; ok {
			return name
		}
		if id == "" {
			return fallback
		}
		return id
	}
}

// describe turns a stored event into a line of the activity feed (brief B6: «открыл ветку DevOps»,
// «клик Telegram», «нашёл пасхалку konami»).
func describe(kind, target string) string {
	switch kind {
	case analytics.TypePageview:
		return "открыл страницу"
	case analytics.TypeSection:
		return "дошёл до секции «" + named(sectionNames, "?")(target) + "»"
	case analytics.TypeOutbound:
		return "ушёл на " + target
	case analytics.TypeEgg:
		return "нашёл пасхалку " + target
	case analytics.TypeClick:
		prefix, rest, _ := strings.Cut(target, "-")
		switch {
		case target == "cta-discuss":
			return "клик: «Обсудить проект»"
		case target == "projects-show-all":
			return "показал все проекты"
		case target == "game-entry":
			return "открыл вход в игру"
		case target == "form-submit":
			return "отправил форму заявки"
		case (prefix == "cta" || prefix == "social") && rest != "":
			return "клик: " + named(contactNames, rest)(rest)
		case prefix == "skill" && rest != "":
			return "открыл ветку навыков «" + rest + "»"
		case prefix == "project" && rest != "":
			return "открыл проект " + rest
		case prefix == "lang" && rest != "":
			return "переключил язык: " + named(languageNames, rest)(rest)
		}
		return "клик: " + target
	}
	return kind + " " + target
}

// formatPercent keeps small shares visible: 0.4 % is not «0 %».
func formatPercent(value float64) string {
	switch {
	case value <= 0:
		return "0%"
	case value < 1:
		return "<1%"
	default:
		return fmt.Sprintf("%.0f%%", value)
	}
}

// toInt64 lets templates pass whatever integer type a report uses.
func toInt64(value any) int64 {
	switch number := value.(type) {
	case int:
		return int64(number)
	case int64:
		return number
	case int32:
		return int64(number)
	case uint32:
		return int64(number)
	case float64:
		return int64(number)
	}
	return 0
}
