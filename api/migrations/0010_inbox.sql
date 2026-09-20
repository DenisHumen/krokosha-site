-- Letters that came to the service mailbox (brief B10.5).

-- One row per letter ever handled. For a letter that joined a request's conversation the row
-- says just that — the text and the files are with the request, and go when it goes. For a
-- letter nobody could place the row is what the «Входящие без заявки» screen shows; the letter
-- itself waits in the mailbox (uid) until a person decides, or until it is too old.
CREATE TABLE IF NOT EXISTS inbox_letters (
    id           BIGINT UNSIGNED   NOT NULL AUTO_INCREMENT PRIMARY KEY,
    received_at  DATETIME(3)       NOT NULL,
    -- SHA-256 of the Message-ID, or of the letter itself when it has none. A letter is handled
    -- once: whether it was delivered twice, or the service stopped between the database and the
    -- mailbox.
    message_key  BINARY(32)        NOT NULL,
    outcome      VARCHAR(16)       NOT NULL,           -- matched | unmatched | bounce | ignored
    lead_id      BIGINT UNSIGNED   NULL,               -- the request it went to
    uid          INT UNSIGNED      NULL,               -- where a letter without a request waits in the mailbox
    uid_validity INT UNSIGNED      NULL,
    message_id   VARCHAR(255)      NULL,               -- to find it again if the mailbox was rebuilt
    from_name    VARCHAR(255)      NULL,               -- untrusted, like everything below
    from_address VARCHAR(255)      NULL,
    subject      VARCHAR(255)      NULL,
    excerpt      TEXT              NULL,               -- the beginning of the text
    files        SMALLINT UNSIGNED NOT NULL DEFAULT 0,
    automatic    TINYINT(1)        NOT NULL DEFAULT 0, -- written by a program: an out-of-office note, a delivery report
    note         VARCHAR(255)      NULL,               -- why it is here: «заявка K-0042 удалена», «письмо больше 30 МБ»
    UNIQUE KEY uq_inbox_letters_key (message_key),
    KEY idx_inbox_letters_outcome (outcome, received_at),
    KEY idx_inbox_letters_lead (lead_id),
    CONSTRAINT fk_inbox_letters_lead FOREIGN KEY (lead_id) REFERENCES leads (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
