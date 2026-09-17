-- SPIKE (not upstream): T4 exists. Until now only its gate did.
--
-- Galactics' cycle makes VERIFY a beat with a declared output: "a verdict per
-- criterion, with its evidence". §22 built what CLOSES that beat and not the
-- beat itself, so `qa` was a place a unit waited rather than work anyone did.
-- The gate could answer "AP-5 said yes" and could not answer "what did AP-5
-- check, and what did it find".
--
-- Everything here is already written in agents/ap5.md. The tables do not invent
-- a process; they make four sentences an agent had to remember into things the
-- product holds:
--
--   "Each acceptance criterion gets its own line and its own verdict."
--   "Evidence or it did not happen. Steps to reproduce, expected, observed."
--   "No criteria, no verdict ... you do not invent them and you do not pass it."
--   "`qa` is yours, and only yours."                      (§22 did this one)
--
-- The criteria ALREADY EXIST in Galactics' issue template, as markdown
-- checkboxes under `## Acceptance Criteria`. They are a per-criterion object in
-- embryo that nothing reads. This gives them somewhere to be read from.
CREATE TABLE acceptance_criterion (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id     UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,

    -- Position in the list, so a verdict can be quoted as "criterion 2" and mean
    -- the same thing to the agent that wrote it and the human reading it.
    ordinal      INTEGER NOT NULL CHECK (ordinal > 0),

    statement    TEXT NOT NULL CHECK (length(trim(statement)) > 0),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT acceptance_criterion_ordinal_unique UNIQUE (issue_id, ordinal)
);

CREATE INDEX idx_acceptance_criterion_issue ON acceptance_criterion (issue_id, ordinal);

-- One row per verdict, and they ACCUMULATE. A re-check after a fix is a new
-- row, not an overwrite.
--
-- This is the difference between a status field and a record. Overwriting would
-- make "criterion 2 failed twice before passing" unaskable, and that sequence
-- is the whole input to "do the past answers predict the next ones" — the
-- question raised-hand.md uses to decide whether a category earns a reference,
-- an agent or a human.
CREATE TABLE criterion_verdict (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    criterion_id UUID NOT NULL REFERENCES acceptance_criterion(id) ON DELETE CASCADE,

    passed       BOOLEAN NOT NULL,

    -- "Evidence or it did not happen. Steps to reproduce, expected, observed.
    -- A failing verdict a developer cannot reproduce from your comment is an
    -- incomplete verdict."
    --
    -- NOT NULL and non-empty, for both outcomes. A pass with no evidence is the
    -- aggregate "works fine" that AP-5's file refuses by name, wearing a
    -- per-criterion shape.
    evidence     TEXT NOT NULL CHECK (length(trim(evidence)) > 0),

    -- Who ruled. `agent` is the normal case and the reason the column exists:
    -- a verdict is only worth reading if you know whether AP-5 produced it or
    -- somebody clicked it.
    author_type  TEXT NOT NULL CHECK (author_type IN ('agent', 'member')),
    author_id    UUID,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_criterion_verdict_latest ON criterion_verdict (criterion_id, created_at DESC);

-- Whether this gate reads verdicts as well as asking its ratifier.
--
-- A SEPARATE COLUMN from ratifier_type rather than a fourth kind, because they
-- are different questions and a real gate asks both: "did the right party say
-- yes" AND "does the evidence exist". AP-5 ratifying `qa` with no verdicts
-- behind it is the situation this whole table exists to end.
--
-- Default false: every gate declared before this migration keeps behaving
-- exactly as it did.
ALTER TABLE level_gate ADD COLUMN requires_verdicts BOOLEAN NOT NULL DEFAULT false;
