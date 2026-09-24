-- The achievements of the easter eggs and how rare each one is — the share of players who have it,
-- the way Steam shows it (docs/architecture.md, 2026-09-24). A «player» is a browser that has found
-- at least one egg; it counts itself once, with its first find, and every egg it finds once (the page
-- keeps a note in localStorage). Nothing here can be traced to a visitor: counts only.

-- Players and finds per day, in the site's time zone: the totals are sums of these rows, and the
-- admin area draws the days.
CREATE TABLE IF NOT EXISTS achievement_daily (
    day DATE         NOT NULL,
    id  VARCHAR(16)  NOT NULL,                  -- an egg (konami, sudo…), «all», or «players»
    n   INT UNSIGNED NOT NULL,
    PRIMARY KEY (day, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The totals and the share, recomputed from the days every few minutes: what the site shows next
-- to an achievement («found by 4.2% of visitors»).
CREATE TABLE IF NOT EXISTS achievements (
    id         VARCHAR(16)  NOT NULL PRIMARY KEY, -- an egg or «all»
    found      INT UNSIGNED NOT NULL,
    players    INT UNSIGNED NOT NULL,             -- the players the share was taken of
    percent    DECIMAL(5,2) NOT NULL,             -- found / players × 100, at most 100
    updated_at DATETIME(3)  NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
