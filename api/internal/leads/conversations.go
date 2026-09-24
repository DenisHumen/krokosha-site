package leads

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
)

// The «Заявки» screen of the admin area is a messenger (design «компактный чат»): a list of
// conversations on the left, one of them on the right. A line of the list says who wrote and about
// what, how much is at stake, what state it is in, and what the staff have not read yet — whatever
// channel the client wrote through, the site is where every message is seen.

// Conversation is a line of the list.
type Conversation struct {
	Summary
	Subject  string
	ClientID int64
	// Unread: messages of the client since the staff last opened the request (staff_seen_at).
	Unread int
	// LastAt: the last message of the conversation, or when the request came.
	LastAt time.Time
	// Waiting: open, and the last word is the client's — an answer is owed.
	Waiting bool
	// Orders and Spent: the completed orders of the client — of the account, or of everybody who
	// left the same contact — for the priority of the line.
	Orders int
	Spent  float64
}

// Tabs of the list.
const (
	TabAll        = ""
	TabUnanswered = "unanswered" // open, the last word is the client's
	TabWork       = "work"       // new and in progress
	TabWaiting    = "waiting"    // waiting for the client
	TabClosed     = "closed"     // done and rejected
	TabSpam       = "spam"
)

// Tabs in the order of the screen.
var Tabs = []string{TabAll, TabUnanswered, TabWork, TabWaiting, TabClosed, TabSpam}

// ConversationFilter narrows the list.
type ConversationFilter struct {
	Tab   string
	Query string // the number, a name, a contact, words of the description or the subject
	ID    int64  // one conversation, whatever its tab
	Limit int
}

// lastDirection is the direction of the last message that is not a note.
const lastDirection = `COALESCE((SELECT m.direction FROM lead_messages m WHERE m.lead_id = l.id AND m.direction <> 'note' ORDER BY m.id DESC LIMIT 1), '')`

// ConversationsLimit: the list shows this many at most, the latest first; the search finds the rest.
const ConversationsLimit = 300

// Conversations lists requests for the messenger screen, the latest conversation first.
func (s *Store) Conversations(ctx context.Context, filter ConversationFilter) ([]Conversation, error) {
	where, having, args := []string{"1 = 1"}, "", []any{}
	switch {
	case filter.ID > 0:
		where = append(where, "l.id = ?")
		args = append(args, filter.ID)
	case filter.Tab == TabUnanswered:
		where = append(where, "l.status IN ('new', 'in_progress', 'waiting_client')")
		having = " HAVING last_direction = 'in'"
	case filter.Tab == TabWork:
		where = append(where, "l.status IN ('new', 'in_progress')")
	case filter.Tab == TabWaiting:
		where = append(where, "l.status = 'waiting_client'")
	case filter.Tab == TabClosed:
		where = append(where, "l.status IN ('done', 'rejected')")
	case filter.Tab == TabSpam:
		where = append(where, "l.status = 'spam'")
	default:
		where = append(where, "l.status <> 'spam'")
	}
	if query := strings.TrimSpace(filter.Query); query != "" && filter.ID == 0 {
		if id, ok := parseNumber(query); ok {
			where = append(where, "l.id = ?")
			args = append(args, id)
		} else {
			like := "%" + escapeLike(query) + "%"
			where = append(where, "(l.name LIKE ? OR l.contact_value LIKE ? OR l.description LIKE ? OR l.subject LIKE ?)")
			args = append(args, like, like, like, like)
		}
	}
	if filter.Limit <= 0 {
		filter.Limit = ConversationsLimit
	}
	//nolint:gosec // the conditions are built from the constants above
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.status, l.created_at, l.updated_at, l.name, l.contact_method, l.contact_value, l.direction,
		       COALESCE(l.budget, ''), LEFT(l.description, 240), COALESCE(l.assignee, ''), l.kind, l.discount_percent,
		       COALESCE(l.subject, ''), COALESCE(l.client_id, 0),
		       (SELECT COUNT(*) FROM lead_messages m WHERE m.lead_id = l.id AND m.direction = 'in'
		          AND m.created_at > COALESCE(l.staff_seen_at, '1000-01-01')) AS unread,
		       COALESCE((SELECT MAX(m.created_at) FROM lead_messages m WHERE m.lead_id = l.id AND m.direction <> 'note'), l.created_at) AS last_at,
		       `+lastDirection+` AS last_direction,
		       CASE WHEN l.client_id IS NOT NULL THEN
		              (SELECT COUNT(*) FROM leads d WHERE d.client_id = l.client_id AND d.kind = 'request' AND d.status = 'done')
		              + COALESCE((SELECT c.orders_carried FROM clients c WHERE c.id = l.client_id), 0)
		            ELSE (SELECT COUNT(*) FROM leads d WHERE d.client_id IS NULL AND d.kind = 'request' AND d.status = 'done'
		              AND d.contact_method = l.contact_method AND d.contact_value = l.contact_value AND d.contact_value <> '') END AS orders,
		       CASE WHEN l.client_id IS NOT NULL THEN
		              (SELECT COALESCE(SUM(d.amount), 0) FROM leads d WHERE d.client_id = l.client_id AND d.kind = 'request' AND d.status = 'done')
		              + COALESCE((SELECT c.spent_carried FROM clients c WHERE c.id = l.client_id), 0)
		            ELSE (SELECT COALESCE(SUM(d.amount), 0) FROM leads d WHERE d.client_id IS NULL AND d.kind = 'request' AND d.status = 'done'
		              AND d.contact_method = l.contact_method AND d.contact_value = l.contact_value AND d.contact_value <> '') END AS spent
		FROM leads l WHERE `+strings.Join(where, " AND ")+having+`
		ORDER BY last_at DESC, l.id DESC LIMIT ?`, append(args, filter.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		var item Conversation
		var last string
		if err := rows.Scan(&item.ID, &item.Status, &item.CreatedAt, &item.UpdatedAt, &item.Name, &item.ContactMethod, &item.ContactValue,
			&item.Direction, &item.Budget, &item.Excerpt, &item.Assignee, &item.Kind, &item.Discount, &item.Subject, &item.ClientID,
			&item.Unread, &item.LastAt, &last, &item.Orders, &item.Spent); err != nil {
			return nil, err
		}
		item.Waiting = last == "in" && isOpen(item.Status)
		item.LastFromUser = item.Waiting && item.Status != StatusNew
		out = append(out, item)
	}
	return out, rows.Err()
}

// Conversation reads one line of the list, whatever its tab.
func (s *Store) Conversation(ctx context.Context, id int64) (Conversation, error) {
	items, err := s.Conversations(ctx, ConversationFilter{ID: id, Limit: 1})
	if err != nil {
		return Conversation{}, err
	}
	if len(items) == 0 {
		return Conversation{}, ErrNotFound
	}
	return items[0], nil
}

func isOpen(status string) bool {
	return status == StatusNew || status == StatusInProgress || status == StatusWaitingClient
}

// ConversationCounts are the numbers of the tabs.
func (s *Store) ConversationCounts(ctx context.Context) (map[string]int, error) {
	var all, unanswered, work, waiting, closed, spam int
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(status <> 'spam'), 0),
		       COALESCE(SUM(status IN ('new', 'in_progress', 'waiting_client') AND last_direction = 'in'), 0),
		       COALESCE(SUM(status IN ('new', 'in_progress')), 0), COALESCE(SUM(status = 'waiting_client'), 0),
		       COALESCE(SUM(status IN ('done', 'rejected')), 0), COALESCE(SUM(status = 'spam'), 0)
		FROM (SELECT l.status, `+lastDirection+` AS last_direction FROM leads l) l`).
		Scan(&all, &unanswered, &work, &waiting, &closed, &spam)
	return map[string]int{
		TabAll: all, TabUnanswered: unanswered, TabWork: work, TabWaiting: waiting, TabClosed: closed, TabSpam: spam,
	}, err
}

