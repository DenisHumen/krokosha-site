-- Files sent with a request (brief B10.1, B10.7) and the end of a request's life.

-- A file lives under <data>/attachments, outside the web root, under a random name; this table is
-- what knows whose it is. The admin area hands files out, nothing else does.
CREATE TABLE IF NOT EXISTS lead_attachments (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    lead_id    BIGINT UNSIGNED NOT NULL,
    message_id BIGINT UNSIGNED NULL,                            -- the message it came with
    created_at DATETIME(3)     NOT NULL,
    filename   VARCHAR(255)    NOT NULL,                        -- as the sender named it, cleaned; untrusted
    kind       VARCHAR(8)      NOT NULL,                        -- pdf | png | jpg | txt | docx — by signature, not by name
    size       INT UNSIGNED    NOT NULL,
    sha256     BINARY(32)      NOT NULL,
    stored_as  CHAR(32)        NOT NULL,                        -- the random name on disk
    UNIQUE KEY uq_lead_attachments_stored (stored_as),
    KEY idx_lead_attachments (lead_id),
    CONSTRAINT fk_lead_attachments_lead FOREIGN KEY (lead_id) REFERENCES leads (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- When a request was anonymised (brief B10.7): what identified the person is gone, what the
-- statistics need — dates, status, direction, source — stays.
-- MySQL has no «ADD COLUMN IF NOT EXISTS», and a migration must be safe to run twice.
SET @add_column = (
    SELECT IF(COUNT(*) = 0, 'ALTER TABLE leads ADD COLUMN anonymized_at DATETIME(3) NULL AFTER closed_at', 'DO 0')
    FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = 'leads' AND column_name = 'anonymized_at'
);
PREPARE add_column FROM @add_column;
EXECUTE add_column;
DEALLOCATE PREPARE add_column;
