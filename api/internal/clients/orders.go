package clients

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Achievements of an account for its orders (docs/architecture.md, 2026-09-24), next to its easter
// eggs in client_achievements: the first completed order, a second one, an order of at least
// loyalty.big_order — in any order — and a golden one for all three. They give no discount: the
// levels of regular clients do that. An achievement once earned stays, whatever happens to the
// request later: a done order that goes back to work, a request anonymised after years.

// The achievements of orders.
const (
	FirstOrder  = "first_order"
	SecondOrder = "second_order"
	BigOrder    = "big_order"
	AllOrders   = "all_orders"
)

// OrderAchievements are the achievements of orders, in the order the account shows them.
var OrderAchievements = []string{FirstOrder, SecondOrder, BigOrder, AllOrders}

// IsOrderAchievement reports whether id is an achievement of orders.
func IsOrderAchievement(id string) bool {
	for _, known := range OrderAchievements {
		if known == id {
			return true
		}
	}
	return false
}

// MinClients: below it the share of accounts with an achievement is not shown — like MinPlayers of
// the eggs, it would say more about chance than about the achievement.
const MinClients = 10

// Earned is an achievement of orders an account has.
type Earned struct {
	ID  string    `json:"id"`
	At  time.Time `json:"at"`
	New bool      `json:"new"` // the client has not been shown it yet
}

// AwardOrders gives an account the achievements its orders have earned and it does not have yet.
func (s *Service) AwardOrders(ctx context.Context, clientID int64) error {
	if clientID <= 0 {
		return nil
	}
	orders, biggest, err := s.opts.Leads.OrderFacts(ctx, clientID)
	if err != nil {
		return err
	}
	threshold := s.opts.Leads.Rules().BigOrder
	earned := map[string]bool{
		FirstOrder:  orders >= 1,
		SecondOrder: orders >= 2,
		BigOrder:    threshold > 0 && biggest >= threshold,
	}
	now := s.now()
	for _, id := range []string{FirstOrder, SecondOrder, BigOrder} {
		if !earned[id] {
			continue
		}
		if _, err := s.opts.DB.ExecContext(ctx, `INSERT IGNORE INTO client_achievements (client_id, id, found_at) VALUES (?, ?, ?)`,
			clientID, id, now); err != nil {
			return err
		}
	}
	// All three — earned now or before (an order that went back to work keeps what it earned).
	if _, err := s.opts.DB.ExecContext(ctx, `
		INSERT IGNORE INTO client_achievements (client_id, id, found_at)
		SELECT ?, ?, ? FROM client_achievements WHERE client_id = ? AND id IN (?, ?, ?) HAVING COUNT(*) = 3`,
		clientID, AllOrders, now, clientID, FirstOrder, SecondOrder, BigOrder); err != nil {
		return err
	}
	return nil
}

// AwardOrdersOfLead is AwardOrders for the account a request belongs to, if any: what the leads
// store calls after every change of a request (main.go → leads.Store.OnChange).
func (s *Service) AwardOrdersOfLead(ctx context.Context, leadID int64) error {
	var clientID sql.NullInt64
	err := s.opts.DB.QueryRowContext(ctx, `SELECT client_id FROM leads WHERE id = ?`, leadID).Scan(&clientID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !clientID.Valid) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.AwardOrders(ctx, clientID.Int64)
}

// Orders lists the achievements of orders of an account, in the order they were earned.
func (s *Service) Orders(ctx context.Context, clientID int64) ([]Earned, error) {
	rows, err := s.opts.DB.QueryContext(ctx, `
		SELECT id, found_at, seen_at IS NULL FROM client_achievements
		WHERE client_id = ? AND id IN (?, ?, ?, ?) ORDER BY found_at, FIELD(id, ?, ?, ?, ?)`,
		clientID, FirstOrder, SecondOrder, BigOrder, AllOrders, FirstOrder, SecondOrder, BigOrder, AllOrders)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Earned
	for rows.Next() {
		var earned Earned
		if err := rows.Scan(&earned.ID, &earned.At, &earned.New); err != nil {
			return nil, err
		}
		out = append(out, earned)
	}
	return out, rows.Err()
}

// MarkShown notes that the client has been shown these achievements: they are no longer new.
func (s *Service) MarkShown(ctx context.Context, clientID int64, ids []string) error {
	now := s.now()
	for _, id := range ids {
		if !IsOrderAchievement(id) {
			continue
		}
		if _, err := s.opts.DB.ExecContext(ctx, `UPDATE client_achievements SET seen_at = ? WHERE client_id = ? AND id = ? AND seen_at IS NULL`,
			now, clientID, id); err != nil {
			return err
		}
	}
	return nil
}

// OrderShares is how many of the accounts have each achievement of orders, in per cent — empty
// while there are fewer than MinClients accounts.
func (s *Service) OrderShares(ctx context.Context) (map[string]float64, error) {
	counts, accounts, err := s.OrderCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]float64{}
	if accounts < MinClients {
		return out, nil
	}
	for _, id := range OrderAchievements {
		out[id] = min(100, float64(counts[id]*10000/accounts)/100)
	}
	return out, nil
}

// OrderCounts is what the admin area shows: how many accounts have each achievement of orders, and
// how many accounts there are.
func (s *Service) OrderCounts(ctx context.Context) (counts map[string]int, accounts int, err error) {
	counts = map[string]int{}
	if err := s.opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM clients`).Scan(&accounts); err != nil {
		return nil, 0, err
	}
	rows, err := s.opts.DB.QueryContext(ctx, `
		SELECT id, COUNT(*) FROM client_achievements WHERE id IN (?, ?, ?, ?) GROUP BY id`,
		FirstOrder, SecondOrder, BigOrder, AllOrders)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, 0, err
		}
		counts[id] = n
	}
	return counts, accounts, rows.Err()
}
