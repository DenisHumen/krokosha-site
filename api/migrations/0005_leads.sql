-- Requests from the site's form, the conversation around them, and the reliable delivery of
-- notifications (brief B10: a mini-CRM).

-- One request. Its number on screen — #K-0042 — is the id.
CREATE TABLE IF NOT EXISTS leads (
    id                BIGINT UNSIGNED  NOT NULL AUTO_INCREMENT PRIMARY KEY,
    -- Random and unguessable: the client's link to their own request («continue in Telegram»).
    public_token      BINARY(16)       NOT NULL,
    created_at        DATETIME(3)      NOT NULL,
    updated_at        DATETIME(3)      NOT NULL,
    status            VARCHAR(16)      NOT NULL DEFAULT 'new',  -- new | in_progress | waiting_client | done | rejected | spam
    assignee          VARCHAR(64)      NULL,                    -- who took it: an admin login or a name from the bot
    assigned_at       DATETIME(3)      NULL,
    first_response_at DATETIME(3)      NULL,                    -- the first reply to the client: «time to first reaction»
    closed_at         DATETIME(3)      NULL,
    reject_reason     VARCHAR(255)     NULL,

    -- What the visitor typed. Untrusted: escaped wherever it is shown.
    name              VARCHAR(100)     NOT NULL,
    contact_method    VARCHAR(10)      NOT NULL,                -- email | telegram | phone
    contact_value     VARCHAR(200)     NOT NULL,
    direction         VARCHAR(20)      NOT NULL,                -- id from content/site.yaml → contacts.form.directions
    description       TEXT             NOT NULL,
    budget            VARCHAR(60)      NULL,
    timeline          VARCHAR(60)      NULL,
    lang              CHAR(2)          NOT NULL,                -- language of the page: replies are written in it
    consent_at        DATETIME(3)      NOT NULL,                -- the «I consent» box, when it was ticked

    spam_score        TINYINT UNSIGNED NOT NULL DEFAULT 0,
    spam_reasons      VARCHAR(255)     NULL,

    -- The anonymous statistics session the request came from (brief B10.1, said so on /privacy):
    -- a copy of its summary, so the request stays readable after raw events are cleaned up.
    session_id        BINARY(8)        NULL,
    source            VARCHAR(10)      NULL,                    -- direct | search | social | other | ads
    referrer_host     VARCHAR(100)     NULL,
    utm_source        VARCHAR(100)     NULL,
    utm_medium        VARCHAR(100)     NULL,
    utm_campaign      VARCHAR(100)     NULL,
    country           CHAR(2)          NULL,
    device            VARCHAR(10)      NULL,
    browser           VARCHAR(20)      NULL,
    os                VARCHAR(20)      NULL,
    ip_prefix         VARCHAR(45)      NOT NULL,                -- truncated, like everywhere
    sections_seen     VARCHAR(255)     NULL,                    -- «services → projects → contacts»
    time_on_site_ms   INT UNSIGNED     NULL,

    UNIQUE KEY uq_leads_token (public_token),
    KEY idx_leads_status (status, created_at),
    KEY idx_leads_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The conversation: what the client wrote, what was answered, and internal notes.
CREATE TABLE IF NOT EXISTS lead_messages (
    id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    lead_id          BIGINT UNSIGNED NOT NULL,
    created_at       DATETIME(3)     NOT NULL,
    direction        VARCHAR(8)      NOT NULL,                  -- in (from the client) | out (to the client) | note (internal)
    channel          VARCHAR(10)     NOT NULL,                  -- form | email | telegram | phone | admin
    author           VARCHAR(64)     NULL,                      -- who wrote an answer or a note; NULL for the client
    body             TEXT            NOT NULL,
    delivery         VARCHAR(10)     NULL,                      -- for «out»: queued | sent | failed
    email_message_id VARCHAR(255)    NULL,                      -- Message-ID, so that replies stay one thread
    KEY idx_lead_messages (lead_id, created_at),
    CONSTRAINT fk_lead_messages_lead FOREIGN KEY (lead_id) REFERENCES leads (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- What happened to a request: who took it, who changed its status, from what to what.
CREATE TABLE IF NOT EXISTS lead_events (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    lead_id     BIGINT UNSIGNED NOT NULL,
    created_at  DATETIME(3)     NOT NULL,
    actor       VARCHAR(64)     NOT NULL,                       -- admin login, «bot:<name>», «client», «system»
    action      VARCHAR(24)     NOT NULL,                       -- created | status | assigned | replied | noted | client_replied
    from_status VARCHAR(16)     NULL,
    to_status   VARCHAR(16)     NULL,
    details     VARCHAR(255)    NULL,
    KEY idx_lead_events (lead_id, created_at),
    CONSTRAINT fk_lead_events_lead FOREIGN KEY (lead_id) REFERENCES leads (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Ready-made answers and refusals, editable in the admin area. {name} and {id} are filled in.
CREATE TABLE IF NOT EXISTS reply_templates (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    kind       VARCHAR(8)      NOT NULL,                        -- reply | reject
    lang       CHAR(2)         NOT NULL,
    position   INT             NOT NULL DEFAULT 0,
    title      VARCHAR(100)    NOT NULL,
    body       TEXT            NOT NULL,
    updated_at DATETIME(3)     NOT NULL,
    KEY idx_reply_templates (kind, lang, position)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Outbox (brief B10.2): a request and the tasks to tell people about it are written in ONE
-- transaction; a worker delivers the tasks with retries. Telegram or the mail server being down
-- delays a notification, it never loses one. Deliberately not in Redis: see docs/architecture.md §6.
CREATE TABLE IF NOT EXISTS outbox (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    created_at      DATETIME(3)     NOT NULL,
    channel         VARCHAR(16)     NOT NULL,                   -- email | telegram
    kind            VARCHAR(32)     NOT NULL,                   -- lead.notify | lead.autoreply | lead.reply | alert…
    lead_id         BIGINT UNSIGNED NULL,
    -- Idempotency: the same task is never queued twice («lead:42:notify:email»).
    dedupe_key      VARCHAR(120)    NOT NULL,
    payload         JSON            NOT NULL,
    status          VARCHAR(10)     NOT NULL DEFAULT 'pending', -- pending | sending | sent | failed (gave up)
    attempts        INT UNSIGNED    NOT NULL DEFAULT 0,
    next_attempt_at DATETIME(3)     NOT NULL,
    locked_until    DATETIME(3)     NULL,                       -- a worker that died mid-delivery releases the task by time
    last_error      VARCHAR(500)    NULL,
    sent_at         DATETIME(3)     NULL,
    UNIQUE KEY uq_outbox_dedupe (dedupe_key),
    KEY idx_outbox_due (status, next_attempt_at),
    KEY idx_outbox_lead (lead_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
