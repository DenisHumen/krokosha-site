package leads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/achievements"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/loyalty"
)

// Discounts of requests (docs/architecture.md, 2026-09-24). A request gets its discount when it
// comes, inside the transaction that stores it: the history read for it cannot change under it,
// and a one-time reward — the eggs, a personal discount «once» — is spent together with the
// request that got it, or not at all. From then on the discount is the request's: a later level
// does not change what was promised, only the owner can, by hand (SetDiscount).

// ChannelSite: the client wrote in the personal account on the site.
const ChannelSite = "site"

// querier is a transaction or the pool.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// accountOfEmail finds the account that proved this address with a code; 0 — none. The address is
// compared in lower case, byte by byte: the table's collation alone would take «ánna@» for «anna@».
func accountOfEmail(ctx context.Context, q querier, email string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM clients WHERE LOWER(email) COLLATE utf8mb4_bin = LOWER(?) AND disabled_at IS NULL`, email).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// historyOf reads what is known of a client: of the account when there is one, else of everybody
// who left the same contact. lock takes the account's row for the transaction, so that two requests
// sent at once cannot both spend the same one-time reward.
func historyOf(ctx context.Context, q querier, clientID int64, method, value string, lock bool) (loyalty.History, loyalty.Personal, error) {
	var history loyalty.History
	var personal loyalty.Personal
	var eggs int
	// The eggs' discount given to a request the owner rejected (or found to be spam) was never
	// spent: rejecting a stranger's request restores it, like the first request's.
	aggregate := `SELECT COALESCE(SUM(status NOT IN ('rejected', 'spam')), 0), COALESCE(SUM(status = 'done'), 0),
	                     COALESCE(SUM(IF(status = 'done', COALESCE(amount, 0), 0)), 0),
	                     COALESCE(SUM(discount_reason = 'eggs' AND status NOT IN ('rejected', 'spam')), 0)
	              FROM leads WHERE kind = 'request' AND `
	var err error
	if clientID > 0 && lock {
		// The account's row before its history: a second request of the same account, sent at the
		// same moment, waits here until the first is committed — and then counts it, since the
		// transaction reads what is committed (Store.Create). Otherwise both would see no earlier
		// request and each take the one-time rewards.
		var id int64
		if err = q.QueryRowContext(ctx, `SELECT id FROM clients WHERE id = ? FOR UPDATE`, clientID).Scan(&id); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return history, personal, err
		}
	}
	if clientID > 0 {
		err = q.QueryRowContext(ctx, aggregate+`client_id = ?`, clientID).Scan(&history.Earlier, &history.Orders, &history.Spent, &eggs)
	} else {
		// An anonymised request has no contact any more: it matches nobody.
		err = q.QueryRowContext(ctx, aggregate+`contact_method = ? AND contact_value = ? AND contact_value <> ''`, method, value).
			Scan(&history.Earlier, &history.Orders, &history.Spent, &eggs)
	}
	if err != nil {
		return history, personal, err
	}
	history.EggsUsed = eggs > 0
	if clientID == 0 {
		return history, personal, nil
	}

	query := `SELECT orders_carried, spent_carried, COALESCE(personal_discount, 0), COALESCE(personal_note, ''), personal_until, personal_once
	          FROM clients WHERE id = ?`
	if lock {
		query += ` FOR UPDATE`
	}
	var carriedOrders int
	var carriedSpent float64
	var until sql.NullTime
	err = q.QueryRowContext(ctx, query, clientID).Scan(&carriedOrders, &carriedSpent, &personal.Percent, &personal.Note, &until, &personal.Once)
	if errors.Is(err, sql.ErrNoRows) {
		return history, personal, nil // the account was deleted a moment ago: its requests are nobody's
	}
	history.Orders += carriedOrders
	history.Spent += carriedSpent
	if until.Valid {
		personal.Until = until.Time
	}
	return history, personal, err
}

// Account is what a letter or the bot needs to reach the owner of a personal account.
type Account struct {
	ID         int64
	Name       string
	Lang       string
	Email      string // "" — none proved
	TelegramID int64  // 0 — none linked
	Preferred  string
}

// AccountOf reads the account a request belongs to.
func (s *Store) AccountOf(ctx context.Context, clientID int64) (Account, error) {
	account := Account{ID: clientID}
	var email, preferred sql.NullString
	var telegram sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT name, lang, email, telegram_id, preferred FROM clients WHERE id = ? AND disabled_at IS NULL`, clientID).
		Scan(&account.Name, &account.Lang, &email, &telegram, &preferred)
	if errors.Is(err, sql.ErrNoRows) {
		return account, ErrNoClient
	}
	account.Email, account.TelegramID, account.Preferred = email.String, telegram.Int64, preferred.String
	return account, err
}

