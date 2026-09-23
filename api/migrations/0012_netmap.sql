-- The map of the internet of /map (docs/netmap.md): what `krokosha-cli netmap sync` makes of open
-- data every night, and what the API draws routes on. Only changes are written: a network or a
-- link that went away keeps its row with the day it went (gone_at), every such change is listed in
-- netmap_change, and both are forgotten after 90 days. Everything here can be built again from
-- the sources, so the backups keep the tables without their rows (deploy/backup.sh).

-- One run of the sync. The API loads the map again when a newer successful run appears.
CREATE TABLE IF NOT EXISTS netmap_sync (
    id          INT UNSIGNED  NOT NULL AUTO_INCREMENT PRIMARY KEY,
    started_at  DATETIME(3)   NOT NULL,
    finished_at DATETIME(3)   NULL,
    ok          BOOLEAN       NOT NULL DEFAULT FALSE,
    networks    INT UNSIGNED  NOT NULL DEFAULT 0,
    links       INT UNSIGNED  NOT NULL DEFAULT 0,
    prefixes    INT UNSIGNED  NOT NULL DEFAULT 0,
    exchanges   INT UNSIGNED  NOT NULL DEFAULT 0,
    added       INT UNSIGNED  NOT NULL DEFAULT 0,   -- links that appeared (or came back)
    gone        INT UNSIGNED  NOT NULL DEFAULT 0,   -- links that went away
    changed     INT UNSIGNED  NOT NULL DEFAULT 0,   -- links whose kind changed
    overview    VARCHAR(64)   NOT NULL DEFAULT '',  -- the file the page downloads
    report      TEXT          NULL,                 -- what the sources said, a line each
    error       VARCHAR(1000) NULL,
    KEY ok_id (ok, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Autonomous systems: the networks of the internet.
CREATE TABLE IF NOT EXISTS netmap_as (
    asn        INT UNSIGNED     NOT NULL PRIMARY KEY,
    name       VARCHAR(128)     NOT NULL,
    country    VARCHAR(2)       NOT NULL,           -- of the registration; '' — not told
    type       VARCHAR(32)      NOT NULL,           -- PeeringDB: NSP, Content, Cable/DSL/ISP…
    lat        DOUBLE           NOT NULL,           -- DOUBLE, not FLOAT: a coordinate comes back exactly as it was written
    lon        DOUBLE           NOT NULL,
    placed     TINYINT UNSIGNED NOT NULL,           -- 0 nowhere, 1 by its addresses, 2 by its data centres, 3 by its neighbours
    first_seen DATE             NOT NULL,
    gone_at    DATE             NULL,
    KEY gone_at (gone_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Links between two networks, the smaller number first.
CREATE TABLE IF NOT EXISTS netmap_link (
    a          INT UNSIGNED     NOT NULL,
    b          INT UNSIGNED     NOT NULL,
    rel        TINYINT          NOT NULL,           -- what a is to b: -1 its provider, 0 a peer, 1 its customer, 2 not known
    sources    TINYINT UNSIGNED NOT NULL,           -- bits: 1 BGP paths and 2 multilateral peering (CAIDA), 4 RouteViews tables
    first_seen DATE             NOT NULL,
    gone_at    DATE             NULL,
    PRIMARY KEY (a, b),
    KEY gone_at (gone_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- What changed, sync by sync: links and networks that appeared, went away, or changed their kind.
CREATE TABLE IF NOT EXISTS netmap_change (
    id      BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    sync_id INT UNSIGNED    NOT NULL,
    day     DATE            NOT NULL,
    kind    VARCHAR(12)     NOT NULL,               -- link_added, link_gone, link_kind, as_added, as_gone
    a       INT UNSIGNED    NOT NULL,
    b       INT UNSIGNED    NOT NULL DEFAULT 0,     -- links only
    rel     TINYINT         NULL,                   -- the kind of a link, as it is now
    KEY day (day),
    KEY a (a),
    KEY b (b)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Which network announces which addresses: 4 bytes of an IPv4 address or 16 of an IPv6 one.
CREATE TABLE IF NOT EXISTS netmap_prefix (
    first VARBINARY(16) NOT NULL PRIMARY KEY,
    last  VARBINARY(16) NOT NULL,
    asn   INT UNSIGNED  NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Exchange points (PeeringDB), placed in the middle of their data centres.
CREATE TABLE IF NOT EXISTS netmap_ix (
    id      INT UNSIGNED NOT NULL PRIMARY KEY,
    name    VARCHAR(128) NOT NULL,
    city    VARCHAR(128) NOT NULL,
    country VARCHAR(2)   NOT NULL,
    lat     DOUBLE       NOT NULL,
    lon     DOUBLE       NOT NULL,
    placed  BOOLEAN      NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Data centres (PeeringDB).
CREATE TABLE IF NOT EXISTS netmap_facility (
    id      INT UNSIGNED NOT NULL PRIMARY KEY,
    name    VARCHAR(128) NOT NULL,
    city    VARCHAR(128) NOT NULL,
    country VARCHAR(2)   NOT NULL,
    lat     DOUBLE       NOT NULL,
    lon     DOUBLE       NOT NULL,
    placed  BOOLEAN      NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- The ports of a network at an exchange point, all of them together.
CREATE TABLE IF NOT EXISTS netmap_port (
    asn   INT UNSIGNED      NOT NULL,
    ix    INT UNSIGNED      NOT NULL,
    speed BIGINT UNSIGNED   NOT NULL,               -- Mbit/s
    ports SMALLINT UNSIGNED NOT NULL,
    PRIMARY KEY (asn, ix)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- The data centres a network is present in.
CREATE TABLE IF NOT EXISTS netmap_site (
    asn      INT UNSIGNED NOT NULL,
    facility INT UNSIGNED NOT NULL,
    PRIMARY KEY (asn, facility)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- The peering LANs of exchange points: a hop of a traceroute in one is at that exchange point.
CREATE TABLE IF NOT EXISTS netmap_lan (
    prefix VARCHAR(43)  NOT NULL PRIMARY KEY,
    ix     INT UNSIGNED NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
