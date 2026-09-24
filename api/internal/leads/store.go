package leads

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/analytics"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/loyalty"
	"github.com/DenisHumen/krokosha-site/api/internal/outbox"
)

// Statuses of a request (brief B10.4).
const (
	StatusNew           = "new"
	StatusInProgress    = "in_progress"
	StatusWaitingClient = "waiting_client"
	StatusDone          = "done"
	StatusRejected      = "rejected"
	StatusSpam          = "spam"
)

// Kinds of outbox tasks about requests.
const (
	TaskNotify    = "lead.notify"    // tell the owner (email) or everyone allowed (Telegram)
	TaskAutoReply = "lead.autoreply" // confirm to the client by email
	TaskReply     = "lead.reply"     // an answer written in the admin area or in the bot
	// TaskClientMessage: the client wrote again — in Telegram or by mail; everybody is told.
	TaskClientMessage = "lead.client_message"
	// TaskUndelivered: a mail server returned a letter to the client — the staff must know.
	TaskUndelivered = "lead.undelivered"
	// TaskSiteReply: an answer waits in the client's personal account — the client is told.
	TaskSiteReply = "lead.site_reply"
)

// Lead is a stored request.
type Lead struct {
	ID          int64
	PublicToken string // base64url, 22 characters
	Status      string
	CreatedAt   time.Time
	Submission
	Verdict Verdict
	Session analytics.SessionSummary
	// IPPrefix is the truncated address the request came from.
	IPPrefix string
	// Anonymized: the storage period ran out, the person and the conversation are gone.
	Anonymized bool
	// Discount is what the request got when it came (loyalty.Decide), or what the owner set since.
	Discount loyalty.Offer
	// Amount is what the order came to, entered by the owner; HasAmount is false until then.
	Amount    float64
	HasAmount bool

	personalOnce bool // the discount given is the account's personal one «once»: Create spends it
}

// Number is how a request is called everywhere: #K-0042.
func Number(id int64) string { return fmt.Sprintf("K-%04d", id) }

// Number of this request.
func (l *Lead) Number() string { return Number(l.ID) }

// TaskPayload is what the senders get: the id is enough, they read the request themselves —
// a task that waited for a day must not send yesterday's data.
type TaskPayload struct {
	LeadID    int64 `json:"lead_id"`
	MessageID int64 `json:"message_id,omitempty"`
	// Note is for tasks about something that is not a row anywhere: why a letter came back.
	Note string `json:"note,omitempty"`
}

// Store keeps requests.
type Store struct {
	db    *sql.DB
	now   func() time.Time
	files *Files // where attachments live; nil — this installation keeps none
	media *Files // where the files of templates live (quick answers); nil — templates carry none
	// rules of discounts (content/site.yaml → loyalty); location is the owner's time zone, the one
	// the last day of a personal discount is counted in.
	rules    func() config.Loyalty
	location *time.Location

	// Listeners: the Telegram bot keeps its cards in step with what happens here.
	hooks struct {
		sync.RWMutex
		changed []func(leadID int64)
		erasing []func(ctx context.Context, leadID int64)
	}
}

// NewStore builds the store.
func NewStore(db *sql.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now, rules: func() config.Loyalty { return config.Loyalty{} }, location: time.UTC}
}

// UseFiles tells the store where attachments are kept, so that deleting a request deletes them too.
func (s *Store) UseFiles(files *Files) { s.files = files }

// UseMedia tells the store where the files of templates are kept.
func (s *Store) UseMedia(files *Files) { s.media = files }

// Files is the directory of attachments, where the files of answers are kept too; nil — none.
func (s *Store) Files() *Files { return s.files }

// UseLoyalty gives the store the rules of discounts; without them no request gets one.
func (s *Store) UseLoyalty(rules func() config.Loyalty, location *time.Location) {
	s.rules = rules
	if location != nil {
		s.location = location
	}
}

// Rules returns the rules of discounts in force.
func (s *Store) Rules() config.Loyalty { return s.rules() }

// OnChange registers a listener called after anything about a request changed — it was taken,
// answered, moved to another status — wherever that was done: the admin area or the bot. The
// listener must be quick; whatever takes time it does elsewhere.
func (s *Store) OnChange(listener func(leadID int64)) {
	s.hooks.Lock()
	defer s.hooks.Unlock()
	s.hooks.changed = append(s.hooks.changed, listener)
}

// OnErase registers a listener called just before a request is deleted or anonymised: the last
// moment at which whoever keeps copies elsewhere (messages in Telegram chats) still knows where.
func (s *Store) OnErase(listener func(ctx context.Context, leadID int64)) {
	s.hooks.Lock()
	defer s.hooks.Unlock()
	s.hooks.erasing = append(s.hooks.erasing, listener)
}

func (s *Store) changed(leadID int64) {
	s.hooks.RLock()
	defer s.hooks.RUnlock()
	for _, listener := range s.hooks.changed {
		listener(leadID)
	}
}

