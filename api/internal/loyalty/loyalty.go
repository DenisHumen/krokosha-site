// Package loyalty decides the discount a request gets (docs/architecture.md, 2026-09-24): 10% on a
// client's first request, 20% once for every easter egg found, and the levels of a regular client —
// Silver, Gold, Platinum — reached by the number of completed orders or by their sum, whichever comes
// first. Discounts do not add up: the largest one that applies is the one given. The owner may give
// a client a discount of their own, and set any discount on a request by hand.
//
// Everything here is arithmetic on what the caller read from the database; the rules themselves are
// content/site.yaml → loyalty (config.Loyalty), so the owner changes them without touching code.
package loyalty

import (
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
)

// Reasons of a discount: why a request got the percent it has.
const (
	ReasonWelcome  = "welcome"  // the client's first request
	ReasonTier     = "tier"     // the level of a regular client
	ReasonEggs     = "eggs"     // every easter egg found; once per client
	ReasonPersonal = "personal" // given to this client by the owner
	ReasonManual   = "manual"   // set on this request by hand
)

// History is what is known of a client when a request comes: earlier requests, completed orders,
// what they came to, the rewards already used.
type History struct {
	Earlier  int     // earlier requests, except the rejected ones and spam
	Orders   int     // completed orders, with those whose requests were anonymised since
	Spent    float64 // their sum, in the currency of the rules
	EggsUsed bool    // the one-time discount of the eggs went to an earlier request
}

// Personal is the discount the owner gave a client.
type Personal struct {
	Percent int
	Note    string
	Until   time.Time // zero — for good; else the last day it is valid on (a date, time zone of the owner)
	Once    bool      // it goes to one request, then it is spent
}

// Valid reports whether the discount applies on the given day.
func (p Personal) Valid(today time.Time) bool {
	if p.Percent <= 0 {
		return false
	}
	return p.Until.IsZero() || !dateOnly(today).After(dateOnly(p.Until))
}

func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// Claim is what the request itself brings: a verified receipt of every easter egg.
type Claim struct {
	Eggs bool
}

// Offer is the discount a request gets.
type Offer struct {
	Percent int
	Reason  string // one of the Reason… constants; "" — no discount
	// Detail: the id of the tier; the owner's note of a personal or a manual discount.
	Detail string
}

// TierOf returns the highest level the history reaches; ok is false below the first one.
func TierOf(rules config.Loyalty, h History) (tier config.LoyaltyTier, ok bool) {
	for _, candidate := range rules.Tiers {
		if reaches(candidate, h) {
			tier, ok = candidate, true
		}
	}
	return tier, ok
}

// reaches: whichever condition comes first.
func reaches(tier config.LoyaltyTier, h History) bool {
	return (tier.Orders > 0 && h.Orders >= tier.Orders) || (tier.Spent > 0 && h.Spent >= tier.Spent)
}

// Next is the level after the current one and what is still missing to reach it.
type Next struct {
	Tier   config.LoyaltyTier
	Orders int     // orders still to complete; 0 when the level has no such condition
	Spent  float64 // still to spend; 0 likewise
	// Progress, 0–1: the nearer of the two conditions, for a bar.
	Progress float64
}

// NextTier says what comes after the current level; ok is false at the top.
func NextTier(rules config.Loyalty, h History) (next Next, ok bool) {
	// The levels climb (config.Loyalty.Validate), so the next one is the first after the current
	// that is not reached yet.
	start := 0
	if current, reached := TierOf(rules, h); reached {
		for i, tier := range rules.Tiers {
			if tier.ID == current.ID {
				start = i + 1
			}
		}
	}
	for _, candidate := range rules.Tiers[start:] {
		if reaches(candidate, h) {
			continue
		}
		next = Next{Tier: candidate}
		if candidate.Orders > 0 {
			next.Orders = candidate.Orders - h.Orders
			next.Progress = float64(h.Orders) / float64(candidate.Orders)
		}
		if candidate.Spent > 0 {
			next.Spent = candidate.Spent - h.Spent
			next.Progress = max(next.Progress, h.Spent/candidate.Spent)
		}
		next.Progress = min(1, max(0, next.Progress))
		return next, true
	}
	return Next{}, false
}

// Decide gives a request the largest of the discounts that apply to it.
func Decide(rules config.Loyalty, h History, claim Claim, personal Personal, today time.Time) Offer {
	if !rules.Enabled {
		return Offer{}
	}
	best := Offer{}
	consider := func(percent int, reason, detail string) {
		if percent > best.Percent {
			best = Offer{Percent: percent, Reason: reason, Detail: detail}
		}
	}
	if h.Earlier == 0 && h.Orders == 0 {
		consider(rules.Welcome, ReasonWelcome, "")
	}
	if tier, ok := TierOf(rules, h); ok {
		consider(tier.Discount, ReasonTier, tier.ID)
	}
	if claim.Eggs && !h.EggsUsed {
		consider(rules.Eggs, ReasonEggs, "")
	}
	if personal.Valid(today) {
		consider(personal.Percent, ReasonPersonal, personal.Note)
	}
	return best
}

// Public reports whether a submitter who is not signed in may be told the reason: the first
// request and the eggs are theirs to know; a level or a personal discount would tell a stranger
// who typed somebody's address that the address belongs to a regular client.
func Public(reason string) bool { return reason == ReasonWelcome || reason == ReasonEggs }
