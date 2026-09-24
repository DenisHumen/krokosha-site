package clients

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/achievements"
	"github.com/DenisHumen/krokosha-site/api/internal/leads"
)

// --- the easter eggs of an account ------------------------------------------------------------------

// Found is an achievement of an account.
type Found struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

// SyncEggs brings the eggs a browser found into the account, by their receipts: the account keeps
// them across devices. Receipts that do not verify are skipped without a word.
func (s *Service) SyncEggs(ctx context.Context, clientID int64, receipts []string) ([]Found, error) {
	for i, text := range receipts {
		if i >= len(achievements.Eggs)+1 {
			break
		}
		receipt, err := achievements.Verify(s.opts.Secret, text)
		if err != nil || receipt.ID == achievements.All {
			continue
		}
		// Found in a browser, which showed it then: seen.
		if _, err := s.opts.DB.ExecContext(ctx, `INSERT INTO client_achievements (client_id, id, found_at, seen_at) VALUES (?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE found_at = LEAST(found_at, VALUES(found_at))`, clientID, receipt.ID, receipt.At, receipt.At); err != nil {
			return nil, err
		}
	}
	return s.Eggs(ctx, clientID)
}

// Eggs lists the eggs of an account, in the order they were found.
func (s *Service) Eggs(ctx context.Context, clientID int64) ([]Found, error) {
	rows, err := s.opts.DB.QueryContext(ctx, `SELECT id, found_at FROM client_achievements WHERE client_id = ? ORDER BY found_at, id`, clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Found
	for rows.Next() {
		var found Found
		if err := rows.Scan(&found.ID, &found.At); err != nil {
			return nil, err
		}
		if achievements.IsEgg(found.ID) {
			out = append(out, found)
		}
	}
	return out, rows.Err()
}

// HasEveryEgg reports whether the account found every egg — on whatever devices.
func (s *Service) HasEveryEgg(ctx context.Context, clientID int64) (bool, error) {
	eggs, err := s.Eggs(ctx, clientID)
	return len(eggs) == len(achievements.Eggs), err
}

// --- the admin area ----------------------------------------------------------------------------------

// Row is a line of the admin's list of clients.
type Row struct {
	ID           int64
	Name         string
	Company      string
	Email        string
	Telegram     string // @username, or the id when there is no name
	CreatedAt    time.Time
	LastSeenAt   sql.NullTime
	Disabled     bool
	Personal     int
	Requests     int // requests and inquiries, spam aside
	Open         int
	Orders       int // completed orders, the carried ones included
	Spent        float64
	LastActivity time.Time
}

// Filter narrows the admin's list.
type Filter struct {
	Query  string
	Limit  int
	Offset int
}

// List returns the accounts, the most recently active first, and how many match.
func (s *Service) List(ctx context.Context, filter Filter) ([]Row, int, error) {
	where, args := "1 = 1", []any{}
	if query := strings.TrimSpace(filter.Query); query != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query) + "%"
		where = `(c.name LIKE ? OR c.company LIKE ? OR c.email LIKE ? OR c.telegram_username LIKE ? OR c.note LIKE ?
			OR EXISTS (SELECT 1 FROM client_contacts k WHERE k.client_id = c.id AND k.value LIKE ?))`
		args = append(args, like, like, like, like, like, like)
		if id, ok := parseClientNumber(query); ok {
			where = "(" + where + " OR c.id = ?)"
			args = append(args, id)
		}
	}
	var total int // the condition is built from constants above
	if err := s.opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM clients c WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	out, err := s.rows(ctx, where, args, filter.Limit, filter.Offset)
	return out, total, err
}

// Summary is one account the way the list shows it: for the card of a request.
func (s *Service) Summary(ctx context.Context, id int64) (*Row, error) {
	out, err := s.rows(ctx, "c.id = ?", []any{id}, 1, 0)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return &out[0], nil
}

