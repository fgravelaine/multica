-- SPIKE (not upstream): the gate, and who ratifies it.
--
-- Galactics' levels.md says a level declares five parameters. Three of them had
-- somewhere to live in Multica already (the reference is `referential`, the unit
-- is the issue, the delegation boundary is the parent/child tree). Two did not:
-- WHAT HAS THE RIGHT TO SAY NO at this level, and WHO ACCEPTS THE RETURN.
--
-- `in_review` was the near miss. The whole product respects it — agents park
-- completed work there, the notification listeners treat it as the dominant
-- "this needs you now", the runtime sweeper refuses to reset it. But nothing
-- validates a transition anywhere in Multica: `resolveIssueStatusKey` checks
-- that the key exists in the workspace catalog and is not archived, and that is
-- the whole of it. `in_review` -> `done` is unguarded, by anyone. So it is a
-- place that holds a unit, not a gate that can refuse to release it.
--
-- ORDERED, NOT PARALLEL, and that was a decision rather than a default. Two
-- reviewers on one unit can either queue or both hold it. Galactics already
-- runs the queued form — its own state flow is `in review -> qa -> done`, where
-- "in review puts the change proposal in front of a reviewer, qa puts the
-- acceptance criteria in front of AP-5". Parallel k-of-n ratification has no
-- worked example on either side, and a tally nobody has exercised would be a
-- guess about how a team reviews. Recorded as a gap instead.
--
-- Worth writing down: levels.md gives a level ONE gate cell and ONE ratifier
-- cell, but its own table already breaks that. N3's gate reads "AP-5 against
-- the criteria, gate-pr" — two checks — and N4's ratifier reads "the gate, then
-- the merge" — a sequence. Multiple gates are not a deviation from Galactics.
-- They are its undeclared practice, and this table is the first thing that
-- declares them.
CREATE TABLE level_gate (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    level        TEXT NOT NULL CHECK (level IN ('campaign', 'mission', 'objective', 'task', 'step')),

    -- The order the gates are walked in. 1-based, and gaps are allowed so a
    -- gate can be inserted between two others without renumbering the rest.
    position     INTEGER NOT NULL CHECK (position > 0),

    -- The status a unit sits in while this gate holds it.
    --
    -- TEXT, not an FK to issue_status, and the reason runs the other way from
    -- the usual one. An FK would either block archiving a status or cascade the
    -- gate away with it — and a gate that disappears silently is a rung that
    -- stops refusing anything while still looking configured. A dangling
    -- status_key is visible; a deleted gate is not. Validated on write instead.
    status_key   TEXT NOT NULL,

    -- Who accepts the return. `human` is any member of the workspace: the floor
    -- is that a person passed it, not which person. `agent` is one named agent,
    -- which is what AP-5 against acceptance criteria looks like here.
    --
    -- `lead` is deliberately ABSENT. raised_hand resolves a lead relative to the
    -- agent that raised the hand, and a gate has no raiser. Making it mean "the
    -- assignee's lead" is a different definition, and inventing one to fill a
    -- column is how a vocabulary drifts. It is one CHECK change away when
    -- somebody says what it should mean.
    ratifier_type TEXT NOT NULL CHECK (ratifier_type IN ('human', 'agent')),
    ratifier_id   UUID REFERENCES agent(id) ON DELETE SET NULL,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A named agent needs naming. A human ratifier must NOT carry an id: it
    -- would look like "this one person" while the check only tests that the
    -- actor is a human, and a constraint that reads as narrower than it enforces
    -- is worse than no constraint.
    CONSTRAINT level_gate_agent_needs_an_id
        CHECK ((ratifier_type = 'agent') = (ratifier_id IS NOT NULL))
);

-- One gate per position per rung, and one gate per status per rung. The second
-- is the load-bearing one: two gates sharing a status would make "which gate is
-- this unit sitting at" unanswerable, and the whole walk depends on that lookup.
CREATE UNIQUE INDEX idx_level_gate_position ON level_gate (workspace_id, level, position);
CREATE UNIQUE INDEX idx_level_gate_status ON level_gate (workspace_id, level, status_key);
