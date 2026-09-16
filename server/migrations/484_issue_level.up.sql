-- SPIKE (not upstream): the rung an issue declares itself to be.
--
-- Campaign → Mission → Objective → Task → Step. Until now the rung was DERIVED
-- from depth, and that cannot express the one thing the board has to find: an
-- orphan. An objective triggered on its own has no parent, so depth says 0, so
-- it IS a campaign — the thing you are looking for and the thing it is mistaken
-- for are byte-identical, and no filter can separate them.
--
-- Storing it makes the orphan a DISAGREEMENT:
--
--     orphan  ⇔  level is declared, and parentage says something else
--                e.g. level = 'objective' with no parent at all
--
-- NULL means undeclared, and undeclared falls back to depth. That is what keeps
-- this safe on a table with every issue in it: nothing existing changes meaning,
-- and an issue only becomes capable of being an orphan once somebody states
-- what it was meant to be.
--
-- Reparenting deliberately does NOT rewrite a declared level. Healing it
-- silently is exactly how you lose the signal — the disagreement is the report.
ALTER TABLE issue
    ADD COLUMN level TEXT
        CHECK (level IN ('campaign', 'mission', 'objective', 'task', 'step'));

-- The board's two reads: everything at one rung, and the orphans among them.
-- Partial, because the vast majority of rows are undeclared and derived.
CREATE INDEX idx_issue_declared_level
    ON issue (workspace_id, level)
    WHERE level IS NOT NULL;
