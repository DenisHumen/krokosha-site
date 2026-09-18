-- The hidden admin area (brief B6): accounts, login sessions and an audit trail.

CREATE TABLE IF NOT EXISTS admin_users (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    login          VARCHAR(64)     NOT NULL,
    password_hash  VARCHAR(255)    NOT NULL,             -- argon2id, PHC string format
    totp_secret    VARBINARY(64)   NULL,                 -- set when enrolment starts
    totp_enabled   TINYINT(1)      NOT NULL DEFAULT 0,   -- set when the first code was confirmed
    totp_last_step BIGINT          NOT NULL DEFAULT 0,   -- a code is accepted once: no replay within its window
    disabled       TINYINT(1)      NOT NULL DEFAULT 0,
    created_at     DATETIME(3)     NOT NULL,
    last_login_at  DATETIME(3)     NULL,
    UNIQUE KEY uq_admin_login (login)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The cookie holds a random token; only its SHA-256 is stored, so a leaked database
-- does not hand out live sessions.
CREATE TABLE IF NOT EXISTS admin_sessions (
    token_hash   BINARY(32)      NOT NULL PRIMARY KEY,
    user_id      BIGINT UNSIGNED NOT NULL,
    csrf_token   CHAR(43)        NOT NULL,
    created_at   DATETIME(3)     NOT NULL,
    last_seen_at DATETIME(3)     NOT NULL,
    expires_at   DATETIME(3)     NOT NULL,               -- absolute limit, whatever the activity
    ip_prefix    VARCHAR(45)     NOT NULL,
    client       VARCHAR(60)     NOT NULL,               -- «Chrome · Windows»
    KEY idx_admin_sessions_user (user_id),
    KEY idx_admin_sessions_expires (expires_at),
    CONSTRAINT fk_admin_sessions_user FOREIGN KEY (user_id) REFERENCES admin_users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Who did what and when: logins, failures, account changes; later — every action on a lead.
CREATE TABLE IF NOT EXISTS audit_log (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    occurred_at DATETIME(3)     NOT NULL,
    actor       VARCHAR(64)     NOT NULL,                -- admin login, «bot:<telegram id>», «system»
    action      VARCHAR(64)     NOT NULL,
    subject     VARCHAR(100)    NULL,
    details     TEXT            NULL,
    ip_prefix   VARCHAR(45)     NULL,
    KEY idx_audit_time (occurred_at),
    KEY idx_audit_subject (subject)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
