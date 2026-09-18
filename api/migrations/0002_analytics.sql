-- First-party, cookie-less analytics (brief B5).
--
-- A visitor is sha256(daily salt + IP + User-Agent), cut to 16 bytes. The salt changes every day
-- and old salts are deleted, so visits of different days cannot be linked, by anyone, ever.
-- The IP address itself is stored only truncated (/24 or /48).

CREATE TABLE IF NOT EXISTS analytics_salts (
    day  DATE       NOT NULL PRIMARY KEY,
    salt BINARY(32) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- One row per page load.
CREATE TABLE IF NOT EXISTS analytics_pageviews (
    id            BIGINT UNSIGNED  NOT NULL AUTO_INCREMENT PRIMARY KEY,
    pageview_id   BINARY(8)        NOT NULL,             -- random id made by the page, lives in memory only
    visitor       BINARY(16)       NOT NULL,
    session_id    BINARY(8)        NOT NULL,             -- visitor + 30 minutes of inactivity
    started_at    DATETIME(3)      NOT NULL,             -- UTC
    day           DATE             NOT NULL,             -- in the site's time zone, for reports
    path          VARCHAR(200)     NOT NULL,
    lang          CHAR(2)          NULL,
    referrer_host VARCHAR(100)     NULL,
    referrer_kind VARCHAR(10)      NOT NULL,             -- direct | search | social | other
    utm_source    VARCHAR(100)     NULL,
    utm_medium    VARCHAR(100)     NULL,
    utm_campaign  VARCHAR(100)     NULL,
    utm_term      VARCHAR(100)     NULL,
    utm_content   VARCHAR(100)     NULL,
    is_ad         TINYINT(1)       NOT NULL DEFAULT 0,   -- paid traffic: by utm_medium or an ad click id
    ip_prefix     VARCHAR(45)      NOT NULL,             -- 203.0.113.0/24 or 2001:db8:1::/48
    country       CHAR(2)          NULL,                 -- filled when a GeoIP database is installed
    city          VARCHAR(80)      NULL,
    device        VARCHAR(10)      NOT NULL,             -- desktop | mobile | tablet
    browser       VARCHAR(20)      NOT NULL,
    os            VARCHAR(20)      NOT NULL,
    duration_ms   INT UNSIGNED     NOT NULL DEFAULT 0,   -- time the page was actually visible
    max_scroll    TINYINT UNSIGNED NOT NULL DEFAULT 0,   -- 0, 25, 50, 75 or 100
    UNIQUE KEY uq_pageview (visitor, pageview_id),
    KEY idx_day (day),
    KEY idx_started (started_at),
    KEY idx_session (session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- What happened on the page: clicks on tracked elements, outbound links, sections seen.
-- Scroll depth and time on page are properties of the page view itself, not rows here.
CREATE TABLE IF NOT EXISTS analytics_events (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    pageview    BIGINT UNSIGNED NOT NULL,
    occurred_at DATETIME(3)     NOT NULL,
    day         DATE            NOT NULL,
    type        VARCHAR(16)     NOT NULL,                -- click | outbound | section | section_time | egg
    target      VARCHAR(100)    NULL,                    -- data-track id, section name, host of the link
    value       INT UNSIGNED    NULL,                    -- milliseconds for section_time
    KEY idx_day_type (day, type),
    KEY idx_pageview (pageview),
    CONSTRAINT fk_analytics_events_pageview FOREIGN KEY (pageview)
        REFERENCES analytics_pageviews (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
