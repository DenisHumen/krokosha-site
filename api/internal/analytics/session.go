package analytics

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"strings"
)

// SessionSummary describes the visit a request from the contact form came from (brief B10.1):
// where the visitor came from, on what device, what they looked at before writing. It is the
// anonymous statistics of this package, read once and copied to the request — /privacy says so.
type SessionSummary struct {
	Known        bool   // false: no statistics for this visitor (Do Not Track, no JavaScript)
	SessionID    []byte // 8 bytes
	Source       string // direct | search | social | other | ads
	ReferrerHost string
	UTMSource    string
	UTMMedium    string
	UTMCampaign  string
	Country      string
	Device       string
	Browser      string
	OS           string
	Sections     []string // in the order they were first seen
	TimeOnSiteMs int64
}

// CurrentSession finds today's visit of whoever sends a request from this address with this
// browser. The device is always known (it is in the User-Agent of the request itself); everything
// else only when the statistics script was allowed to run.
func (s *Service) CurrentSession(ctx context.Context, ip net.IP, userAgent string) (SessionSummary, error) {
	client := ParseUserAgent(userAgent)
	summary := SessionSummary{Device: client.Device, Browser: client.Browser, OS: client.OS}
	if ip == nil {
		return summary, nil
	}
	salt, err := s.salts.forDay(ctx, s.opts.Now())
	if err != nil {
		return summary, err
	}
	visitor := visitorID(salt, ip, userAgent)

	var session []byte
	err = s.opts.DB.QueryRowContext(ctx, `SELECT session_id FROM analytics_pageviews WHERE visitor = ? ORDER BY started_at DESC, id DESC LIMIT 1`, visitor[:]).Scan(&session)
	if errors.Is(err, sql.ErrNoRows) {
		return summary, nil
	}
	if err != nil {
		return summary, err
	}
	summary.Known, summary.SessionID = true, session

	var referrer, utmSource, utmMedium, utmCampaign, country sql.NullString
	err = s.opts.DB.QueryRowContext(ctx, `
		SELECT IF(is_ad = 1, 'ads', referrer_kind), referrer_host, utm_source, utm_medium, utm_campaign, country
		FROM analytics_pageviews WHERE session_id = ? ORDER BY started_at, id LIMIT 1`, session).
		Scan(&summary.Source, &referrer, &utmSource, &utmMedium, &utmCampaign, &country)
	if err != nil {
		return summary, err
	}
	summary.ReferrerHost, summary.UTMSource, summary.UTMMedium = referrer.String, utmSource.String, utmMedium.String
	summary.UTMCampaign, summary.Country = utmCampaign.String, country.String

	if err := s.opts.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(duration_ms), 0) FROM analytics_pageviews WHERE session_id = ?`, session).
		Scan(&summary.TimeOnSiteMs); err != nil {
		return summary, err
	}
	rows, err := s.opts.DB.QueryContext(ctx, `
		SELECT e.target FROM analytics_events e JOIN analytics_pageviews p ON p.id = e.pageview
		WHERE p.session_id = ? AND e.type = 'section' AND e.target IS NOT NULL ORDER BY e.occurred_at, e.id`, session)
	if err != nil {
		return summary, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var section string
		if err := rows.Scan(&section); err != nil {
			return summary, err
		}
		if !seen[section] {
			seen[section] = true
			summary.Sections = append(summary.Sections, section)
		}
	}
	return summary, rows.Err()
}

// SessionHex is the id of the visit as the admin area's «visits» screen links it.
func (s SessionSummary) SessionHex() string { return hex.EncodeToString(s.SessionID) }

// SectionsPath is «hero → skills → contacts».
func (s SessionSummary) SectionsPath() string { return strings.Join(s.Sections, " → ") }
