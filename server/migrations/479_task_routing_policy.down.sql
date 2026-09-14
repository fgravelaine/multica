ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_routing_policy_shape;
ALTER TABLE issue DROP COLUMN IF EXISTS routing_policy;

DROP INDEX IF EXISTS idx_agent_task_queue_routed_candidates;

ALTER TABLE agent_task_queue
    DROP CONSTRAINT IF EXISTS agent_task_queue_routing_policy_shape;

ALTER TABLE agent_task_queue
    DROP COLUMN IF EXISTS routing_policy;
