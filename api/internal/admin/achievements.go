package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/achievements"
	"github.com/DenisHumen/krokosha-site/api/internal/clients"
)

// «Ачивки»: how rare every achievement is — the easter eggs among the players, the achievements of
// orders among the accounts — and a bench that shows the site's own banner of each kind, with its
// sound: the owner hears what a client hears. The banner is design/components/eggs/achievement.js,
// copied into static/ (a test keeps the two the same).

type achievementsData struct {
	Eggs    []achievements.Stat
	Updated time.Time
	Players int
	// EggCount: the eggs hidden on the site; AllFound: players who found every one; EggsDiscount:
	// what that gives once (content/site.yaml → loyalty.eggs).
	EggCount     int
	AllFound     int
	EggsDiscount int
	// Orders: the achievements of orders, with how many accounts have each.
	Orders   []orderStat
	Accounts int
	BigOrder float64
	Samples  []toastSample
}

type orderStat struct {
	ID      string
	Count   int
	Percent float64
}

// toastSample is a button of the bench: what it shows, and the banner (or banners) as JSON for
// static/achievements.js.
type toastSample struct {
	Label string
	Hint  string
	Toast string
}

// toast is what achievement.js unlock() takes.
type toast struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Text   string `json:"text"`
	Found  string `json:"found"`
	Count  int    `json:"count,omitempty"`
	Total  int    `json:"total,omitempty"`
	Rare   bool   `json:"rare"`
	Rarity string `json:"rarity,omitempty"`
	Sound  string `json:"sound"`
}

// orderNames are the achievements of orders as the owner reads them (the site's texts are
// content/site.yaml → account.orders).
var orderNames = map[string]string{
	clients.FirstOrder:  "Первый заказ",
	clients.SecondOrder: "Снова с нами — второй заказ",
	clients.BigOrder:    "Крупный проект",
	clients.AllOrders:   "Партнёр — все три",
}

// sharePhrase is the line under a banner: «Есть у 6,5 % игроков».
func sharePhrase(percent float64, whose string) string {
	number := strconv.FormatFloat(percent, 'f', 1, 64)
	number = strings.TrimSuffix(number, ".0")
	return "Есть у " + strings.Replace(number, ".", ",", 1) + " % " + whose
}

func (h *Handler) achievementsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := achievementsData{BigOrder: h.opts.Loyalty().BigOrder, EggCount: len(achievements.Eggs), EggsDiscount: h.opts.Loyalty().Eggs}
	eggShare := map[string]float64{}
	if h.opts.Achievements != nil {
		stats, updated, err := h.opts.Achievements.Stats(ctx)
		if err != nil {
			h.fail(w, r, "cannot read the achievements", err)
			return
		}
		data.Eggs, data.Updated = stats, updated
		for _, stat := range stats {
			data.Players = max(data.Players, stat.Players)
			if stat.ID == achievements.All {
				data.AllFound = stat.Found
			}
			if stat.Players >= achievements.MinPlayers {
				eggShare[stat.ID] = stat.Percent
			}
		}
	}
	orderShare := map[string]float64{}
	if h.opts.Clients != nil {
		counts, accounts, err := h.opts.Clients.OrderCounts(ctx)
		if err != nil {
			h.fail(w, r, "cannot count the achievements of orders", err)
			return
		}
		data.Accounts = accounts
		for _, id := range clients.OrderAchievements {
			stat := orderStat{ID: id, Count: counts[id]}
			if accounts > 0 {
				stat.Percent = float64(counts[id]) * 100 / float64(accounts)
			}
			if accounts >= clients.MinClients {
				orderShare[id] = stat.Percent
			}
			data.Orders = append(data.Orders, stat)
		}
	}
	data.Samples = h.samples(eggShare, orderShare, data.BigOrder)
	h.render(w, r, http.StatusOK, "achievements", view{Title: "Ачивки", Nav: "achievements", Data: data, Script: "achievements.js"})
}

// samples are the buttons of the bench: one banner of every kind, with the shares of the site when it
// has them (else ones that look like them), and three in a row — the queue a client may get.
func (h *Handler) samples(eggShare, orderShare map[string]float64, bigOrder float64) []toastSample {
	share := func(known map[string]float64, id string, fallback float64, whose string) string {
		if percent, ok := known[id]; ok {
			return sharePhrase(percent, whose)
		}
		return sharePhrase(fallback, whose)
	}
	const egg, order = "Пасхалка найдена", "Достижение получено"
	big := "Заказ от " + h.money(bigOrder)
	common := toast{ID: "console", Name: "Почти коллега", Text: "krokosha.hello() в консоли DevTools", Found: egg, Count: 3, Total: 8,
		Rarity: share(eggShare, "console", 36, "игроков"), Sound: "egg"}
	rare := toast{ID: "reboot", Name: "Выключи и включи", Text: "Аватар перезагрузился, как сервер", Found: egg, Count: 5, Total: 8, Rare: true,
		Rarity: share(eggShare, "reboot", 6, "игроков"), Sound: "rare"}
	every := toast{ID: "all", Name: "root@krokosha", Text: "Найдены все пасхалки", Found: egg, Rare: true,
		Rarity: share(eggShare, "all", 1.2, "игроков"), Sound: "epic"}
	first := toast{ID: clients.FirstOrder, Name: "Первый заказ", Text: "Первый заказ выполнен", Found: order, Count: 1, Total: 3,
		Rarity: share(orderShare, clients.FirstOrder, 40, "клиентов"), Sound: "egg"}
	large := toast{ID: clients.BigOrder, Name: "Крупный проект", Text: big, Found: order, Count: 2, Total: 3, Rare: true,
		Rarity: share(orderShare, clients.BigOrder, 7, "клиентов"), Sound: "rare"}
	partner := toast{ID: clients.AllOrders, Name: "Партнёр", Text: "Все достижения за заказы", Found: order, Rare: true,
		Rarity: share(orderShare, clients.AllOrders, 3, "клиентов"), Sound: "epic"}
	encode := func(value any) string {
		raw, _ := json.Marshal(value)
		return string(raw)
	}
	return []toastSample{
		{Label: "Обычная", Hint: "пасхалка, которая есть у многих: «динь»", Toast: encode(common)},
		{Label: "Редкая", Hint: "меньше 10 % игроков: золото, лучи и перезвон", Toast: encode(rare)},
		{Label: "Все пасхалки", Hint: "root@krokosha: золото и фанфара", Toast: encode(every)},
		{Label: "Первый заказ", Hint: "ачивка заказов в кабинете: обычная", Toast: encode(first)},
		{Label: "Крупный проект", Hint: "редкая ачивка заказов", Toast: encode(large)},
		{Label: "Партнёр", Hint: "все три ачивки заказов: фанфара", Toast: encode(partner)},
		{Label: "Три подряд", Hint: "очередь, как у клиента: одна за другой", Toast: encode([]toast{common, rare, every})},
	}
}
