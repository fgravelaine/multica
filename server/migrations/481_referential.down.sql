DROP INDEX IF EXISTS idx_raised_hand_open_referential;
ALTER TABLE raised_hand DROP COLUMN IF EXISTS referential_key;
DROP TABLE IF EXISTS referential;
