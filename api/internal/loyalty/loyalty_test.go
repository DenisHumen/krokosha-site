package loyalty

import (
	"testing"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
)

// The rules Denis chose on 2026-09-24 (content/site.yaml → loyalty).
func rules() config.Loyalty {
	return config.Loyalty{
		Enabled: true, Currency: "USD", Welcome: 10, Eggs: 20,
		Tiers: []config.LoyaltyTier{
			{ID: "silver", Orders: 1, Spent: 1000, Discount: 5},
			{ID: "gold", Orders: 3, Spent: 5000, Discount: 10},
			{ID: "platinum", Orders: 6, Spent: 15000, Discount: 15},
		},
	}
}

var today = time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

func TestTheRulesAreValid(t *testing.T) {
	if err := rules().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*config.Loyalty){
		"percent over 100":     func(l *config.Loyalty) { l.Eggs = 120 },
		"negative percent":     func(l *config.Loyalty) { l.Welcome = -1 },
		"no currency":          func(l *config.Loyalty) { l.Currency = "" },
		"duplicate id":         func(l *config.Loyalty) { l.Tiers[1].ID = "silver" },
		"id with spaces":       func(l *config.Loyalty) { l.Tiers[0].ID = "Silver level" },
		"no condition":         func(l *config.Loyalty) { l.Tiers[0].Orders, l.Tiers[0].Spent = 0, 0 },
		"orders do not climb":  func(l *config.Loyalty) { l.Tiers[2].Orders = 3 },
		"spent does not climb": func(l *config.Loyalty) { l.Tiers[1].Spent = 900 },
		"discount goes down":   func(l *config.Loyalty) { l.Tiers[2].Discount = 7 },
	} {
		broken := rules()
		broken.Tiers = append([]config.LoyaltyTier{}, broken.Tiers...)
		change(&broken)
		if err := broken.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTierIsWhicheverComesFirst(t *testing.T) {
	for _, tc := range []struct {
		name   string
		orders int
		spent  float64
		want   string
	}{
		{"new client", 0, 0, ""},
		{"one small order", 1, 200, "silver"},
		{"two orders", 2, 900, "silver"},
		{"three orders, little money", 3, 1200, "gold"},
		{"one big order", 1, 5000, "gold"},
		{"a huge order", 1, 15000, "platinum"},
		{"six small orders", 6, 600, "platinum"},
		{"just below gold both ways", 2, 4999.99, "silver"},
	} {
		tier, ok := TierOf(rules(), History{Orders: tc.orders, Spent: tc.spent})
		if got := map[bool]string{true: tier.ID}[ok]; got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNextTier(t *testing.T) {
	next, ok := NextTier(rules(), History{})
	if !ok || next.Tier.ID != "silver" || next.Orders != 1 || next.Spent != 1000 || next.Progress != 0 {
		t.Errorf("from nothing: %+v %v", next, ok)
	}
	// Silver by one order; gold needs two more orders or $4000 more — the nearer condition fills the bar.
	next, ok = NextTier(rules(), History{Orders: 1, Spent: 3000})
	if !ok || next.Tier.ID != "gold" || next.Orders != 2 || next.Spent != 2000 || next.Progress != 0.6 {
		t.Errorf("from silver: %+v %v", next, ok)
	}
	if next, ok := NextTier(rules(), History{Orders: 9, Spent: 50000}); ok {
		t.Errorf("at the top: %+v", next)
	}
	// A level reached by money skips the one below it.
	next, ok = NextTier(rules(), History{Orders: 1, Spent: 6000})
	if !ok || next.Tier.ID != "platinum" {
		t.Errorf("gold by money: %+v %v", next, ok)
	}
}

func TestDecideGivesTheLargest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		history  History
		claim    Claim
		personal Personal
		want     Offer
	}{
		{"first request", History{}, Claim{}, Personal{}, Offer{10, ReasonWelcome, ""}},
		{"first request with every egg", History{}, Claim{Eggs: true}, Personal{}, Offer{20, ReasonEggs, ""}},
		{"second request while the first is open", History{Earlier: 1}, Claim{}, Personal{}, Offer{}},
		{"after a rejected request (not counted)", History{Earlier: 0}, Claim{}, Personal{}, Offer{10, ReasonWelcome, ""}},
		{"silver", History{Earlier: 1, Orders: 1, Spent: 300}, Claim{}, Personal{}, Offer{5, ReasonTier, "silver"}},
		{"silver with the eggs", History{Earlier: 1, Orders: 1}, Claim{Eggs: true}, Personal{}, Offer{20, ReasonEggs, ""}},
		{"eggs used before", History{Earlier: 1, Orders: 1, EggsUsed: true}, Claim{Eggs: true}, Personal{}, Offer{5, ReasonTier, "silver"}},
		{"platinum", History{Earlier: 6, Orders: 6}, Claim{}, Personal{}, Offer{15, ReasonTier, "platinum"}},
		{"personal above all", History{Earlier: 6, Orders: 6}, Claim{Eggs: true}, Personal{Percent: 30, Note: "партнёр"}, Offer{30, ReasonPersonal, "партнёр"}},
		{"personal below the tier", History{Earlier: 6, Orders: 6}, Claim{}, Personal{Percent: 7}, Offer{15, ReasonTier, "platinum"}},
		{"personal, expired", History{Earlier: 1}, Claim{}, Personal{Percent: 25, Until: today.AddDate(0, 0, -1)}, Offer{}},
		{"personal, its last day", History{Earlier: 1}, Claim{}, Personal{Percent: 25, Until: today}, Offer{25, ReasonPersonal, ""}},
	} {
		if got := Decide(rules(), tc.history, tc.claim, tc.personal, today.Add(15*time.Hour)); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}

	off := rules()
	off.Enabled = false
	if got := Decide(off, History{}, Claim{Eggs: true}, Personal{Percent: 50}, today); got != (Offer{}) {
		t.Errorf("switched off: %+v", got)
	}
}

func TestPublicReasons(t *testing.T) {
	for reason, want := range map[string]bool{ReasonWelcome: true, ReasonEggs: true, ReasonTier: false, ReasonPersonal: false, ReasonManual: false, "": false} {
		if Public(reason) != want {
			t.Errorf("Public(%q) = %v", reason, !want)
		}
	}
}
