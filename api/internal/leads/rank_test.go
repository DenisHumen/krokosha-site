package leads

import (
	"testing"
	"time"
)

// pool is a few templates of each language, the way the migration seeds them.
func pool() []Template {
	id := int64(0)
	make := func(lang, title, category, moment string, keywords ...string) Template {
		id++
		return Template{ID: id, Kind: "reply", Lang: lang, Title: title, Category: category, Moment: moment, Keywords: keywords, Body: title}
	}
	return []Template{
		make("ru", "Приветствие", "greeting", MomentFirst, "здравств", "привет", "добрый день"),
		make("ru", "Изучу и отвечу сегодня", "greeting", MomentFirst, "здравств", "добрый день"),
		make("ru", "Нужны детали", "details", MomentAny, "не знаю", "подскажите"),
		make("ru", "Стоимость", "pricing", MomentAny, "цен", "стоим", "сколько стоит", "сколько будет", "стоить", "бюджет"),
		make("ru", "Сроки", "timeline", MomentAny, "срок", "когда", "срочн", "дедлайн"),
		make("ru", "Примеры работ", "portfolio", MomentAny, "портфолио", "пример", "кейс", "делали"),
		make("ru", "Коммерческое предложение", "offer", MomentTalk, "кп", "коммерческ", "предложен"),
		make("ru", "Напомнить о себе", "followup", MomentWaiting),
		make("ru", "Спасибо за заказ", "thanks", MomentDone, "спасибо"),
		make("uk", "Привітання", "greeting", MomentFirst, "вітаю", "добрий день"),
		make("uk", "Зможу допомогти", "help", MomentAny, "зможете", "можете допомогти", "допоможете"),
		make("uk", "Приклади робіт", "portfolio", MomentAny, "портфоліо", "приклад", "кейс"),
		make("uk", "Що я вмію", "skills", MomentAny, "вмієте", "стек", "kubernetes", "docker"),
		make("uk", "Вартість", "pricing", MomentAny, "ціна", "вартіст", "скільки коштує"),
		make("en", "Pricing", "pricing", MomentAny, "price", "cost", "how much", "budget"),
		make("en", "Support after the work", "support", MomentAny, "support", "maintenance", "monitoring"),
		make("en", "Timeline", "timeline", MomentAny, "deadline", "how long", "when", "urgent"),
		make("en", "Greeting", "greeting", MomentFirst, "hello", "hi"),
	}
}

