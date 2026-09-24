package config

import (
	"errors"
	"fmt"
	"regexp"
)

// Loyalty is content/site.yaml → loyalty: the discounts of clients and the levels of regular ones.
// What the numbers mean is internal/loyalty's business.
type Loyalty struct {
	Enabled  bool          `yaml:"enabled"`
	Currency string        `yaml:"currency"` // what the owner enters the sums of orders in
	Welcome  int           `yaml:"welcome"`  // % on the first request
	Eggs     int           `yaml:"eggs"`     // % once, for every easter egg found
	Tiers    []LoyaltyTier `yaml:"tiers"`
	// BigOrder is the sum from which an order earns the «big project» achievement; 0 — never.
	BigOrder float64 `yaml:"big_order"`
	// Priority: how the admin area paints the conversations of a client by their orders; the site
	// never shows it. None in the content — the design's thresholds (DefaultPriority).
	Priority LoyaltyPriority `yaml:"priority"`
}

// LoyaltyPriority is loyalty.priority: the completed orders, or their sum, that make a client a
// regular one (Middle) and a key one (High) for the colour of their conversations in the admin area.
// One completed order makes a client «low»; none — «new».
type LoyaltyPriority struct {
	Middle PriorityLevel `yaml:"middle"`
	High   PriorityLevel `yaml:"high"`
}

// PriorityLevel is reached by Orders completed orders or by Spent in their sum, whichever comes
// first; 0 turns a condition off.
type PriorityLevel struct {
	Orders int     `yaml:"orders"`
	Spent  float64 `yaml:"spent"`
}

// DefaultPriority is the proposal of the design «компактный чат»: two orders or 3000 — a regular
// client, four or 8000 — a key one.
var DefaultPriority = LoyaltyPriority{Middle: PriorityLevel{Orders: 2, Spent: 3000}, High: PriorityLevel{Orders: 4, Spent: 8000}}

// PriorityRules are the thresholds of the content, or the design's when it names none.
func (l Loyalty) PriorityRules() LoyaltyPriority {
	if l.Priority == (LoyaltyPriority{}) {
		return DefaultPriority
	}
	return l.Priority
}

// LoyaltyTier is a level of a regular client, reached by Orders completed orders or by Spent in their
// sum, whichever comes first; 0 turns a condition off.
type LoyaltyTier struct {
	ID       string    `yaml:"id"`
	Name     Localized `yaml:"name"`
	Orders   int       `yaml:"orders"`
	Spent    float64   `yaml:"spent"`
	Discount int       `yaml:"discount"`
}

var reTierID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)

// Validate refuses rules that make no sense: percents outside 0–100, levels that do not climb.
// The site's build checks the same (web/src/lib/schema.ts), so a mistake never reaches a server.
func (l Loyalty) Validate() error {
	for name, percent := range map[string]int{"welcome": l.Welcome, "eggs": l.Eggs} {
		if percent < 0 || percent > 100 {
			return fmt.Errorf("loyalty.%s: %d is not a percent", name, percent)
		}
	}
	if l.Enabled && l.Currency == "" {
		return errors.New("loyalty.currency is required")
	}
	if l.BigOrder < 0 {
		return fmt.Errorf("loyalty.big_order: %v is not a sum", l.BigOrder)
	}
	if p := l.Priority; p != (LoyaltyPriority{}) {
		for name, level := range map[string]PriorityLevel{"middle": p.Middle, "high": p.High} {
			if level.Orders < 0 || level.Spent < 0 || (level.Orders == 0 && level.Spent == 0) {
				return fmt.Errorf("loyalty.priority.%s: needs orders or spent above zero", name)
			}
		}
		if (p.High.Orders > 0 && p.Middle.Orders > 0 && p.High.Orders <= p.Middle.Orders) ||
			(p.High.Spent > 0 && p.Middle.Spent > 0 && p.High.Spent <= p.Middle.Spent) {
			return errors.New("loyalty.priority.high: asks more than middle")
		}
	}
	seen := map[string]bool{}
	for i, tier := range l.Tiers {
		switch {
		case !reTierID.MatchString(tier.ID) || seen[tier.ID]:
			return fmt.Errorf("loyalty.tiers[%d]: id %q must be a unique lowercase word", i, tier.ID)
		case tier.Discount < 0 || tier.Discount > 100:
			return fmt.Errorf("loyalty.tiers.%s: %d is not a percent", tier.ID, tier.Discount)
		case tier.Orders < 0 || tier.Spent < 0 || (tier.Orders == 0 && tier.Spent == 0):
			return fmt.Errorf("loyalty.tiers.%s: needs orders or spent above zero", tier.ID)
		}
		seen[tier.ID] = true
		if i > 0 {
			prev := l.Tiers[i-1]
			if tier.Discount < prev.Discount || (tier.Orders > 0 && prev.Orders > 0 && tier.Orders <= prev.Orders) ||
				(tier.Spent > 0 && prev.Spent > 0 && tier.Spent <= prev.Spent) {
				return fmt.Errorf("loyalty.tiers.%s: every level asks more than the one before and gives no less", tier.ID)
			}
		}
	}
	return nil
}

// Tier returns the level with the given id.
func (l Loyalty) Tier(id string) (LoyaltyTier, bool) {
	for _, tier := range l.Tiers {
		if tier.ID == id {
			return tier, true
		}
	}
	return LoyaltyTier{}, false
}
