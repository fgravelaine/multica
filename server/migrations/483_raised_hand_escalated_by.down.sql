DROP INDEX IF EXISTS idx_raised_hand_escalated_by_lead;
ALTER TABLE raised_hand DROP COLUMN IF EXISTS escalated_by_lead_id;
