-- The Telegram bot (brief B10.3–B10.5). Anybody can find the bot, so nothing here trusts a user
-- name: people are bound by their numeric Telegram id, and they get in by invitation only.

-- Who may work with requests in Telegram.
CREATE TABLE IF NOT EXISTS bot_users (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    telegram_id BIGINT          NOT NULL,                       -- the person; in a private chat also the chat id
    role        VARCHAR(8)      NOT NULL,                       -- owner | member
    name        VARCHAR(64)     NOT NULL,                       -- how cards call them: «взял Денис»
    username    VARCHAR(64)     NULL,                           -- shown in the admin area, never used to find anyone
    created_at  DATETIME(3)     NOT NULL,
    invited_by  VARCHAR(64)     NOT NULL,
    disabled_at DATETIME(3)     NULL,                           -- access revoked: nothing is sent, nothing is answered
    muted_until DATETIME(3)     NULL,                           -- /mute 2h
    -- What the bot is waiting for from this person: the text of an answer, a note, a reason.
    state       JSON            NULL,
    UNIQUE KEY uq_bot_users_telegram (telegram_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- One-time invitations: a code lives 24 hours and lets one person in.
CREATE TABLE IF NOT EXISTS bot_invites (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    code_hash  BINARY(32)      NOT NULL,                        -- SHA-256: the code itself is shown once, to whoever made it
    role       VARCHAR(8)      NOT NULL,
    created_at DATETIME(3)     NOT NULL,
    created_by VARCHAR(64)     NOT NULL,                        -- admin login, «bot:<name>», «cli»
    expires_at DATETIME(3)     NOT NULL,
    used_at    DATETIME(3)     NULL,
    used_by    BIGINT          NULL,                            -- telegram id
    UNIQUE KEY uq_bot_invites_code (code_hash),
    KEY idx_bot_invites_expires (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Every message the bot sent to the people above about a request. The card — one per chat — is
-- rewritten when the status changes. And all of them are wiped when the client's data is deleted
-- or anonymised: what is erased on the server must not live on in Telegram chats. No foreign key,
-- on purpose: these rows have to outlive their request until the wiping is done.
CREATE TABLE IF NOT EXISTS bot_messages (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    lead_id    BIGINT UNSIGNED NOT NULL,
    chat_id    BIGINT          NOT NULL,
    message_id BIGINT          NOT NULL,
    kind       VARCHAR(8)      NOT NULL,                        -- card | text
    created_at DATETIME(3)     NOT NULL,
    wipe_after DATETIME(3)     NULL,                            -- set when the request is erased; the wiper takes it from there
    UNIQUE KEY uq_bot_messages (chat_id, message_id),
    KEY idx_bot_messages_lead (lead_id, kind),
    KEY idx_bot_messages_wipe (wipe_after)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Clients who pressed «continue in Telegram» (brief B10.5): the bot relays between them and the
-- people above. One request — one Telegram account: the link from the «thank you» page works once.
CREATE TABLE IF NOT EXISTS bot_clients (
    lead_id     BIGINT UNSIGNED NOT NULL PRIMARY KEY,
    telegram_id BIGINT          NOT NULL,
    linked_at   DATETIME(3)     NOT NULL,
    KEY idx_bot_clients_telegram (telegram_id, linked_at),
    CONSTRAINT fk_bot_clients_lead FOREIGN KEY (lead_id) REFERENCES leads (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
