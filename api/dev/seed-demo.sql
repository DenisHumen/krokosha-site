-- Demo data for looking at the admin dashboards locally (api/README.md, «Посмотреть админку локально»).
-- Visits of the last two weeks: several sources, devices, languages, clicks and easter eggs.
-- Safe to run again: it replaces its own rows only (ip_prefix 192.0.2.0/24 is a documentation range).

DELETE FROM analytics_pageviews WHERE ip_prefix = '192.0.2.0/24';

SET @@cte_max_recursion_depth = 5000;

INSERT INTO analytics_pageviews
    (pageview_id, visitor, session_id, started_at, day, path, lang, referrer_host, referrer_kind,
     utm_source, utm_medium, utm_campaign, is_ad, ip_prefix, country, device, browser, os, duration_ms, max_scroll)
WITH RECURSIVE seq (n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < 899),
-- Evenly spread pseudo-random numbers from a hash: MySQL's RAND(seed) gives nearly the same
-- value for neighbouring seeds. v* belong to the visit (two page views each), p* to the page view.
dice AS (
    SELECT n,
           CONV(SUBSTRING(MD5(CONCAT('visit-', n DIV 2)), 1, 6), 16, 10) / 16777216 AS v_day,
           CONV(SUBSTRING(MD5(CONCAT('visit-', n DIV 2)), 7, 6), 16, 10) / 16777216 AS v_hour,
           CONV(SUBSTRING(MD5(CONCAT('visit-', n DIV 2)), 13, 6), 16, 10) / 16777216 AS source,
           CONV(SUBSTRING(MD5(CONCAT('visit-', n DIV 2)), 19, 6), 16, 10) / 16777216 AS device,
           CONV(SUBSTRING(MD5(CONCAT('page-', n)), 1, 6), 16, 10) / 16777216 AS page,
           CONV(SUBSTRING(MD5(CONCAT('page-', n)), 7, 6), 16, 10) / 16777216 AS depth
    FROM seq
),
visits AS (
    -- Recent days are busier than older ones; visits fall between 08:00 and 23:00 Kyiv time.
    -- What would lie in the future moves a day back.
    SELECT n, IF(planned > UTC_TIMESTAMP(3), TIMESTAMPADD(DAY, -1, planned), planned) AS at, source, device, page, depth
    FROM (
        SELECT dice.*, TIMESTAMPADD(MINUTE, FLOOR((5 + v_hour * 15) * 60) + (n MOD 2) * 3,
                   TIMESTAMPADD(DAY, -CAST(FLOOR(POW(v_day, 1.6) * 14) AS SIGNED), CAST(UTC_DATE() AS DATETIME(3)))) AS planned
        FROM dice
    ) AS plan
)
SELECT UNHEX(LPAD(HEX(n), 16, '0')),
       UNHEX(MD5(CONCAT('demo-visitor-', DATE(at), '-', n DIV 2))),
       UNHEX(LEFT(MD5(CONCAT('demo-visit-', n DIV 2)), 16)),
       at, DATE(CONVERT_TZ(at, '+00:00', '+03:00')),
       ELT(1 + FLOOR(page * 6), '/', '/', '/uk/', '/uk/', '/ru/', '/privacy/'),
       ELT(1 + FLOOR(page * 6), 'en', 'en', 'uk', 'uk', 'ru', 'en'),
       CASE WHEN source < 0.30 THEN 'google.com' WHEN source < 0.38 THEN 'bing.com' WHEN source < 0.50 THEN 't.me'
            WHEN source < 0.56 THEN 'linkedin.com' WHEN source < 0.62 THEN 'dou.ua' ELSE NULL END,
       CASE WHEN source < 0.38 THEN 'search' WHEN source < 0.56 THEN 'social' WHEN source < 0.62 THEN 'other' ELSE 'direct' END,
       IF(source < 0.12, 'google', NULL), IF(source < 0.12, 'cpc', NULL), IF(source < 0.12, ELT(1 + FLOOR(page * 2), 'mikrotik-kyiv', 'server-setup'), NULL),
       source < 0.12, '192.0.2.0/24',
       ELT(1 + FLOOR(device * 5), 'UA', 'UA', 'PL', 'DE', 'US'),
       ELT(1 + FLOOR(device * 5), 'desktop', 'desktop', 'mobile', 'mobile', 'tablet'),
       ELT(1 + FLOOR(device * 5), 'Chrome', 'Firefox', 'Safari', 'Chrome', 'Safari'),
       ELT(1 + FLOOR(device * 5), 'Windows', 'Linux', 'iOS', 'Android', 'iPadOS'),
       2000 + FLOOR(depth * 90000), ELT(1 + FLOOR(depth * 5), 0, 25, 50, 75, 100)
