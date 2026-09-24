package admin

import (
	"context"
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/sysstatus"
	"github.com/DenisHumen/krokosha-site/api/internal/telegram"
)

// The frame of every page (design v3): the rail of sections on the left, and the ticker in the
// header — a few numbers worth seeing whatever screen is open.

// icons are the paths of the rail and the profile menu: 24×24, drawn with a stroke.
var icons = map[string]string{
	"overview":     "M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z",
	"leads":        "M4 6h16M4 12h16M4 18h10",
	"clients":      "M9 11a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM3 20c0-3.3 2.7-6 6-6s6 2.7 6 6M16 5.5a3 3 0 0 1 0 5.5M18 14.5c1.8.8 3 2.8 3 5.5",
	"inbox":        "M4 13l2.5-8h11L20 13v6H4zM4 13h5l1 2h4l1-2h5",
	"bot":          "M7 8h10a3 3 0 0 1 3 3v5a3 3 0 0 1-3 3H7a3 3 0 0 1-3-3v-5a3 3 0 0 1 3-3zM12 4v4M9 13v1M15 13v1",
	"mail":         "M4 6h16v12H4zM4 7l8 6 8-6",
	"achievements": "M8 4h8v5a4 4 0 0 1-8 0zM8 6H5a3 3 0 0 0 3 4M16 6h3a3 3 0 0 1-3 4M12 13v4M9 20h6",
	"visits":       "M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6S2 12 2 12zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z",
	"traffic":      "M4 19V5M4 19h16M8 15l3-4 3 2 5-6",
	"status":       "M3 12h4l2-5 4 10 2-5h6",
	"account":      "M14 10a4 4 0 1 0-3.5 4L10 15H8v2H6v2H3v-3l6.5-6.5",
	"logout":       "M14 4h5v16h-5M10 8l-4 4 4 4M6 12h10",
	"person":       "M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM4 21c0-4 3.6-7 8-7s8 3 8 7",
	"server":       "M12 2l8.7 5v10L12 22l-8.7-5V7z",
	"robot":        "M7 8h10a3 3 0 0 1 3 3v5a3 3 0 0 1-3 3H7a3 3 0 0 1-3-3v-5a3 3 0 0 1 3-3zM12 4v4",
	"out":          "M5 19L19 5M9 5h10v10",
	"keys":         "M9 6a3 3 0 1 0-3 3h12a3 3 0 1 0-3-3v12a3 3 0 1 0 3-3H6a3 3 0 1 0 3 3z",
	"templates":    "M5 4h14v16H5zM8 8h8M8 12h8M8 16h5",
	"backdrop":     "M4 7h2M9 7h1M13 7h3M4 12h1M8 12h3M14 12h2M4 17h3M10 17h1M14 17h2M19 12h1",
}

// icon draws one of them. The menu's icons are round at the joints, the rail's are square.
func icon(name string, round bool) template.HTML {
	path, ok := icons[name]
	if !ok {
		return ""
	}
	joins := `stroke-linecap="square" stroke-linejoin="miter"`
	if round {
		joins = `stroke-linecap="round" stroke-linejoin="round"`
	}
	svg := `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" ` + joins +
		` aria-hidden="true" focusable="false"><path d="` + path + `"></path></svg>`
	return template.HTML(svg) //nolint:gosec // constant paths of this file
}

// navItem is an icon of the rail.
type navItem struct {
	Key, Label, Path string
	Dot              bool // something waits there: new requests, letters without a request
}

func (h *Handler) nav(v view) []navItem {
	items := []navItem{
		{Key: "overview", Label: "Обзор", Path: "/"},
		{Key: "leads", Label: "Заявки", Path: "/leads", Dot: v.NewLeads > 0},
	}
	if v.HasClients {
		items = append(items, navItem{Key: "clients", Label: "Клиенты", Path: "/clients"})
	}
	if v.HasInbox {
		items = append(items, navItem{Key: "inbox", Label: "Входящие", Path: "/inbox", Dot: v.Letters > 0})
	}
	items = append(items, navItem{Key: "bot", Label: "Бот", Path: "/bot"})
	if v.HasMail {
		items = append(items, navItem{Key: "mail", Label: "Почта", Path: "/mail"})
	}
	if v.HasAchievements {
		items = append(items, navItem{Key: "achievements", Label: "Ачивки", Path: "/achievements"})
	}
	return append(items,
		navItem{Key: "visits", Label: "Визиты", Path: "/visits"},
		navItem{Key: "traffic", Label: "Трафик", Path: "/traffic"},
		navItem{Key: "status", Label: "Статус", Path: "/status"},
		navItem{Key: "account", Label: "Аккаунт", Path: "/account"},
	)
}

// tick is a number of the ticker; Warn paints it in the colour of trouble.
type tick struct {
	Key, Value, Path string
	Warn             bool
}

// headerCacheFor: the ticker is drawn on every page; its slower parts are read at most this often.
const headerCacheFor = 30 * time.Second

type headerCache struct {
	at     time.Time
	visits int
	vitals sysstatus.Vitals
}

