-- Small key/value store of the service itself: installation id, feature markers, and later
-- the state of background jobs. Feature tables arrive with their own migrations.
CREATE TABLE IF NOT EXISTS app_meta (
    name       VARCHAR(190) NOT NULL PRIMARY KEY,
    value      TEXT         NOT NULL,
    updated_at DATETIME(3)  NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
