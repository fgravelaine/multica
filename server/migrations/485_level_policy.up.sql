-- SPIKE (not upstream): what a rung costs to run.
--
-- An agent carries one model, one thinking level and one service tier, so
-- "Obi-Wan cheap here, expensive there" means copying Obi-Wan — and a copied
-- persona splits every per-agent measurement with it: his raised hands, his
-- contest ratio, his autonomy numbers. You end up reading two half-Obi-Wans.
--
-- This moves the three settings off the agent and onto the RUNG, so one
-- persona keeps one identity and the level of the work picks how it runs.
--
-- Honest about the evidence: on this workspace's ~22 runs, tokens per run did
-- NOT vary by rung (126k / 122k / 117k across campaign, mission, objective).
-- The rung is a claim about scope and blast radius, not a measurement of
-- difficulty, and there are not yet enough real runs to say whether it
-- predicts cost. This is a deliberate bet taken in that knowledge, and the
-- first thing to check once real numbers exist is whether it held.
--
-- Nothing here is required. An empty table means every run uses the agent's
-- own settings, exactly as today.
CREATE TABLE level_policy (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    level         TEXT NOT NULL CHECK (level IN ('campaign', 'mission', 'objective', 'task', 'step')),

    -- All three nullable and independent: a workspace may want a cheaper model
    -- at the step rung without touching thinking or tier, and NULL has to mean
    -- "leave the agent's value alone" rather than "clear it".
    model          TEXT,
    thinking_level TEXT,
    service_tier   TEXT,

    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A row that sets nothing is not a policy, it is a row that will confuse
    -- the next reader into thinking the rung is covered.
    CONSTRAINT level_policy_sets_something
        CHECK (model IS NOT NULL OR thinking_level IS NOT NULL OR service_tier IS NOT NULL)
);

-- One policy per rung per workspace. Also the lookup the claim path makes.
CREATE UNIQUE INDEX idx_level_policy_workspace_level ON level_policy (workspace_id, level);