// ticker gathers the two groups: the site (people now, visits of the week, requests, letters) and
// the server (processor, disk, bot, backup).
func (h *Handler) ticker(ctx context.Context, v view) [][]tick {
	h.headerMu.Lock()
	cached := h.header
	h.headerMu.Unlock()
	if time.Since(cached.at) > headerCacheFor {
		cached = headerCache{at: time.Now()}
		if h.opts.Reports != nil {
			today := h.opts.Reports.Today()
			totals, err := h.opts.Reports.Totals(ctx, analytics.Period{From: today.AddDate(0, 0, -6), To: today, Kind: "custom"})
			if err != nil {
				h.opts.Log.Warn("cannot count the visits of the week", "error", err)
			}
			cached.visits = totals.Visits
		}
		if h.opts.System != nil {
			cached.vitals = h.opts.System.Vitals()
		}
		h.headerMu.Lock()
		h.header = cached
		h.headerMu.Unlock()
	}

	site := []tick{
		{Key: "Сейчас", Value: fmt.Sprintf("%d онлайн", h.opts.Active(ctx, activeWindow)), Path: "/"},
		{Key: "Визиты 7д", Value: formatCount(int64(cached.visits)), Path: "/visits"},
		{Key: "Заявки", Value: "нет новых", Path: "/leads?status=new"},
	}
	if v.NewLeads > 0 {
		site[2].Value = plural(v.NewLeads, "новая", "новые", "новых")
	}
	if v.HasInbox {
		letters := "пусто"
		if v.Letters > 0 {
			letters = fmt.Sprint(v.Letters)
		}
		site = append(site, tick{Key: "Входящие", Value: letters, Path: "/inbox"})
	}

	vitals := cached.vitals
	server := []tick{
		{Key: "CPU", Value: fmt.Sprintf("%.0f%%", vitals.CPU), Path: "/status", Warn: vitals.CPU >= 90},
		{Key: "Диск", Value: fmt.Sprintf("%.0f%%", vitals.Disk), Path: "/status", Warn: vitals.Disk >= 90},
		h.botTick(),
		h.backupTick(vitals.Backup),
	}
	return [][]tick{site, server}
}

// telegramStatus asks how the bot is doing; false — there is no token, so no bot.
func telegramStatus(status func() (telegram.Status, bool)) (telegram.Status, bool) {
	if status == nil {
		return telegram.Status{}, false
	}
	return status()
}

func (h *Handler) botTick() tick {
	out := tick{Key: "Бот", Path: "/bot"}
	status, configured := telegramStatus(h.opts.BotStatus)
	switch {
	case !configured:
		out.Value = "нет токена"
	case status.Username == "" && status.LastError != "":
		out.Value, out.Warn = "нет связи", true
	case status.Username == "":
		out.Value = "подключается"
	case status.LastError != "" && status.LastErrorAt.After(status.ConnectedAt) && time.Since(status.LastErrorAt) < 10*time.Minute:
		out.Value, out.Warn = "ошибка", true
	default:
		out.Value = "онлайн"
	}
	return out
}

func (h *Handler) backupTick(backup sysstatus.Backup) tick {
	out := tick{Key: "Бэкап", Path: "/status"}
	switch {
	case !backup.Known:
		out.Value = "ещё не было"
	case !backup.OK:
		out.Value, out.Warn = "ошибка", true
	default:
		finished := backup.FinishedAt.In(h.opts.Location)
		now := time.Now().In(h.opts.Location)
		if finished.Year() == now.Year() && finished.YearDay() == now.YearDay() {
			out.Value = finished.Format("15:04")
		} else {
			out.Value = finished.Format("02.01 15:04")
		}
		out.Warn = now.Sub(finished) > 50*time.Hour // it runs every night
	}
	return out
}

// initials of a login for the avatar: «denis» → «DE», «olga.k» → «OK».
func initials(login string) string {
	var out []rune
	fields := strings.FieldsFunc(login, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	switch {
	case len(fields) >= 2:
		out = append(out, []rune(fields[0])[0], []rune(fields[1])[0])
	case len(fields) == 1:
		runes := []rune(fields[0])
		out = runes[:min(2, len(runes))]
	}
	return strings.ToUpper(string(out))
}

// step turns a percentage into the number of a size class (.h-37, .w-37): 0…100, whole.
func step(percent float64) int {
	if math.IsNaN(percent) || percent <= 0 {
		return 0
	}
	if percent < 1 {
		return 1 // a small share stays visible
	}
	return int(math.Min(100, math.Round(percent)))
}

// lasting says how long something has been going on, in one unit: «40 мин», «5 ч», «12 дн».
func lasting(since time.Time) string {
	elapsed := time.Since(since)
	switch {
	case since.IsZero() || elapsed < 0:
		return "—"
	case elapsed < time.Hour:
		return fmt.Sprintf("%d мин", int(elapsed.Minutes()))
	case elapsed < 48*time.Hour:
		return fmt.Sprintf("%d ч", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%d дн", int(elapsed.Hours()/24))
	}
}

// shortDuration prints a setting like «30 мин», «4 ч», «1 ч 30 мин».
func shortDuration(d time.Duration) string {
	minutes := int(d.Round(time.Minute).Minutes())
	switch {
	case minutes < 60:
		return fmt.Sprintf("%d мин", minutes)
	case minutes%60 == 0:
		return fmt.Sprintf("%d ч", minutes/60)
	default:
		return fmt.Sprintf("%d ч %d мин", minutes/60, minutes%60)
	}
}

// spanOf is lasting for a duration: «23 дн» of an uptime.
func spanOf(d time.Duration) string { return lasting(time.Now().Add(-d)) }

// siteURL is the public address of the site, for the links of templates; "" — not known.
func (h *Handler) siteURL() string {
	if h.opts.SiteHost == "" {
		return ""
	}
	return "https://" + h.opts.SiteHost
}
