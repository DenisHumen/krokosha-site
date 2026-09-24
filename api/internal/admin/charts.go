package admin

import (
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
)

// Charts are drawn on the server: bars are elements with size classes (.h-N, .w-N in admin.css),
// rings and thin bars small SVGs. The admin area's CSP allows no inline styles and needs no chart
// library; every number below is computed here.

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
	return toneBar(percent, tone)
}

func toneBar(percent float64, tone string) template.HTML {
	filled := math.Max(0, math.Min(percent, 100))
	switch tone {
	case "cyan", "pink", "warn", "error":
	default:
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

// minutesSeconds prints a duration the way a stopwatch does: «1:48», «12:05», «1:02:03».
func minutesSeconds(ms int64) string {
	seconds := (max(ms, 0) + 500) / 1000
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds%3600/60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
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

// periodShort is the compact form of the period next to the switcher: «19 сен 2026»,
// «14–20 сен 2026», «28 сен – 4 окт 2026», «сентябрь 2026».
func periodShort(period analytics.Period) string {
	from, to := period.From, period.To
	day := func(t time.Time) string { return fmt.Sprintf("%d %s", t.Day(), shortMonths[t.Month()]) }
	switch {
	case period.Kind == "month":
		return fmt.Sprintf("%s %d", strings.ToLower(monthNames[from.Month()]), from.Year())
	case period.Days() == 1:
		return fmt.Sprintf("%s %d", day(from), from.Year())
	case from.Year() != to.Year():
		return fmt.Sprintf("%s %d – %s %d", day(from), from.Year(), day(to), to.Year())
	case from.Month() != to.Month():
		return fmt.Sprintf("%s – %s %d", day(from), day(to), to.Year())
	default:
		return fmt.Sprintf("%d–%d %s %d", from.Day(), to.Day(), shortMonths[to.Month()], to.Year())
	}
}

var shortMonths = [...]string{"", "янв", "фев", "мар", "апр", "мая", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"}

var periodKinds = map[string]string{"day": "день", "week": "неделя", "month": "месяц", "custom": "период"}

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
	deviceNames = map[string]string{"desktop": "Компьютер", "mobile": "Телефон", "tablet": "Планшет"}
	// shortNames label the strip of devices: «ПК 58% · ТЕЛ 36% · ПЛ 6%».
	shortNames    = map[string]string{"Компьютер": "ПК", "Телефон": "Тел", "Планшет": "Пл"}
	languageNames = map[string]string{"en": "English", "uk": "Українська", "ru": "Русский", "": "не указан"}
	contactNames  = map[string]string{"telegram": "Telegram", "email": "Почта", "github": "GitHub"}
)

// countryName says «Украина» for «UA» (the names of CLDR); a code it does not know stays a code.
func countryName(code string) string {
	if code == "" {
		return "не определена"
	}
	region, err := language.ParseRegion(code)
	if err != nil {
		return code
	}
	if name := countryNames.Name(region); name != "" {
		return name
	}
	return code
}

var countryNames = display.Russian.Regions()

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
	case uint64:
		return int64(min(number, math.MaxInt64))
	case float64:
		return int64(number)
	}
	return 0
}

// toNumber is toInt64 for what a template may pass as a share: floats stay fractional.
func toNumber(value any) float64 {
	switch number := value.(type) {
	case float64:
		return number
	case float32:
		return float64(number)
	}
	return float64(toInt64(value))
}

// formatBytes prints a size the way people read it: «1,4 МБ».
func formatBytes(value int64) string {
	units := [...]string{"Б", "КБ", "МБ", "ГБ", "ТБ"}
	size, unit := float64(value), 0
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d Б", value)
	}
	precision := 1
	if size >= 100 {
		precision = 0
	}
	number := strings.Replace(fmt.Sprintf("%.*f", precision, size), ".", ",", 1)
	return strings.TrimSuffix(number, ",0") + " " + units[unit]
}

// formatCount groups thousands with a thin space: 12 345.
func formatCount(value int64) string {
	digits := fmt.Sprint(value)
	if value < 0 {
		return digits
	}
	const thinSpace = rune(0x202F) // narrow no-break space: the number never wraps in the middle
	var out strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteRune(thinSpace)
		}
		out.WriteRune(digit)
	}
	return out.String()
}

// ago says how long ago something happened, roughly: «3 мин назад».
func ago(elapsed time.Duration) string {
	switch {
	case elapsed < 0 || elapsed > 100*365*24*time.Hour:
		return "—"
	case elapsed < time.Minute:
		return "только что"
	case elapsed < time.Hour:
		return fmt.Sprintf("%d мин назад", int(elapsed.Minutes()))
	case elapsed < 48*time.Hour:
		return fmt.Sprintf("%d ч назад", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%d дн. назад", int(elapsed.Hours()/24))
	}
}

// meter is a bar whose colour follows how full it is: calm, then a warning, then an alarm.
func meter(percent float64) template.HTML {
	tone := "accent"
	switch {
	case percent >= 90:
		tone = "error"
	case percent >= 80:
		tone = "warn"
	}
	return toneBar(percent, tone)
}

// usageRow is a line of the «server» card: how full memory or a disk is.
type usageRow struct {
	Name    string
	Percent float64
	Used    uint64
	Total   uint64
}

func usage(name string, total, free uint64) usageRow {
	row := usageRow{Name: name, Total: total}
	if total > 0 && free <= total {
		row.Used = total - free
		row.Percent = float64(row.Used) * 100 / float64(total)
	}
	return row
}
