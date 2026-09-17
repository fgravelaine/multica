-- SPIKE (not upstream): why a hand went up, and what its answer binds.
--
-- Two halves of one loop, from Galactics' operating-model/cycle/raised-hand.md.
--
-- 1. THE TRIGGER, and the set is closed.
--
-- Three, and no fourth: a block found at framing, three failures in a row, a
-- contradiction discovered in flight. The reason the set is closed is stated
-- there and it is about measurement, not tidiness:
--
--     an open trigger set makes the raised hand the default exit, and then the
--     counter measures how tired an agent is rather than where the references
--     are thin.
--
-- This fork built both counters — the referential diagnostic and the per-lead
-- contest ratio — before reading that sentence. Without the column they count
-- what it says they count.
--
-- 2. THE SCOPE, and where an answer goes.
--
-- Galactics: three things come back — the answer, ITS LEVEL (a rule, or a local
-- choice), and the diff that follows. "A rule goes up into the reference and the
-- unit carries a pointer to it."
--
-- A local choice binds one unit and dies with it. A rule is the only thing that
-- makes a referential thicker, and this implementation had no way to say which
-- an answer was — so the diagnostic measured thin references with no path back
-- into them.
--
-- Both NULL for hands raised before this existed. A missing trigger is honestly
-- unrecorded rather than guessed at, and the metrics label it as such.
ALTER TABLE raised_hand
    ADD COLUMN trigger TEXT
        CHECK (trigger IN ('block', 'three_failures', 'contradiction')),
    ADD COLUMN answer_scope TEXT
        CHECK (answer_scope IN ('rule', 'local'));

-- A scope without an answer is a contradiction in terms.
ALTER TABLE raised_hand
    ADD CONSTRAINT raised_hand_scope_needs_an_answer
        CHECK (answer_scope IS NULL OR status = 'answered');

-- What an answered rule leaves behind.
--
-- A separate table rather than appending to referential.description, because a
-- referential is a CATALOG ROW and this is its content: many statements, each
-- with a date and the question that produced it. Squashing them into one text
-- field would lose exactly the thing that makes the loop auditable — which
-- decision put this rule here.
CREATE TABLE referential_entry (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    -- TEXT, not an FK, for the reason migration 481 gives: an entry must
    -- outlive the catalog row it names.
    referential_key TEXT NOT NULL,
    statement       TEXT NOT NULL,
    -- The hand that produced it. ON DELETE SET NULL, because the rule survives
    -- the question: what was decided stays true even if the asking is gone.
    source_hand_id  UUID REFERENCES raised_hand(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_referential_entry_lookup
    ON referential_entry (workspace_id, referential_key, created_at DESC);