// accountChannel is how the owner of an account hears about an answer in it: the way they prefer,
// else the address, else Telegram; "" — neither (a blocked or deleted account).
func accountChannel(ctx context.Context, q querier, clientID int64) (string, error) {
	var email, preferred sql.NullString
	var telegram sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT email, telegram_id, preferred FROM clients WHERE id = ? AND disabled_at IS NULL`, clientID).
		Scan(&email, &telegram, &preferred)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", err
	case preferred.String == MethodTelegram && telegram.Valid:
		return ChannelTelegram, nil
	case email.Valid:
		return ChannelEmail, nil
	case telegram.Valid:
		return ChannelTelegram, nil
	}
	return "", nil
}

// today is the date in the owner's time zone, the one the last day of a personal discount is.
func (s *Store) today() time.Time {
	now := s.now().In(s.location)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// price decides the discount of a new request. It returns the fingerprint of the eggs' receipt
// when the eggs are what gave the discount: only then is the receipt spent.
func (s *Store) price(ctx context.Context, tx *sql.Tx, lead *Lead) (loyalty.Offer, []byte, error) {
	rules := s.rules()
	if !rules.Enabled {
		return loyalty.Offer{}, nil, nil
	}
	history, personal, err := historyOf(ctx, tx, lead.ClientID, lead.ContactMethod, lead.ContactValue, true)
	if err != nil {
		return loyalty.Offer{}, nil, err
	}
	// A personal discount is the owner's gift to the client: the client spends it, signed in — not
	// whoever types the client's address into the form (the request still joins the account).
	if !lead.Trusted {
		personal = loyalty.Personal{}
	}
	claim := loyalty.Claim{}
	var fingerprint []byte
	if lead.EggsReceipt != "" {
		fingerprint = achievements.Fingerprint(lead.EggsReceipt)
		var used int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM leads WHERE eggs_receipt = ?`, fingerprint).Scan(&used); err != nil {
			return loyalty.Offer{}, nil, err
		}
		if claim.Eggs = used == 0; !claim.Eggs {
			fingerprint = nil
		}
	}
	claim.Eggs = claim.Eggs || lead.EggsByAccount
	offer := loyalty.Decide(rules, history, claim, personal, s.today())
	if offer.Reason != loyalty.ReasonEggs {
		fingerprint = nil
	}
	lead.personalOnce = offer.Reason == loyalty.ReasonPersonal && personal.Once
	return offer, fingerprint, nil
}

// spend: a personal discount «once» that went to this request is gone from the account.
func (s *Store) spend(ctx context.Context, tx *sql.Tx, lead *Lead) error {
	if !lead.personalOnce || lead.ClientID == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE clients SET personal_discount = NULL, personal_note = NULL, personal_until = NULL, personal_once = 0, updated_at = ?
		WHERE id = ?`, s.now().UTC(), lead.ClientID)
	return err
}

// OrderFacts is what the achievements of an account's orders are earned by: how many orders it
// completed (those of anonymised requests included) and the largest sum of one.
func (s *Store) OrderFacts(ctx context.Context, clientID int64) (orders int, biggest float64, err error) {
	var largest sql.NullFloat64
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) + COALESCE((SELECT orders_carried FROM clients WHERE id = ?), 0), MAX(amount)
		FROM leads WHERE kind = 'request' AND status = 'done' AND client_id = ?`, clientID, clientID).Scan(&orders, &largest)
	return orders, largest.Float64, err
}

// History is what the loyalty rules know of an account: for its page and for the admin area.
func (s *Store) History(ctx context.Context, clientID int64) (loyalty.History, loyalty.Personal, error) {
	return historyOf(ctx, s.db, clientID, "", "", false)
}

// ContactHistory is the same for somebody without an account, by the contact they left.
func (s *Store) ContactHistory(ctx context.Context, method, value string) (loyalty.History, error) {
	history, _, err := historyOf(ctx, s.db, 0, method, value, false)
	return history, err
}

// EggsReceiptUsed reports whether a receipt of the eggs already paid for a discount.
func (s *Store) EggsReceiptUsed(ctx context.Context, receipt string) (bool, error) {
	var used int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM leads WHERE eggs_receipt = ?`, achievements.Fingerprint(receipt)).Scan(&used)
	return used > 0, err
}

// Preview is the discount the next request of an account would get — for the account's page and
// for the form of a signed-in client. eggs: the account or the browser has every egg. Nothing is spent.
func (s *Store) Preview(ctx context.Context, clientID int64, eggs bool) (loyalty.Offer, error) {
	rules := s.rules()
	if !rules.Enabled {
		return loyalty.Offer{}, nil
	}
	history, personal, err := historyOf(ctx, s.db, clientID, "", "", false)
	if err != nil {
		return loyalty.Offer{}, err
	}
	return loyalty.Decide(rules, history, loyalty.Claim{Eggs: eggs}, personal, s.today()), nil
}

// discountWords are the reasons of a discount in the three languages of the site.
var discountWords = map[string]map[string]string{
	"ru": {"welcome": "первая заявка", "tier": "уровень «%s»", "eggs": "найдены все пасхалки", "personal": "персональная", "manual": "вручную"},
	"uk": {"welcome": "перша заявка", "tier": "рівень «%s»", "eggs": "знайдено всі пасхалки", "personal": "персональна", "manual": "вручну"},
	"en": {"welcome": "first request", "tier": "%s level", "eggs": "every easter egg found", "personal": "personal", "manual": "set by hand"},
}

// DiscountWords says what a discount is and why: «10% — первая заявка», «5% — уровень «Серебряный»».
// For the owner (owner = true) a discount set by hand says so; a client reads just its note.
func DiscountWords(rules config.Loyalty, offer loyalty.Offer, lang string, owner bool) string {
	if offer.Percent <= 0 {
		return ""
	}
	words, ok := discountWords[lang]
	if !ok {
		words = discountWords["en"]
	}
	reason := words[offer.Reason]
	switch offer.Reason {
	case loyalty.ReasonTier:
		name := offer.Detail
		if tier, ok := rules.Tier(offer.Detail); ok && tier.Name.In(lang) != "" {
			name = tier.Name.In(lang)
		}
		reason = fmt.Sprintf(reason, name)
	case loyalty.ReasonPersonal, loyalty.ReasonManual:
		switch {
		case offer.Detail != "" && (owner || offer.Reason == loyalty.ReasonPersonal):
			reason += " (" + offer.Detail + ")"
		case offer.Detail != "":
			reason = offer.Detail
		case !owner && offer.Reason == loyalty.ReasonManual:
			reason = ""
		}
	}
	if reason == "" {
		return fmt.Sprintf("%d%%", offer.Percent)
	}
	return fmt.Sprintf("%d%% — %s", offer.Percent, reason)
}