func (s *Store) erasing(ctx context.Context, leadID int64) {
	s.hooks.RLock()
	defer s.hooks.RUnlock()
	for _, listener := range s.hooks.erasing {
		listener(ctx, leadID)
	}
}

// Create stores a request, its first message, the record of it, and the notifications to send —
// in one transaction (brief B10.2): either everything is there, or the visitor is told to try
// again. Spam is stored too, silently: nobody is notified.
func (s *Store) Create(ctx context.Context, sub Submission, verdict Verdict, session analytics.SessionSummary, ipPrefix string) (*Lead, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	if sub.Kind == "" {
		sub.Kind = KindRequest
	}
	now := s.now().UTC()
	lead := &Lead{
		PublicToken: base64.RawURLEncoding.EncodeToString(token), Status: StatusNew, CreatedAt: now,
		Submission: sub, Verdict: verdict, Session: session, IPPrefix: ipPrefix,
	}
	if verdict.IsSpam() && !sub.Trusted {
		lead.Status = StatusSpam
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Whose it is: the account the visitor is signed in to, or else the account that proved this
	// very address with a code. What a robot sent belongs to nobody.
	if lead.Status != StatusSpam && lead.ClientID == 0 && sub.ContactMethod == MethodEmail {
		if lead.ClientID, err = accountOfEmail(ctx, tx, sub.ContactValue); err != nil {
			return nil, err
		}
	}
	var eggs []byte // the receipt of the eggs, when the discount it claims is the one given
	if lead.Kind == KindRequest && lead.Status != StatusSpam {
		if lead.Discount, eggs, err = s.price(ctx, tx, lead); err != nil {
			return nil, err
		}
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO leads (public_token, created_at, updated_at, status, kind, client_id, parent_id, subject, discount_percent,
			discount_reason, discount_detail, eggs_receipt, eggs_span_s, name, contact_method, contact_value, direction, description,
			budget, timeline, lang, consent_at, spam_score, spam_reasons, session_id, source, referrer_host, utm_source, utm_medium,
			utm_campaign, country, device, browser, os, ip_prefix, sections_seen, time_on_site_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		token, now, now, lead.Status, lead.Kind, nullID(lead.ClientID), nullID(lead.ParentID), null(cut(lead.Subject, 150)), lead.Discount.Percent,
		null(lead.Discount.Reason), null(cut(lead.Discount.Detail, 100)), nullBytes(eggs), nullSpan(lead.EggsSpan, eggs != nil),
		sub.Name, sub.ContactMethod, sub.ContactValue, sub.Direction, sub.Description,
		null(sub.Budget), null(sub.Timeline), sub.Lang, now, verdict.Score, null(cut(strings.Join(verdict.Reasons, "; "), 255)),
		nullBytes(session.SessionID), null(session.Source), null(session.ReferrerHost), null(session.UTMSource), null(session.UTMMedium),
		null(session.UTMCampaign), null(session.Country), null(session.Device), null(session.Browser), null(session.OS), ipPrefix,
		null(cut(session.SectionsPath(), 255)), nullInt(session.TimeOnSiteMs, session.Known))
	if err != nil {
		return nil, err
	}
	if lead.ID, err = result.LastInsertId(); err != nil {
		return nil, err
	}
	if err := s.spend(ctx, tx, lead); err != nil {
		return nil, err
	}

	channel := "form"
	if lead.Kind == KindInquiry {
		channel = ChannelSite
	}
	message, err := tx.ExecContext(ctx, `INSERT INTO lead_messages (lead_id, created_at, direction, channel, body) VALUES (?, ?, 'in', ?, ?)`,
		lead.ID, now, channel, sub.Description)
	if err != nil {
		return nil, err
	}
	messageID, err := message.LastInsertId()
	if err != nil {
		return nil, err
	}
	// The files are on disk already (Files.Save); from here on the database knows whose they are.
	for _, file := range sub.Files {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO lead_attachments (lead_id, message_id, created_at, filename, kind, size, sha256, stored_as) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			lead.ID, messageID, now, cut(file.Filename, 255), file.Kind, file.Size, file.SHA256, file.StoredAs); err != nil {
			return nil, err
		}
	}
	details := "форма на сайте"
	if lead.Kind == KindInquiry {
		details = "обращение из личного кабинета"
	}
	if lead.Status == StatusSpam {
		details = "похоже на спам: " + strings.Join(verdict.Reasons, "; ")
		if sub.FilesDropped > 0 {
			details += fmt.Sprintf("; вложения не сохранены (%d)", sub.FilesDropped)
		}
		details = cut(details, 255)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO lead_events (lead_id, created_at, actor, action, to_status, details) VALUES (?, ?, 'client', 'created', ?, ?)`,
		lead.ID, now, lead.Status, details); err != nil {
		return nil, err
	}

	if lead.Status != StatusSpam {
		payload := TaskPayload{LeadID: lead.ID}
		tasks := []outbox.NewTask{
			{Channel: outbox.ChannelEmail, Kind: TaskNotify, LeadID: lead.ID, DedupeKey: fmt.Sprintf("lead:%d:notify:email", lead.ID), Payload: payload},
			{Channel: outbox.ChannelTelegram, Kind: TaskNotify, LeadID: lead.ID, DedupeKey: fmt.Sprintf("lead:%d:notify:telegram", lead.ID), Payload: payload},
		}
		// An inquiry is written in the account, where it shows at once: no confirmation by mail.
		if sub.ContactMethod == MethodEmail && lead.Kind == KindRequest {
			tasks = append(tasks, outbox.NewTask{Channel: outbox.ChannelEmail, Kind: TaskAutoReply, LeadID: lead.ID,
				DedupeKey: fmt.Sprintf("lead:%d:autoreply", lead.ID), Payload: payload})
		}
		for _, task := range tasks {
			if err := outbox.Enqueue(ctx, tx, now, task); err != nil {
				return nil, err
			}
		}
	}
	return lead, tx.Commit()
}

// Get reads a request by its id.
func (s *Store) Get(ctx context.Context, id int64) (*Lead, error) {
	lead := &Lead{ID: id}
	var token, session []byte
	var budget, timeline, reasons, source, referrer, utmSource, utmMedium, utmCampaign, country, device, browser, os, sections sql.NullString
	var subject, discountReason, discountDetail sql.NullString
	var timeOnSite, clientID, parentID, eggsSpan sql.NullInt64
	var amount sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		SELECT public_token, status, kind, client_id, parent_id, subject, discount_percent, discount_reason, discount_detail, amount,
		       eggs_span_s, created_at, name, contact_method, contact_value, direction, description, budget, timeline, lang,
		       spam_score, spam_reasons, session_id, source, referrer_host, utm_source, utm_medium, utm_campaign, country, device,
		       browser, os, ip_prefix, sections_seen, time_on_site_ms, anonymized_at IS NOT NULL
		FROM leads WHERE id = ?`, id).Scan(&token, &lead.Status, &lead.Kind, &clientID, &parentID, &subject, &lead.Discount.Percent,
		&discountReason, &discountDetail, &amount, &eggsSpan, &lead.CreatedAt, &lead.Name, &lead.ContactMethod, &lead.ContactValue,
		&lead.Direction, &lead.Description, &budget, &timeline, &lead.Lang, &lead.Verdict.Score, &reasons, &session, &source, &referrer,
		&utmSource, &utmMedium, &utmCampaign, &country, &device, &browser, &os, &lead.IPPrefix, &sections, &timeOnSite, &lead.Anonymized)
	if err != nil {
		return nil, err
	}
	lead.PublicToken = base64.RawURLEncoding.EncodeToString(token)
	lead.Budget, lead.Timeline = budget.String, timeline.String
	lead.ClientID, lead.ParentID, lead.Subject = clientID.Int64, parentID.Int64, subject.String
	lead.Discount.Reason, lead.Discount.Detail = discountReason.String, discountDetail.String
	lead.Amount, lead.HasAmount = amount.Float64, amount.Valid
	lead.EggsSpan = time.Duration(eggsSpan.Int64) * time.Second
	if reasons.String != "" {
		lead.Verdict.Reasons = strings.Split(reasons.String, "; ")
	}
	lead.Session = analytics.SessionSummary{
		Known: len(session) > 0, SessionID: session, Source: source.String, ReferrerHost: referrer.String,
		UTMSource: utmSource.String, UTMMedium: utmMedium.String, UTMCampaign: utmCampaign.String, Country: country.String,
		Device: device.String, Browser: browser.String, OS: os.String, TimeOnSiteMs: timeOnSite.Int64,
	}
	if sections.String != "" {
		lead.Session.Sections = strings.Split(sections.String, " → ")
	}
	return lead, nil
}

// ByToken finds a request by the random token given to its sender.
func (s *Store) ByToken(ctx context.Context, token string) (*Lead, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 16 {
		return nil, sql.ErrNoRows
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM leads WHERE public_token = ?`, raw).Scan(&id); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func null(text string) sql.NullString { return sql.NullString{String: text, Valid: text != ""} }

func nullBytes(data []byte) any {
	if len(data) == 0 {
		return nil
	}
	return data
}

func nullInt(value int64, valid bool) sql.NullInt64 { return sql.NullInt64{Int64: value, Valid: valid} }

func nullID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: id > 0} }

func nullSpan(span time.Duration, valid bool) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(span / time.Second), Valid: valid}
}

// cut shortens a text to fit a column, on a rune boundary.
func cut(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && text[limit]&0xC0 == 0x80 { // do not split a multi-byte character
		limit--
	}
	return text[:limit]
}
