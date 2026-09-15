DROP INDEX IF EXISTS idx_raised_hand_open_recipient;
ALTER TABLE raised_hand
    DROP CONSTRAINT IF EXISTS raised_hand_escalation_shape,
    DROP CONSTRAINT IF EXISTS raised_hand_recipient_shape,
    DROP COLUMN IF EXISTS answered_by_level,
    DROP COLUMN IF EXISTS escalation_note,
    DROP COLUMN IF EXISTS escalated_at,
    DROP COLUMN IF EXISTS recipient_id,
    DROP COLUMN IF EXISTS recipient_type;
