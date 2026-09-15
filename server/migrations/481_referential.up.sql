-- SPIKE (not upstream): the referential a raised hand interrogates.
--
-- The mission view's diagnostic is a COUNT — twelve hands on the design system
-- and none on architecture says which body of knowledge is too thin to answer
-- on its own. A count needs a stable key, which is the whole reason this is a
-- catalog and not a text field: "design system", "design-system" and "Design
-- System" would be three referentials and the diagnostic would be worthless.
--
-- Shaped after issue_status, which is this repo's answer to exactly this
-- problem — a per-workspace controlled vocabulary with seeded built-ins that a
-- workspace can extend. Same seeding style (idempotent, ON CONFLICT DO
-- NOTHING, safe under a rolling deploy), same archived_at rather than delete,
-- same key CHECK so `--referential design_system` is unambiguous to type.
--
-- The built-ins are the four the design note names, plus two that came out of
-- this fork's own hands: `product_direction` for a question no technical
-- referential can answer, and `unclassified` for the honest case where the
-- raiser does not know which body of knowledge failed it. `unclassified` being
-- a real, countable key matters: a pile of hands nobody could classify is
-- itself a finding, and hiding it in a NULL would lose it.

CREATE TABLE referential (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    key          TEXT NOT NULL CHECK (key ~ '^[a-z0-9][a-z0-9_]{0,31}$'),
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    is_system    BOOLEAN NOT NULL DEFAULT FALSE,
    position     DOUBLE PRECISION NOT NULL DEFAULT 0,
    archived_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- What makes the seed idempotent and a concurrent second seeder a no-op.
CREATE UNIQUE INDEX idx_referential_workspace_key ON referential (workspace_id, key);

-- raised_hand.referential_key is TEXT, not an FK.
--
-- Deliberate, and the same call migration 480 made for task_id: a hand must
-- outlive the catalog row it names. Archiving a referential must not erase the
-- history of what it was asked, and a workspace that renames its vocabulary
-- must not silently rewrite six months of diagnostics. The key is resolved for
-- display and falls back to itself when the catalog no longer has it.
--
-- NULL is confined to history, the way migration 251 confined a task's NULL
-- runtime: every hand raised from now on must declare a referential (the API
-- refuses one without), and NULL means only "raised before this column
-- existed". The view buckets those separately rather than folding them into
-- `unclassified`, because "nobody was asked" and "the raiser could not tell"
-- are different facts.
ALTER TABLE raised_hand
    ADD COLUMN referential_key TEXT;

CREATE INDEX idx_raised_hand_open_referential
    ON raised_hand (workspace_id, referential_key)
    WHERE status = 'open';
