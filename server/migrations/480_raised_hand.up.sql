-- SPIKE (not upstream): the raised hand as an object.
--
-- Today a unit that stops and needs a decision it cannot take writes a
-- free-text comment. The comment carries the question but nothing else: no
-- bounded options, no cost of being wrong on each side, no recommendation, and
-- above all no way for answering it to *do* anything. Somebody reads it,
-- decides, and then separately remembers to un-park the work.
--
-- This table is the question that a decision can be applied to.
--
--   question        what is being asked, in one line
--   options         the bounded set of answers, JSONB:
--                     [{"key":"a","label":"...","cost":"what it costs to be
--                       wrong on this side"}, ...]
--                   `cost` is the field that makes a raised hand answerable in
--                   thirty seconds by someone who does not know the domain, and
--                   it is required per option for exactly that reason.
--   recommendation  the raiser's own pick. A raiser that stops without one has
--                   handed over its judgement along with the decision.
--   material        anything the decider needs to look at; free text here,
--                   which is where this spike stops short of the design note
--                   (its example hangs an image off each option).
--
-- WHAT IT DELIBERATELY DOES NOT ADD.
--
-- No new issue status. A raised hand parks its issue in `blocked`, which
-- already exists and already means this. Answering moves it to `todo`, and
-- `shouldEnqueueAgentTask` (handler/issue.go) then re-triggers the agent by
-- itself — the resume is not written here, it is the product's own promotion
-- rule doing what it already does.
--
-- No new resume mechanism either. The re-triggered task is the same
-- (agent_id, issue_id) pair, so GetLastTaskSession resumes the agent's existing
-- session. "Sabine reprend exactement au temps où elle s'était arrêtée" is not
-- built here; it is what session resume already does, for free.
--
-- No recipient column, and that IS a gap rather than a simplification. The
-- design note is emphatic that the lead is the normal destination and the human
-- the rare one — "la plupart des mains levées se referment contre un référentiel
-- ou chez le lead" — and that the two need separate counters because the second
-- is what measures autonomy. Every hand here goes to the human. See SPIKE-NOTES.

CREATE TABLE raised_hand (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id         UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    agent_id         UUID REFERENCES agent(id) ON DELETE SET NULL,
    -- The task that raised it. No FK: a raised hand must outlive the run that
    -- raised it — that is the entire point — and agent_task_queue rows are
    -- reachable by ON DELETE CASCADE from agent_runtime.
    task_id          UUID,

    question         TEXT NOT NULL CHECK (length(trim(question)) > 0),
    options          JSONB NOT NULL,
    recommendation   TEXT,
    material         TEXT,

    status           TEXT NOT NULL DEFAULT 'open'
                     CHECK (status IN ('open', 'answered')),
    chosen_option    TEXT,
    answer           TEXT,
    answered_by      UUID REFERENCES "user"(id) ON DELETE SET NULL,

    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    answered_at      TIMESTAMPTZ,

    -- At least two options: a raised hand with one option is not a question,
    -- and one with none is the free-text comment this table exists to replace.
    CONSTRAINT raised_hand_options_bounded
        CHECK (jsonb_typeof(options) = 'array' AND jsonb_array_length(options) BETWEEN 2 AND 6),

    -- answered is all-or-nothing.
    CONSTRAINT raised_hand_answer_complete CHECK (
        (status = 'open'     AND chosen_option IS NULL AND answered_at IS NULL)
        OR
        (status = 'answered' AND chosen_option IS NOT NULL AND answered_at IS NOT NULL)
    )
);

-- One open hand per issue. A unit that has stopped for a decision has stopped
-- once; a second open hand on the same issue would mean two answers are needed
-- before it can move, and nothing in the model expresses that ordering.
CREATE UNIQUE INDEX idx_raised_hand_one_open_per_issue
    ON raised_hand (issue_id)
    WHERE status = 'open';

CREATE INDEX idx_raised_hand_workspace_open
    ON raised_hand (workspace_id, created_at DESC)
    WHERE status = 'open';
