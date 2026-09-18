-- What nginx served (brief B6, «server traffic»): every request, including bots and visitors
-- without JavaScript. The API reads nginx's JSON access log incrementally and keeps aggregates
-- only: no addresses, no full User-Agent strings. The raw log itself lives for 30 days
-- (deploy/logrotate).

-- Requests per minute. Response times are kept as a histogram, so that the median and the 95th
-- percentile can be computed for any period by adding the buckets up.
CREATE TABLE IF NOT EXISTS traffic_minutes (
    minute       DATETIME        NOT NULL PRIMARY KEY,   -- UTC, start of the minute
    requests     INT UNSIGNED    NOT NULL DEFAULT 0,
    bytes_sent   BIGINT UNSIGNED NOT NULL DEFAULT 0,
    status_2xx   INT UNSIGNED    NOT NULL DEFAULT 0,
    status_3xx   INT UNSIGNED    NOT NULL DEFAULT 0,
    status_4xx   INT UNSIGNED    NOT NULL DEFAULT 0,
    status_5xx   INT UNSIGNED    NOT NULL DEFAULT 0,
    bot_requests INT UNSIGNED    NOT NULL DEFAULT 0,
    not_found    INT UNSIGNED    NOT NULL DEFAULT 0,     -- 404s: a spike means a broken link or a scanner
    probes       INT UNSIGNED    NOT NULL DEFAULT 0,     -- requests for things this site never had (/wp-admin, /.env…)
    t_5ms        INT UNSIGNED    NOT NULL DEFAULT 0,     -- request_time ≤ 5 ms
    t_10ms       INT UNSIGNED    NOT NULL DEFAULT 0,
    t_25ms       INT UNSIGNED    NOT NULL DEFAULT 0,
    t_50ms       INT UNSIGNED    NOT NULL DEFAULT 0,
    t_100ms      INT UNSIGNED    NOT NULL DEFAULT 0,
    t_250ms      INT UNSIGNED    NOT NULL DEFAULT 0,
    t_500ms      INT UNSIGNED    NOT NULL DEFAULT 0,
    t_1s         INT UNSIGNED    NOT NULL DEFAULT 0,
    t_2500ms     INT UNSIGNED    NOT NULL DEFAULT 0,
    t_5s         INT UNSIGNED    NOT NULL DEFAULT 0,
    t_slow       INT UNSIGNED    NOT NULL DEFAULT 0      -- longer than 5 s
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Distinct clients per hour — the «bots» colour of the overview timeline. A client is an
-- (address, User-Agent) pair, counted with HyperLogLog in Redis; only the number lands here.
CREATE TABLE IF NOT EXISTS traffic_hours (
    hour          DATETIME     NOT NULL PRIMARY KEY,     -- UTC, start of the hour
    bot_clients   INT UNSIGNED NOT NULL DEFAULT 0,
    other_clients INT UNSIGNED NOT NULL DEFAULT 0        -- browsers and tools, with or without JavaScript
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Top URLs. The query string is dropped; the number of distinct paths per day is capped by the
-- reader, so a scanner trying ten thousand addresses cannot bloat the table.
CREATE TABLE IF NOT EXISTS traffic_paths (
    day        DATE              NOT NULL,               -- in the site's time zone
    path       VARCHAR(200)      NOT NULL,
    status     SMALLINT UNSIGNED NOT NULL,
    hits       INT UNSIGNED      NOT NULL DEFAULT 0,
    bytes_sent BIGINT UNSIGNED   NOT NULL DEFAULT 0,
    PRIMARY KEY (day, path, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Who asks: crawlers by name, browsers by family, tools.
CREATE TABLE IF NOT EXISTS traffic_agents (
    day        DATE            NOT NULL,
    agent      VARCHAR(60)     NOT NULL,                 -- Googlebot, Bingbot, Chrome, curl…
    kind       VARCHAR(10)     NOT NULL,                 -- bot | browser | tool | unknown
    hits       INT UNSIGNED    NOT NULL DEFAULT 0,
    bytes_sent BIGINT UNSIGNED NOT NULL DEFAULT 0,
    PRIMARY KEY (day, agent)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Scanners: who probes for what. The network is truncated like everywhere else.
CREATE TABLE IF NOT EXISTS traffic_probes (
    day       DATE         NOT NULL,
    ip_prefix VARCHAR(45)  NOT NULL,
    pattern   VARCHAR(40)  NOT NULL,                     -- wordpress, dotenv, git, php, phpmyadmin…
    hits      INT UNSIGNED NOT NULL DEFAULT 0,
    last_path VARCHAR(200) NOT NULL,
    last_seen DATETIME     NOT NULL,
    PRIMARY KEY (day, ip_prefix, pattern)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Where the reader stopped. The file is recognised by a fingerprint of its first line, which
-- survives a restart and tells a rotated log from the one that was being read.
CREATE TABLE IF NOT EXISTS traffic_state (
    log         VARCHAR(100)    NOT NULL PRIMARY KEY,
    fingerprint BINARY(16)      NOT NULL,
    position    BIGINT UNSIGNED NOT NULL,
    updated_at  DATETIME        NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
