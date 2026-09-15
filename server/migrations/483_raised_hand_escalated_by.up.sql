-- SPIKE (not upstream): remember WHICH lead escalated.
--
-- Migration 482 nulls recipient_id on escalation, which is correct — after an
-- escalation the hand is addressed to the human, and leaving the lead's id in
-- "who this is addressed to" would lie about the current state.
--
-- But it also erased the only record of which lead had it, and that is exactly
-- what the contest ratio needs: settled-against-escalated PER LEAD is how you
-- tell a lead that checks its referentials from one that relays the question
-- unchanged. Without this column every escalation is anonymous and the ratio
-- can only be computed per referential, which answers a different question.
--
-- So the same split as escalated_at: recipient_id is the present, this is the
-- history. Written once, never cleared.
ALTER TABLE raised_hand
    ADD COLUMN escalated_by_lead_id UUID;

-- Stamped only when a lead escalates, so the partial index stays small.
CREATE INDEX idx_raised_hand_escalated_by_lead
    ON raised_hand (escalated_by_lead_id)
    WHERE escalated_by_lead_id IS NOT NULL;
