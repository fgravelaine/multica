-- SPIKE (not upstream): a task carries a routing policy.
--
-- Today a task's runtime is a denormalised copy of `agent.runtime_id`, and four
-- places say so — the enqueue that stamps it, the two claim fences in
-- service/task.go, and `a.runtime_id = atq.runtime_id` inside ClaimAgentTask,
-- whose own comment is "A task's persisted runtime is not authority after an
-- agent rebind." The agent owns the machine; the task only records which one it
-- was at the time.
--
-- routing_policy is the task saying something the agent did not: *this* run
-- belongs on a machine chosen by this rule, whatever the agent is bound to.
--
--   NULL                -> inherit the agent's binding. Exactly today's
--                          behaviour, which is why every existing row and every
--                          untouched code path keeps working.
--   'runtime:<uuid>'    -> pin to one machine.
--   'provider:<name>'   -> any online runtime with that provider.
--
-- WHAT THIS COLUMN CANNOT DO, and why it matters more than what it can.
--
-- Migration 251 added:
--
--   CHECK (runtime_id IS NOT NULL OR completed_at IS NOT NULL)
--
-- with the stated intent that "an ACTIVE task must always have a runtime, so
-- claim / dispatch / delivery-CAS paths can never observe runtime_id IS NULL".
-- NULL is confined to history on purpose.
--
-- So a policy-carrying task STILL has to name a machine at INSERT time. The
-- policy cannot defer the first resolution; it can only make the stamped
-- runtime provisional and let the claim re-resolve it. "Resolved at dispatch"
-- is therefore always a RE-resolution in this model, never a first one — and a
-- task whose policy matches no registered runtime yet is not representable at
-- all. That is the wall, and this migration deliberately does not tunnel
-- through it: the CHECK is left exactly as migration 251 wrote it.
--
-- The index mirrors idx_agent_task_queue_claim_candidates, which is keyed on
-- runtime_id and therefore cannot serve a policy-matched candidate scan: a
-- policy task is claimable by a runtime its runtime_id does not name.

ALTER TABLE agent_task_queue
    ADD COLUMN routing_policy TEXT;

ALTER TABLE agent_task_queue
    ADD CONSTRAINT agent_task_queue_routing_policy_shape
    CHECK (
        routing_policy IS NULL
        OR routing_policy ~ '^runtime:[0-9a-fA-F-]{36}$'
        OR routing_policy ~ '^provider:[a-z0-9_-]{1,64}$'
    );

CREATE INDEX idx_agent_task_queue_routed_candidates
    ON agent_task_queue (routing_policy, priority DESC, created_at)
    WHERE status = 'queued' AND routing_policy IS NOT NULL;

-- The issue carries the policy, and every task it enqueues inherits it.
--
-- The alternative was an agent-level or workspace-level default, and that is
-- the same mistake this spike is testing: it would put the routing decision
-- back on the executor. The unit of work is what knows it needs a particular
-- machine — "this one needs the box with the GPU", "this one must not leave my
-- laptop" — so the unit of work is where the policy belongs.

ALTER TABLE issue
    ADD COLUMN routing_policy TEXT;

ALTER TABLE issue
    ADD CONSTRAINT issue_routing_policy_shape
    CHECK (
        routing_policy IS NULL
        OR routing_policy ~ '^runtime:[0-9a-fA-F-]{36}$'
        OR routing_policy ~ '^provider:[a-z0-9_-]{1,64}$'
    );
