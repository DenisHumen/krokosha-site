-- The Telegram bot (docs/telegram.md): a third role, «notify» — the cards of new requests and the
-- reminders come, but nothing can be done from the bot —, and when a person was last seen in it,
-- for the admin's «Бот» screen. The role column is wide enough already (VARCHAR(8)).
SET @add_seen = (
    SELECT IF(COUNT(*) = 0,
        'ALTER TABLE bot_users ADD COLUMN last_seen_at DATETIME(3) NULL AFTER muted_until',
        'DO 0')
    FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = 'bot_users' AND column_name = 'last_seen_at'
);
PREPARE add_seen FROM @add_seen;
EXECUTE add_seen;
DEALLOCATE PREPARE add_seen;