FROM visits;

-- Sections seen and the time spent in them.
INSERT INTO analytics_events (pageview, occurred_at, day, type, target, value)
SELECT p.id, TIMESTAMPADD(SECOND, s.position * 4, p.started_at), p.day, 'section', s.name, NULL
FROM analytics_pageviews p
JOIN (SELECT 1 AS position, 'hero' AS name, 0 AS depth UNION ALL SELECT 2, 'stats', 25 UNION ALL SELECT 3, 'services', 25
      UNION ALL SELECT 4, 'skills', 50 UNION ALL SELECT 5, 'projects', 75 UNION ALL SELECT 6, 'contacts', 100) s
  ON p.max_scroll >= s.depth
WHERE p.ip_prefix = '192.0.2.0/24' AND p.path <> '/privacy/';

INSERT INTO analytics_events (pageview, occurred_at, day, type, target, value)
SELECT pageview, TIMESTAMPADD(SECOND, 40, occurred_at), day, 'section_time', target,
       1500 + FLOOR(CONV(SUBSTRING(MD5(CONCAT(pageview, target)), 1, 6), 16, 10) / 16777216 * IF(target IN ('skills', 'projects'), 40000, 9000))
FROM analytics_events WHERE type = 'section'
  AND pageview IN (SELECT id FROM analytics_pageviews WHERE ip_prefix = '192.0.2.0/24');

-- Clicks: contacts, skill branches, projects, the language switcher; a few easter eggs.
INSERT INTO analytics_events (pageview, occurred_at, day, type, target, value)
SELECT id, TIMESTAMPADD(SECOND, 25, started_at), day, 'click',
       ELT(1 + (id MOD 9), 'cta-telegram', 'cta-telegram', 'cta-email', 'social-github', 'skill-devops', 'skill-net-hardware',
           'project-krokosha-site', 'lang-uk', 'cta-discuss'), NULL
FROM analytics_pageviews WHERE ip_prefix = '192.0.2.0/24' AND max_scroll >= 50 AND id MOD 3 = 0;

INSERT INTO analytics_events (pageview, occurred_at, day, type, target, value)
SELECT e.pageview, e.occurred_at, e.day, 'outbound',
       CASE e.target WHEN 'cta-telegram' THEN 't.me' WHEN 'cta-email' THEN 'mailto' ELSE 'github.com' END, NULL
FROM analytics_events e
WHERE e.type = 'click' AND e.target IN ('cta-telegram', 'cta-email', 'social-github', 'project-krokosha-site')
  AND e.pageview IN (SELECT id FROM analytics_pageviews WHERE ip_prefix = '192.0.2.0/24');

INSERT INTO analytics_events (pageview, occurred_at, day, type, target, value)
SELECT id, TIMESTAMPADD(SECOND, 50, started_at), day, 'egg', ELT(1 + (id MOD 2), 'konami', 'logo-spin'), NULL
FROM analytics_pageviews WHERE ip_prefix = '192.0.2.0/24' AND id MOD 41 = 0;
