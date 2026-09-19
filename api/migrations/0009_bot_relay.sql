-- The bot relays between clients and staff (brief B10.5).

-- What a remembered message was about, besides its request: «in:17» — the push about the
-- client's message 17. A push retried after a failure then skips the chats it has reached.
-- MySQL has no «ADD COLUMN IF NOT EXISTS», and a migration must be safe to run twice.
SET @add_column = (
    SELECT IF(COUNT(*) = 0, 'ALTER TABLE bot_messages ADD COLUMN ref VARCHAR(40) NULL AFTER kind, ADD KEY idx_bot_messages_ref (lead_id, ref)', 'DO 0')
    FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = 'bot_messages' AND column_name = 'ref'
);
PREPARE add_column FROM @add_column;
EXECUTE add_column;
DEALLOCATE PREPARE add_column;