// UnreadTotal is how many messages of clients the staff have not read, across all requests but
// spam: the dot of «Заявки» in the rail.
func (s *Store) UnreadTotal(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM lead_messages m JOIN leads l ON l.id = m.lead_id
		WHERE m.direction = 'in' AND l.status <> 'spam' AND m.created_at > COALESCE(l.staff_seen_at, '1000-01-01')`).Scan(&n)
	return n, err
}

// MarkStaffSeen notes that the staff opened the conversation: what the client wrote up to «at» —
// the moment it was read for the screen — is read. A message that came a moment later stays unread.
func (s *Store) MarkStaffSeen(ctx context.Context, id int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE leads SET staff_seen_at = GREATEST(COALESCE(staff_seen_at, ?), ?) WHERE id = ?`, at.UTC(), at.UTC(), id)
	return err
}

// Pulse is what the open messenger asks every little while: has a client written since?
type Pulse struct {
	// Last is the newest message of a client anywhere (a new request brings one too); LeadLast the
	// newest of the conversation that is open. They only grow: a change is news.
	Last     int64 `json:"last"`
	LeadLast int64 `json:"lead_last"`
	Unread   int   `json:"unread"`
}

// PulseOf reads the pulse; lead is the open conversation, 0 — none.
func (s *Store) PulseOf(ctx context.Context, lead int64) (Pulse, error) {
	var p Pulse
	var last, leadLast sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT MAX(id) FROM lead_messages WHERE direction = 'in'),
		       (SELECT MAX(id) FROM lead_messages WHERE direction = 'in' AND lead_id = ?)`, lead).Scan(&last, &leadLast)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	p.Last, p.LeadLast = last.Int64, leadLast.Int64
	p.Unread, err = s.UnreadTotal(ctx)
	return p, err
}

// --- priority ------------------------------------------------------------------------------------

// Priority of a conversation, 0–3: how much the client has ordered, and how big this request is.
// The line of the list and the chip of the conversation are painted by it, and the list may be
// sorted by it. The thresholds are a proposal of the design («компактный чат»).
const (
	PriorityNew = iota // a new client, a small or unknown budget
	PriorityLow
	PriorityMid
	PriorityHigh
)

// Activity is what the client's orders say: 4 orders or $8000 — a key client; 2 or $3000 — a
// regular one; 1 — ordered before; none — new.
func Activity(orders int, spent float64) int {
	switch {
	case orders >= 4 || spent >= 8000:
		return PriorityHigh
	case orders >= 2 || spent >= 3000:
		return PriorityMid
	case orders >= 1:
		return PriorityLow
	}
	return PriorityNew
}

// BudgetLevel places a budget among the options of the form (their English labels, the way a
// request keeps its budget): the cheapest 0, the dearest 3. An option without a figure («not sure
// yet») and a budget not chosen are 0.
func BudgetLevel(options []string, budget string) int {
	var priced []string
	for _, option := range options {
		if strings.IndexFunc(option, unicode.IsDigit) >= 0 {
			priced = append(priced, option)
		}
	}
	for i, option := range priced {
		if option == budget && budget != "" {
			if len(priced) == 1 {
				return PriorityNew
			}
			return int(math.Round(float64(i*PriorityHigh) / float64(len(priced)-1)))
		}
	}
	return PriorityNew
}

// Priority combines the two, halfway rounded up.
func Priority(activity, budget int) int { return (activity + budget + 1) / 2 }