// firstThree: the titles of the templates shown first.
func firstThree(list []Suggestion) []string {
	var out []string
	for _, item := range list {
		if item.Top {
			out = append(out, item.Template.Title)
		}
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, item := range a {
		seen[item]++
	}
	for _, item := range b {
		seen[item]--
	}
	for _, count := range seen {
		if count != 0 {
			return false
		}
	}
	return true
}

func TestRankFindsWhatTheClientAsks(t *testing.T) {
	for name, tc := range map[string]struct {
		situation Situation
		top       []string
		first     string
	}{
		"a first request about the price and the time": {
			situation: Situation{Lang: "ru", Moment: MomentFirst, Last: "Здравствуйте! Сколько будет стоить настройка сети и когда сможете начать?"},
			top:       []string{"Стоимость", "Приветствие", "Сроки"},
			first:     "Стоимость",
		},
		"a Ukrainian asks for help and examples": {
			situation: Situation{Lang: "uk", Moment: MomentTalk, Last: "Чи можете допомогти з кластером Kubernetes? Які приклади робіт маєте?"},
			top:       []string{"Зможу допомогти", "Приклади робіт", "Що я вмію"},
		},
		"the last message counts more than the description": {
			situation: Situation{Lang: "en", Moment: MomentTalk, Last: "How much would the support cost per month?", Earlier: "We need monitoring of 20 servers, urgent."},
			top:       []string{"Pricing", "Support after the work", "Timeline"},
			first:     "Pricing",
		},
		"the client is silent": {
			situation: Situation{Lang: "ru", Moment: MomentWaiting, Last: "Нужно перестроить сеть офиса."},
			top:       []string{"Напомнить о себе"},
		},
		"the order is done": {
			situation: Situation{Lang: "ru", Moment: MomentDone, Last: "Спасибо, всё работает!"},
			top:       []string{"Спасибо за заказ"},
		},
	} {
		got := Rank(pool(), tc.situation)
		if !sameSet(firstThree(got), tc.top) {
			t.Errorf("%s: the first three %v, want %v", name, firstThree(got), tc.top)
		}
		if tc.first != "" && got[0].Template.Title != tc.first {
			t.Errorf("%s: the first is %q, want %q", name, got[0].Template.Title, tc.first)
		}
		for i, item := range got {
			if item.Template.Lang != tc.situation.Lang {
				t.Errorf("%s: a template of another language: %q", name, item.Template.Title)
			}
			if i > 0 && item.Top && !got[i-1].Top {
				t.Errorf("%s: the first three are not first", name)
			}
		}
	}
}

func TestRankExplainsItself(t *testing.T) {
	got := Rank(pool(), Situation{Lang: "ru", Moment: MomentFirst, Last: "Добрый день. Какова стоимость? Пришлите КП."})
	reasons := map[string][]Reason{}
	for _, item := range got {
		reasons[item.Template.Title] = item.Reasons
	}
	if r := reasons["Стоимость"]; len(r) != 1 || r[0] != (Reason{Kind: "word", Text: "стоимость"}) {
		t.Errorf("why «Стоимость»: %+v", r)
	}
	if r := reasons["Приветствие"]; len(r) != 2 || r[0] != (Reason{Kind: "word", Text: "добрый день"}) || r[1] != (Reason{Kind: "moment", Text: MomentFirst}) {
		t.Errorf("why «Приветствие»: %+v", r)
	}
	// «КП» is a whole word; the offer is for a conversation already going, so it does not come up first.
	if r := reasons["Коммерческое предложение"]; len(r) != 1 || r[0].Text != "кп" {
		t.Errorf("why «Коммерческое предложение»: %+v", r)
	}
	if _, ok := find(normalize("КПД сервера"), "кп"); ok {
		t.Error("«кп» found in «КПД»")
	}
	if word, ok := find(normalize("Всё ПРИВЕТ"), "привет"); !ok || word != "привет" {
		t.Errorf("found %q", word)
	}
}

func TestRankKnowsWhatWasSentAndForWhom(t *testing.T) {
	templates := pool()
	situation := Situation{Lang: "ru", Moment: MomentTalk, Last: "Сколько будет стоить и какие сроки?"}
	fresh := Rank(templates, situation)
	if fresh[0].Template.Title != "Стоимость" {
		t.Fatalf("without history: %v", firstThree(fresh))
	}
	var priceID int64
	for _, item := range templates {
		if item.Title == "Стоимость" {
			priceID = item.ID
		}
	}
	situation.Sent = map[int64]bool{priceID: true}
	again := Rank(templates, situation)
	if again[0].Template.Title != "Сроки" {
		t.Errorf("the price was sent already, yet it is first: %v", firstThree(again))
	}

	// A template for networks goes up for networks, down for anything else.
	special := append(pool(), Template{ID: 100, Kind: "reply", Lang: "ru", Title: "Сети: обследование", Category: "options", Moment: MomentAny, Directions: []string{"networks"}})
	for direction, want := range map[string]bool{"networks": true, "devops": false} {
		got := Rank(special, Situation{Lang: "ru", Moment: MomentTalk, Direction: direction, Last: "Нужна помощь"})
		found := false
		for _, item := range got {
			if item.Template.ID == 100 {
				found = item.Top
			}
		}
		if found != want {
			t.Errorf("a template for networks, the request about %s: top %v", direction, found)
		}
	}

	// Two greetings are one idea: only one of them among the first three.
	first := Rank(templates, Situation{Lang: "ru", Moment: MomentFirst, Last: "Здравствуйте, добрый день"})
	greetings := 0
	for _, item := range first {
		if item.Top && item.Template.Category == "greeting" {
			greetings++
		}
	}
	if greetings != 1 {
		t.Errorf("greetings among the first three: %d", greetings)
	}
}

func TestSituationOfACard(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	lead := &Lead{Status: StatusWaitingClient, Submission: Submission{Lang: "uk", Direction: "devops", Description: "Потрібен CI/CD."}}
	card := &Card{Lead: lead, Feed: []Entry{
		{Kind: "message", Direction: "in", Channel: "form", Body: "Потрібен CI/CD.", At: now.Add(-72 * time.Hour)},
		{Kind: "message", Direction: "out", Channel: "email", Body: "Які сервери?", At: now.Add(-50 * time.Hour)},
	}}
	card.FirstResponseAt.Valid = true
	got := SituationOf(card, nil, now)
	if got.Moment != MomentWaiting || got.Last != "Потрібен CI/CD." || got.Earlier != "" || got.Lang != "uk" || got.Direction != "devops" {
		t.Errorf("silent client: %+v", got)
	}
	card.Feed = append(card.Feed, Entry{Kind: "message", Direction: "in", Channel: "telegram", Body: "Три сервери. Скільки коштує?", At: now.Add(-time.Hour)})
	lead.Status = StatusInProgress
	if got = SituationOf(card, nil, now); got.Moment != MomentTalk || got.Last != "Три сервери. Скільки коштує?" || got.Earlier != "Потрібен CI/CD." {
		t.Errorf("the conversation goes on: %+v", got)
	}
	card.FirstResponseAt.Valid = false
	if got = SituationOf(card, nil, now); got.Moment != MomentFirst {
		t.Errorf("nothing answered yet: %+v", got)
	}
}