// rows reads accounts with their requests; where is built from constants by the callers.
func (s *Service) rows(ctx context.Context, where string, args []any, limit, offset int) ([]Row, error) {
	//nolint:gosec // as above
	rows, err := s.opts.DB.QueryContext(ctx, `
		SELECT c.id, c.name, c.company, COALESCE(c.email, ''), COALESCE(c.telegram_username, ''), COALESCE(c.telegram_id, 0),
		       c.created_at, c.last_seen_at, c.disabled_at IS NOT NULL, COALESCE(c.personal_discount, 0), c.orders_carried, c.spent_carried,
		       COUNT(l.id), COALESCE(SUM(l.status IN ('new', 'in_progress', 'waiting_client')), 0),
		       COALESCE(SUM(l.kind = 'request' AND l.status = 'done'), 0), COALESCE(SUM(IF(l.kind = 'request' AND l.status = 'done', COALESCE(l.amount, 0), 0)), 0),
		       GREATEST(COALESCE(MAX(l.updated_at), c.created_at), COALESCE(c.last_seen_at, c.created_at)) AS activity
		FROM clients c LEFT JOIN leads l ON l.client_id = c.id AND l.status <> 'spam'
		WHERE `+where+` GROUP BY c.id ORDER BY activity DESC, c.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var row Row
		var telegramID int64
		var carriedOrders int
		var carriedSpent float64
		if err := rows.Scan(&row.ID, &row.Name, &row.Company, &row.Email, &row.Telegram, &telegramID, &row.CreatedAt, &row.LastSeenAt, &row.Disabled,
			&row.Personal, &carriedOrders, &carriedSpent, &row.Requests, &row.Open, &row.Orders, &row.Spent, &row.LastActivity); err != nil {
			return nil, err
		}
		if row.Telegram != "" {
			row.Telegram = "@" + row.Telegram
		} else if telegramID != 0 {
			row.Telegram = "id " + strconv.FormatInt(telegramID, 10)
		}
		row.Orders += carriedOrders
		row.Spent += carriedSpent
		out = append(out, row)
	}
	return out, rows.Err()
}

// Totals are the numbers on top of the admin's list of clients.
type Totals struct {
	Clients  int
	New      int // accounts made since the given moment
	Repeat   int // accounts with two completed orders or more, the carried ones counted
	Telegram int // accounts that sign in with Telegram
	// Revenue is what the requests completed since the start of the year came to — every
	// client's, with an account or without, as far as the owner entered the amounts.
	Revenue float64
}

// Totals counts them: since is where «new» begins, year where the revenue does.
func (s *Service) Totals(ctx context.Context, since, year time.Time) (Totals, error) {
	var out Totals
	err := s.opts.DB.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(created_at >= ?), 0), COALESCE(SUM(orders >= 2), 0), COALESCE(SUM(telegram), 0)
		FROM (SELECT c.created_at, c.telegram_id IS NOT NULL AS telegram,
		             c.orders_carried + COALESCE(SUM(l.kind = 'request' AND l.status = 'done'), 0) AS orders
		      FROM clients c LEFT JOIN leads l ON l.client_id = c.id GROUP BY c.id) AS accounts`, since.UTC()).
		Scan(&out.Clients, &out.New, &out.Repeat, &out.Telegram)
	if err != nil {
		return out, err
	}
	err = s.opts.DB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount), 0) FROM leads WHERE kind = 'request' AND status = 'done' AND COALESCE(closed_at, updated_at) >= ?`, year.UTC()).
		Scan(&out.Revenue)
	return out, err
}

// Count returns how many accounts there are.
func (s *Service) Count(ctx context.Context) (int, error) {
	var count int
	err := s.opts.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM clients`).Scan(&count)
	return count, err
}

// parseClientNumber understands «#12», «12».
func parseClientNumber(query string) (int64, bool) {
	query = strings.TrimPrefix(strings.TrimSpace(query), "#")
	var id int64
	for _, r := range query {
		if r < '0' || r > '9' || id > 1e12 {
			return 0, false
		}
		id = id*10 + int64(r-'0')
	}
	return id, id > 0
}

// Create makes an account in the admin area: for a client who came by phone or in person. The
// address, when given, becomes the one they sign in with — the owner vouches for it.
func (s *Service) Create(ctx context.Context, name, email, phone string) (int64, error) {
	name = cut(leads.Clean(name, false), 100)
	var address sql.NullString
	if strings.TrimSpace(email) != "" {
		normalized := leads.NormalizeEmail(email)
		if normalized == "" {
			return 0, ErrBadContact
		}
		address = sql.NullString{String: normalized, Valid: true}
	}
	now := s.now()
	result, err := s.opts.DB.ExecContext(ctx, `INSERT INTO clients (created_at, updated_at, name, lang, email) VALUES (?, ?, ?, 'ru', ?)`,
		now, now, name, address)
	if isDuplicate(err) {
		return 0, ErrTaken
	}
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(phone) != "" {
		if _, err := s.AddContact(ctx, id, "phone", phone); err != nil {
			return id, err
		}
	}
	if address.Valid {
		_, err = s.opts.Leads.LinkByEmail(ctx, id, address.String)
	}
	return id, err
}

