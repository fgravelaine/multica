-- SPIKE (not upstream): a raised hand has a recipient.
--
-- Until now every hand went to the human, which the design note says is exactly
-- backwards: "L'humain est le destinataire le plus rare — la plupart des mains
-- levées se referment contre un référentiel ou chez le lead." The human is the
-- RARE destination, and the count that reaches them is what measures autonomy.
--
-- That count was structurally 100% because there was nowhere else to send one.
-- Two things had to exist first: a lead, and a referential the question might
-- already be answered by. Squads already supply the lead (squad.leader_id).
-- Migration 481 supplied the referential. So the recipient can land now.
--
-- recipient_type is 'lead' or 'human', resolved AT RAISE from the raising
-- agent's squad. No squad, or the raiser IS the leader, means there is nobody
-- above it and the hand goes straight to the human — the honest answer, not a
-- fallback that pretends at delegation.
--
-- escalated_at is the lead giving up. It is a separate column from
-- recipient_type on purpose: recipient_type tells you where the hand is NOW,
-- escalated_at tells you it passed through a lead first. A hand that went
-- straight to the human and a hand the lead could not settle are different
-- facts about the same referential, and collapsing them would make a thin
-- referential look like an absent lead.
--
-- answered_by_level records WHO actually decided, stamped from the answerer's
-- own identity rather than from where the hand was addressed — a human can
-- answer a lead-addressed hand at any time, and reporting that as a lead
-- closure would inflate exactly the number this is for.

ALTER TABLE raised_hand
    ADD COLUMN recipient_type    TEXT NOT NULL DEFAULT 'human'
        CHECK (recipient_type IN ('lead', 'human')),
    ADD COLUMN recipient_id      UUID,
    ADD COLUMN escalated_at      TIMESTAMPTZ,
    ADD COLUMN escalation_note   TEXT,
    ADD COLUMN answered_by_level TEXT
        CHECK (answered_by_level IS NULL OR answered_by_level IN ('lead', 'human'));

-- A lead recipient names an agent; a human recipient names nobody.
ALTER TABLE raised_hand
    ADD CONSTRAINT raised_hand_recipient_shape CHECK (
        (recipient_type = 'lead'  AND recipient_id IS NOT NULL)
        OR
        (recipient_type = 'human' AND recipient_id IS NULL)
    );

-- Escalation is one-way and only out of a lead.
ALTER TABLE raised_hand
    ADD CONSTRAINT raised_hand_escalation_shape CHECK (
        escalated_at IS NULL OR recipient_type = 'human'
    );

CREATE INDEX idx_raised_hand_open_recipient
    ON raised_hand (workspace_id, recipient_type, recipient_id)
    WHERE status = 'open';
