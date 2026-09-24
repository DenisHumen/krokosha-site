-- Answers reach the client everywhere the client can be reached (docs/architecture.md, 2026-09-24):
-- every address and Telegram account known to be theirs, and the personal account. Each of those is
-- a delivery of its own, with a status of its own: one bounced letter does not hide that the same
-- answer reached Telegram.
CREATE TABLE IF NOT EXISTS lead_deliveries (
    id               BIGINT UNSIGNED   NOT NULL AUTO_INCREMENT PRIMARY KEY,
    message_id       BIGINT UNSIGNED   NOT NULL,
    lead_id          BIGINT UNSIGNED   NOT NULL,
    channel          VARCHAR(10)       NOT NULL,              -- email | telegram
    target           VARCHAR(200)      NOT NULL,              -- the address or the Telegram chat; '' — the chat of the request's bot link, once opened
    status           VARCHAR(10)       NOT NULL,              -- queued | sent | failed
    sent_parts       SMALLINT UNSIGNED NOT NULL DEFAULT 0,    -- Telegram: the parts that went out; a retry sends the rest
    email_message_id VARCHAR(255)      NULL,
    created_at       DATETIME(3)       NOT NULL,
    updated_at       DATETIME(3)       NOT NULL,
    UNIQUE KEY uq_lead_deliveries (message_id, channel, target),
    KEY idx_lead_deliveries_lead (lead_id),
    CONSTRAINT fk_lead_deliveries_message FOREIGN KEY (message_id) REFERENCES lead_messages (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The address a letter of the client came from: an answer goes back there too. And when the staff
-- last opened the conversation of a request: whatever the client wrote after it is unread.
SET @add_columns = (
    SELECT IF(COUNT(*) = 0, '
        ALTER TABLE lead_messages ADD COLUMN from_address VARCHAR(200) NULL AFTER email_message_id
    ', 'DO 0')
    FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = 'lead_messages' AND column_name = 'from_address'
);
PREPARE add_columns FROM @add_columns;
EXECUTE add_columns;
DEALLOCATE PREPARE add_columns;

SET @add_seen = (
    SELECT IF(COUNT(*) = 0, '
        ALTER TABLE leads ADD COLUMN staff_seen_at DATETIME(3) NULL AFTER client_seen_at
    ', 'DO 0')
    FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = 'leads' AND column_name = 'staff_seen_at'
);
PREPARE add_seen FROM @add_seen;
EXECUTE add_seen;
DEALLOCATE PREPARE add_seen;

-- Requests that were there before count as read: the new counters start from now, not from the
-- whole history of every request.
UPDATE leads SET staff_seen_at = updated_at WHERE staff_seen_at IS NULL;