// SetPersonal gives a client a discount of their own (Percent 0 takes it away).
func (s *Service) SetPersonal(ctx context.Context, id int64, personal Personal) error {
	if personal.Percent < 0 || personal.Percent > 100 {
		return ErrBadDiscount
	}
	percent := sql.NullInt64{Int64: int64(personal.Percent), Valid: personal.Percent > 0}
	note := nullString(cut(leads.Clean(personal.Note, false), 100))
	until := personal.Until
	if !percent.Valid {
		note, until, personal.Once = sql.NullString{}, sql.NullTime{}, false
	}
	return s.exec(ctx, id, `UPDATE clients SET personal_discount = ?, personal_note = ?, personal_until = ?, personal_once = ?, updated_at = ? WHERE id = ?`,
		percent, note, until, personal.Once)
}

// SetNote keeps the owner's note about a client.
func (s *Service) SetNote(ctx context.Context, id int64, note string) error {
	return s.exec(ctx, id, `UPDATE clients SET note = ?, updated_at = ? WHERE id = ?`, nullString(cut(leads.Clean(note, true), 4000)))
}

// SetEmail changes the address a client signs in with — when they lost the old one. The owner
// vouches for the new one; "" removes it (the client keeps Telegram, or has no way in until given one).
func (s *Service) SetEmail(ctx context.Context, id int64, email string) error {
	address := sql.NullString{}
	if strings.TrimSpace(email) != "" {
		if address.String = leads.NormalizeEmail(email); address.String == "" {
			return ErrBadContact
		}
		address.Valid = true
	}
	err := s.exec(ctx, id, `UPDATE clients SET email = ?, updated_at = ? WHERE id = ?`, address)
	if isDuplicate(err) {
		return ErrTaken
	}
	if err == nil && address.Valid {
		_, err = s.opts.Leads.LinkByEmail(ctx, id, address.String)
	}
	return err
}

// UnlinkTelegram takes the Telegram account off an account: signing in with it no longer works.
func (s *Service) UnlinkTelegram(ctx context.Context, id int64) error {
	return s.exec(ctx, id, `UPDATE clients SET telegram_id = NULL, telegram_username = NULL, updated_at = ? WHERE id = ?`)
}

// SetDisabled blocks an account (its sessions end at once) or lets it back in.
func (s *Service) SetDisabled(ctx context.Context, id int64, disabled bool) error {
	at := sql.NullTime{Time: s.now(), Valid: disabled}
	if err := s.exec(ctx, id, `UPDATE clients SET disabled_at = ?, updated_at = ? WHERE id = ?`, at); err != nil {
		return err
	}
	if disabled {
		_, err := s.EndSessions(ctx, id, nil)
		return err
	}
	return nil
}

