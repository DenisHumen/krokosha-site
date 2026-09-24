-- Personal accounts of clients (docs/architecture.md, 2026-09-24): requests and inquiries with their
-- statuses and conversation, discounts, the levels of regular clients. There are no passwords: a
-- client signs in with a one-time code that comes by email or from the site's Telegram bot.

-- A client. Whoever signs in with an address or a Telegram account nobody has yet becomes one.
CREATE TABLE IF NOT EXISTS clients (
    id                BIGINT UNSIGNED  NOT NULL AUTO_INCREMENT PRIMARY KEY,
    created_at        DATETIME(3)      NOT NULL,
    updated_at        DATETIME(3)      NOT NULL,
    last_seen_at      DATETIME(3)      NULL,
    name              VARCHAR(100)     NOT NULL DEFAULT '',
    company           VARCHAR(120)     NOT NULL DEFAULT '',
    lang              CHAR(2)          NOT NULL DEFAULT 'en',   -- letters and the bot speak it
    -- The ways to sign in, both proven by a code: an address, a Telegram account.
    email             VARCHAR(200)     NULL,
    telegram_id       BIGINT           NULL,
    telegram_username VARCHAR(64)      NULL,                    -- as it was when linked; for the owner's eyes only
    preferred         VARCHAR(16)      NULL,                    -- how the client likes to be reached: email | telegram | phone | whatsapp…
    -- A discount of the client's own, given by the owner: the largest discount wins as always.
    personal_discount TINYINT UNSIGNED NULL,
    personal_note     VARCHAR(100)     NULL,
    personal_until    DATE             NULL,                    -- the last day it is valid on
    personal_once     TINYINT(1)       NOT NULL DEFAULT 0,      -- spent by the next request
    -- Completed orders whose requests were anonymised since: the level must not fall with the data.
    orders_carried    INT UNSIGNED     NOT NULL DEFAULT 0,
    spent_carried     DECIMAL(12,2)    NOT NULL DEFAULT 0,
    note              TEXT             NULL,                    -- the owner's; the client never sees it
    disabled_at       DATETIME(3)      NULL,                    -- blocked: cannot sign in, sessions are gone
    UNIQUE KEY uq_clients_email (email),
    UNIQUE KEY uq_clients_telegram (telegram_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Other ways to reach the client, as they wrote them: a phone, WhatsApp, Viber, LinkedIn… Nothing
-- proves them; the owner sees them with a link to write.
CREATE TABLE IF NOT EXISTS client_contacts (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    client_id  BIGINT UNSIGNED NOT NULL,
    kind       VARCHAR(16)     NOT NULL,                        -- phone | whatsapp | viber | signal | linkedin | facebook | instagram | x | discord | skype | github | website
    value      VARCHAR(200)    NOT NULL,                        -- as the client wrote it, cleaned
    created_at DATETIME(3)     NOT NULL,
    UNIQUE KEY uq_client_contacts (client_id, kind, value),
    CONSTRAINT fk_client_contacts_client FOREIGN KEY (client_id) REFERENCES clients (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- A signed-in browser. The cookie holds a random token; only its SHA-256 is here.
CREATE TABLE IF NOT EXISTS client_sessions (
    token_hash   BINARY(32)      NOT NULL PRIMARY KEY,
    client_id    BIGINT UNSIGNED NOT NULL,
    csrf_token   CHAR(43)        NOT NULL,
    created_at   DATETIME(3)     NOT NULL,
    last_seen_at DATETIME(3)     NOT NULL,
    expires_at   DATETIME(3)     NOT NULL,
    ip_prefix    VARCHAR(45)     NOT NULL,
    device       VARCHAR(60)     NOT NULL,                      -- «Chrome · Windows»
    KEY idx_client_sessions_client (client_id),
    KEY idx_client_sessions_expires (expires_at),
    CONSTRAINT fk_client_sessions_client FOREIGN KEY (client_id) REFERENCES clients (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- One-time codes: signing in, adding an address to an account, linking Telegram. Neither the code
-- nor the link is stored: both are HMACs of the nonce with APP_SECRET, made again when the letter is
-- written and when the code comes back.
CREATE TABLE IF NOT EXISTS client_logins (
    id             BIGINT UNSIGNED  NOT NULL AUTO_INCREMENT PRIMARY KEY,
    created_at     DATETIME(3)      NOT NULL,
    expires_at     DATETIME(3)      NOT NULL,
    channel        VARCHAR(10)      NOT NULL,                   -- email | telegram
    email          VARCHAR(200)     NULL,                       -- where the code goes
    telegram_id    BIGINT           NULL,                       -- who opened the bot with it
    telegram_name  VARCHAR(64)      NULL,                       -- their first name, for a new account
    telegram_user  VARCHAR(64)      NULL,                       -- @username
    start_hash     BINARY(32)       NULL,                       -- SHA-256 of the «/start l_…» token
    nonce          BINARY(16)       NOT NULL,
    browser_hash   BINARY(32)       NOT NULL,                   -- SHA-256 of the cookie of the browser that asked
    client_id      BIGINT UNSIGNED  NULL,                       -- set: adds the address or Telegram to this account
    lang           CHAR(2)          NOT NULL,
    tries          TINYINT UNSIGNED NOT NULL DEFAULT 0,
    used_at        DATETIME(3)      NULL,
    KEY idx_client_logins_start (start_hash),
    KEY idx_client_logins_expires (expires_at),
    CONSTRAINT fk_client_logins_client FOREIGN KEY (client_id) REFERENCES clients (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The easter eggs a client found, brought from the browser by their receipts: the account keeps
-- them across devices, and «all» of them is the one-time discount.
CREATE TABLE IF NOT EXISTS client_achievements (
    client_id BIGINT UNSIGNED NOT NULL,
    id        VARCHAR(16)     NOT NULL,
    found_at  DATETIME(3)     NOT NULL,
    PRIMARY KEY (client_id, id),
    CONSTRAINT fk_client_achievements_client FOREIGN KEY (client_id) REFERENCES clients (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Requests learn whose they are, what kind they are, and what discount they got.
-- MySQL has no «ADD COLUMN IF NOT EXISTS», and a migration must be safe to run twice: one ALTER
-- (atomic in MySQL 8) adds everything when the first of the columns is missing.
SET @add_columns = (
    SELECT IF(COUNT(*) = 0, '
        ALTER TABLE leads
            ADD COLUMN kind             VARCHAR(8)       NOT NULL DEFAULT ''request'' AFTER status,
            ADD COLUMN client_id        BIGINT UNSIGNED  NULL AFTER kind,
            ADD COLUMN parent_id        BIGINT UNSIGNED  NULL AFTER client_id,
            ADD COLUMN subject          VARCHAR(150)     NULL AFTER parent_id,
            ADD COLUMN discount_percent TINYINT UNSIGNED NOT NULL DEFAULT 0 AFTER subject,
            ADD COLUMN discount_reason  VARCHAR(16)      NULL AFTER discount_percent,
            ADD COLUMN discount_detail  VARCHAR(100)     NULL AFTER discount_reason,
            ADD COLUMN amount           DECIMAL(12,2)    NULL AFTER discount_detail,
            ADD COLUMN eggs_receipt     BINARY(32)       NULL AFTER amount,
            ADD COLUMN eggs_span_s      INT UNSIGNED     NULL AFTER eggs_receipt,
            ADD COLUMN client_seen_at   DATETIME(3)      NULL AFTER eggs_span_s,
            ADD UNIQUE KEY uq_leads_eggs (eggs_receipt),
            ADD KEY idx_leads_client (client_id, created_at),
            ADD KEY idx_leads_contact (contact_method, contact_value),
            ADD CONSTRAINT fk_leads_client FOREIGN KEY (client_id) REFERENCES clients (id) ON DELETE SET NULL,
            ADD CONSTRAINT fk_leads_parent FOREIGN KEY (parent_id) REFERENCES leads (id) ON DELETE SET NULL
    ', 'DO 0')
    FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = 'leads' AND column_name = 'kind'
);
PREPARE add_columns FROM @add_columns;
EXECUTE add_columns;
DEALLOCATE PREPARE add_columns;
