-- SPIKE (not upstream): a gate that reads a script's verdict instead of asking.
--
-- Galactics' PR gate is 13 checks: 9 `script`, 2 `CI`, 2 `agent review`. Before
-- this migration `level_gate` could express the last two and nothing else — the
-- minority case. levels.md's N4 names the majority outright: "lint, tests,
-- contract diff — ZERO TOKENS". Zero because nobody is asked.
--
-- I previously wrote that this needed a check runner Multica does not have, and
-- filed it beside the protocol-change wall. That was wrong. Multica needs no
-- runner: GitHub is the runner, and Multica ALREADY ingests the results. It
-- handles `check_run`, `check_suite` and `status` webhooks, stores one row per
-- check in `github_pull_request_check_run` with its name, status and
-- conclusion, and renders a rollup to clients. Every one of those nine scripts
-- already lands in this database. Nothing was allowed to refuse on it.
ALTER TABLE level_gate DROP CONSTRAINT IF EXISTS level_gate_ratifier_type_check;
ALTER TABLE level_gate ADD CONSTRAINT level_gate_ratifier_type_check
    CHECK (ratifier_type IN ('human', 'agent', 'check'));

-- The check names that must conclude `success`, exactly as GitHub reports them.
--
-- A LIST, not one name, because a gate that can only require one check would
-- force nine gates to express gate-pr — and nine gates is nine statuses, which
-- is a board nobody can read. The ordering inside the array is not meaningful;
-- all of them must pass.
--
-- Names are free text rather than a catalog. There is nothing to validate
-- against: a check's name is whatever the workflow calls its job, it can be
-- renamed upstream without warning, and a gate naming a check that never
-- reports must FAIL CLOSED rather than be refused at declaration time. A typo
-- here is a gate that never opens, which is loud. The alternative — validating
-- against checks seen so far — would refuse a correct name the first time it is
-- used, before that check has ever run.
ALTER TABLE level_gate ADD COLUMN required_checks TEXT[];

-- Each ratifier kind carries exactly what it needs and nothing else.
--
-- `check` with no names would be a gate that verifies nothing and opens for
-- anyone — the worst failure mode available here, because it reads as
-- configured. `agent` with no id would let any agent accept its own return.
ALTER TABLE level_gate DROP CONSTRAINT IF EXISTS level_gate_agent_needs_an_id;
ALTER TABLE level_gate ADD CONSTRAINT level_gate_ratifier_carries_its_own
    CHECK (
        (ratifier_type = 'agent') = (ratifier_id IS NOT NULL)
        AND (ratifier_type = 'check') = (required_checks IS NOT NULL AND cardinality(required_checks) > 0)
    );
