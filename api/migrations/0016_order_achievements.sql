-- Achievements of an account for its orders (docs/architecture.md, 2026-09-24): «first order», «welcome
-- back» (a second one), «big project» (an order of at least loyalty.big_order) and a golden one for all
-- three. They live next to the account's eggs, in client_achievements. seen_at is when the client was
-- shown the banner of one: until then it is new, and the personal account announces it the way Steam
-- does. The eggs came from the browser that found them, which showed them then: they are seen.
SET @add_seen = (
    SELECT IF(COUNT(*) = 0,
        'ALTER TABLE client_achievements ADD COLUMN seen_at DATETIME(3) NULL AFTER found_at',
        'DO 0')
    FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = 'client_achievements' AND column_name = 'seen_at'
);
PREPARE add_seen FROM @add_seen;
EXECUTE add_seen;
DEALLOCATE PREPARE add_seen;

UPDATE client_achievements SET seen_at = found_at
WHERE seen_at IS NULL AND id NOT IN ('first_order', 'second_order', 'big_order', 'all_orders');
