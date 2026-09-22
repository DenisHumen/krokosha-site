-- The numbers of every finished day, kept for good (brief B5: raw events live for a year,
-- aggregates — without a limit). Nothing here can be traced to a visitor: counts only.
--
-- A visitor's identifier changes every day, so «visitors of a period» is exactly the sum of the
-- days' visitors — the same number the raw data gave while it was there.
CREATE TABLE IF NOT EXISTS analytics_daily (
    day           DATE            NOT NULL PRIMARY KEY,   -- in the site's time zone
    visitors      INT UNSIGNED    NOT NULL,
    visits        INT UNSIGNED    NOT NULL,
    ad_visits     INT UNSIGNED    NOT NULL,
    pageviews     INT UNSIGNED    NOT NULL,
    view_ms_sum   BIGINT UNSIGNED NOT NULL,               -- time pages were visible, over the views that reported any
    view_ms_count INT UNSIGNED    NOT NULL,
    actions       INT UNSIGNED    NOT NULL,               -- clicks on tracked elements
    scroll25      INT UNSIGNED    NOT NULL,
    scroll50      INT UNSIGNED    NOT NULL,
    scroll75      INT UNSIGNED    NOT NULL,
    scroll100     INT UNSIGNED    NOT NULL,
    aggregated_at DATETIME(3)     NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The breakdowns of a day — the fifty largest lines of each.
CREATE TABLE IF NOT EXISTS analytics_daily_breakdown (
    day       DATE            NOT NULL,
    -- page | source | referrer | campaign | lang | device | browser | os | country | click |
    -- outbound | egg | section (page views that showed it) | section_ms (time in it) |
    -- contact (visitors who clicked a way to reach the owner)
    dimension VARCHAR(16)     NOT NULL,
    name      VARCHAR(200)    NOT NULL,
    n         BIGINT UNSIGNED NOT NULL,
    PRIMARY KEY (day, dimension, name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