// Delete removes an account: its sessions, contacts and eggs go with it; its requests stay, as
// requests of nobody — they have their own storage period, and their own deletion on request.
func (s *Service) Delete(ctx context.Context, id int64) error {
	result, err := s.opts.DB.ExecContext(ctx, `DELETE FROM clients WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if deleted, _ := result.RowsAffected(); deleted == 0 {
		return ErrNotFound
	}
	return nil
}

// SendLink sends the client a letter with a link to sign in, valid for a day: the owner's answer to
// «I can't get in».
func (s *Service) SendLink(ctx context.Context, id int64) (string, error) {
	client, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if client.Email == "" {
		return "", ErrBadContact
	}
	if client.DisabledAt.Valid {
		return "", ErrDisabled
	}
	l, _, err := s.newLogin(ctx, MethodEmail, client.Email, nil, 0, client.Lang, linkLifetime)
	if err != nil {
		return "", err
	}
	return maskEmail(client.Email), s.queueLetter(ctx, l.id, true)
}

// Merge moves everything of one account into another and removes the first: one person who signed
// in once with an address and once with Telegram. The ways to sign in move too, where the kept
// account has none; the larger personal discount stays.
func (s *Service) Merge(ctx context.Context, keep, drop int64) error {
	if keep == drop {
		return errors.New("an account cannot be merged into itself")
	}
	tx, err := s.opts.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	kept, err := scanClient(tx.QueryRowContext(ctx, `SELECT `+clientColumns+` FROM clients WHERE id = ? FOR UPDATE`, keep))
	if err != nil {
		return err
	}
	dropped, err := scanClient(tx.QueryRowContext(ctx, `SELECT `+clientColumns+` FROM clients WHERE id = ? FOR UPDATE`, drop))
	if err != nil {
		return err
	}
	for _, query := range []string{
		`UPDATE leads SET client_id = ? WHERE client_id = ?`,
		`INSERT IGNORE INTO client_contacts (client_id, kind, value, created_at) SELECT ?, kind, value, created_at FROM client_contacts WHERE client_id = ?`,
		`INSERT INTO client_achievements (client_id, id, found_at, seen_at)
		 SELECT ?, moving.id, moving.found, moving.seen FROM (SELECT id, found_at AS found, seen_at AS seen FROM client_achievements WHERE client_id = ?) AS moving
		 ON DUPLICATE KEY UPDATE found_at = LEAST(client_achievements.found_at, moving.found),
		                         seen_at = COALESCE(client_achievements.seen_at, moving.seen)`,
	} {
		if _, err := tx.ExecContext(ctx, query, keep, drop); err != nil {
			return err
		}
	}
	// The dropped account goes first: its address and Telegram must be free before they move.
	if _, err := tx.ExecContext(ctx, `DELETE FROM clients WHERE id = ?`, drop); err != nil {
		return err
	}
	email, telegram, username := nullString(kept.Email), nullID(kept.TelegramID), nullString(kept.TelegramUsername)
	if !email.Valid {
		email = nullString(dropped.Email)
	}
	if !telegram.Valid {
		telegram, username = nullID(dropped.TelegramID), nullString(dropped.TelegramUsername)
	}
	name, company := kept.Name, kept.Company
	if name == "" {
		name = dropped.Name
	}
	if company == "" {
		company = dropped.Company
	}
	personal, note, until, once := kept.Personal.Percent, kept.Personal.Note, kept.Personal.Until, kept.Personal.Once
	if dropped.Personal.Percent > personal {
		personal, note, until, once = dropped.Personal.Percent, dropped.Personal.Note, dropped.Personal.Until, dropped.Personal.Once
	}
	notes := strings.TrimSpace(strings.Join([]string{kept.Note, dropped.Note}, "\n"))
	if _, err := tx.ExecContext(ctx, `
		UPDATE clients SET email = ?, telegram_id = ?, telegram_username = ?, name = ?, company = ?, personal_discount = ?, personal_note = ?,
		       personal_until = ?, personal_once = ?, orders_carried = orders_carried + ?, spent_carried = spent_carried + ?, note = ?, updated_at = ?
		WHERE id = ?`,
		email, telegram, username, name, company, sql.NullInt64{Int64: int64(personal), Valid: personal > 0}, nullString(note), until, once,
		dropped.OrdersCarried, dropped.SpentCarried, nullString(notes), s.now(), keep); err != nil {
		return err
	}
	return tx.Commit()
}

// exec runs an update of one account: the query takes its values, then the time, then the id.
func (s *Service) exec(ctx context.Context, id int64, query string, values ...any) error {
	result, err := s.opts.DB.ExecContext(ctx, query, append(values, s.now(), id)...)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		if _, err := s.Get(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// --- the end of an account's life ----------------------------------------------------------------------

// Expire deletes the accounts nobody signed in to for the storage period and that have no requests
// left (theirs were anonymised or deleted): an account is kept for its requests, not for itself.
func (s *Service) Expire(ctx context.Context, keepMonths int) (int, error) {
	if keepMonths <= 0 {
		return 0, nil
	}
	before := s.now().AddDate(0, -keepMonths, 0)
	result, err := s.opts.DB.ExecContext(ctx, `
		DELETE c FROM clients c
		WHERE COALESCE(c.last_seen_at, c.created_at) < ? AND c.updated_at < ?
		  AND NOT EXISTS (SELECT 1 FROM leads l WHERE l.client_id = c.id)`, before, before)
	if err != nil {
		return 0, err
	}
	deleted, _ := result.RowsAffected()
	return int(deleted), nil
}

// Run does the daily housekeeping: sessions and codes that ran out, accounts past their storage
// period (Expire). keepMonths is the storage period of requests (LEADS_KEEP_MONTHS): an account
// with nothing left in it lives no longer than they do.
func (s *Service) Run(ctx context.Context, keepMonths int) {
	timer := time.NewTimer(15 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		now := s.now()
		_, _ = s.opts.DB.ExecContext(ctx, `DELETE FROM client_sessions WHERE expires_at < ? OR last_seen_at < ?`, now, now.Add(-SessionIdle))
		_, _ = s.opts.DB.ExecContext(ctx, `DELETE FROM client_logins WHERE expires_at < ?`, now.Add(-24*time.Hour))
		if expired, err := s.Expire(ctx, keepMonths); err != nil && ctx.Err() == nil {
			s.opts.Log.Error("accounts: cannot remove expired accounts", "error", err)
		} else if expired > 0 {
			s.opts.Log.Info("accounts: expired accounts removed", "count", expired)
		}
		timer.Reset(24 * time.Hour)
	}
}
