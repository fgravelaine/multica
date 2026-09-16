-- SPIKE (not upstream): the mission view.
--
-- READ ONLY. Every statement in this file is a SELECT. The view creates
-- nothing, changes no status, and stores nothing of its own — everything it
-- shows is derived from rows the product already writes.
--
-- Five queries, and that is the whole budget regardless of how big the tree is.
-- The rule the view must not break is "no request per node": one HTTP call, a
-- fixed number of round trips. Query 1 collects the subtree, and queries 2-5
-- each take that id set as an array.

-- name: ListMissionTree :many
-- The tree under one issue, breadth-first, with a depth bound.
--
-- Columns are named rather than `i.*` for two reasons: a recursive CTE requires
-- both arms to agree column-for-column, and the payload is a wire response —
-- description and properties have no business travelling for every node in a
-- forty-ticket mission.
--
-- max_depth bounds the walk. An issue tree is acyclic by construction
-- (parent_issue_id is set on create and a cycle would already break the
-- existing child queries), so the bound is a payload guard, not a cycle guard.
WITH RECURSIVE tree AS (
    SELECT
        i.id,
        i.parent_issue_id,
        i.number,
        i.title,
        i.status,
        i.priority,
        i.assignee_type,
        i.assignee_id,
        i.stage,
        i.position,
        i.created_at,
        i.updated_at,
        0::int AS depth
    FROM issue i
    WHERE i.id = @root_id AND i.workspace_id = @workspace_id

    UNION ALL

    SELECT
        c.id,
        c.parent_issue_id,
        c.number,
        c.title,
        c.status,
        c.priority,
        c.assignee_type,
        c.assignee_id,
        c.stage,
        c.position,
        c.created_at,
        c.updated_at,
        t.depth + 1
    FROM issue c
    JOIN tree t ON c.parent_issue_id = t.id
    WHERE t.depth + 1 <= @max_depth::int
)
SELECT * FROM tree
ORDER BY depth, stage NULLS LAST, position, number;

-- name: ListMissionIssueUsage :many
-- Token and cost totals per issue, for a set of issues.
--
-- The aggregation is GetIssueUsageSummary's `usage` CTE, grouped by issue
-- instead of filtered to one — same join, same columns, same cost_usd_ticks
-- unit. The view does not recompute cost and does not convert it: ticks travel
-- to the client exactly as every other usage endpoint sends them.
--
-- Issues with no metered run return no row rather than a row of zeros, so the
-- client can tell "never ran" from "ran and cost nothing".
SELECT
    atq.issue_id,
    COALESCE(SUM(tu.input_tokens), 0)::bigint        AS total_input_tokens,
    COALESCE(SUM(tu.output_tokens), 0)::bigint       AS total_output_tokens,
    COALESCE(SUM(tu.cache_read_tokens), 0)::bigint   AS total_cache_read_tokens,
    COALESCE(SUM(tu.cache_write_tokens), 0)::bigint  AS total_cache_write_tokens,
    COALESCE(SUM(tu.cost_usd_ticks), 0)::bigint      AS total_cost_usd_ticks,
    COUNT(DISTINCT tu.task_id)::int                  AS task_count
FROM task_usage tu
JOIN agent_task_queue atq ON atq.id = tu.task_id
WHERE atq.issue_id = ANY(@issue_ids::uuid[])
GROUP BY atq.issue_id;

-- name: ListMissionOpenHands :many
-- Open raised hands anywhere in the tree, with the agent that raised each.
--
-- referential_key is what the grouping reads. The agent is still joined, but
-- only to name the raiser on the row — it is no longer the grouping key.
SELECT
    h.id,
    h.issue_id,
    h.question,
    h.options,
    h.recommendation,
    h.created_at,
    h.agent_id,
    h.referential_key,
    h.recipient_type,
    h.recipient_id,
    h.escalated_at,
    a.name AS agent_name,
    lead.name AS lead_name
FROM raised_hand h
LEFT JOIN agent a ON a.id = h.agent_id
LEFT JOIN agent lead ON lead.id = h.recipient_id
WHERE h.issue_id = ANY(@issue_ids::uuid[])
  AND h.status = 'open'
ORDER BY h.created_at;

-- name: ListMissionLatestTasks :many
-- The most recent run per issue.
--
-- Only the latest matters here: the view reports whether an issue is currently
-- waiting because its last run failed, not a run history. The issue's own
-- execution log already shows the history, and the view links to it rather
-- than redrawing it.
SELECT DISTINCT ON (atq.issue_id)
    atq.issue_id,
    atq.id AS task_id,
    atq.status,
    atq.failure_reason,
    atq.wait_reason,
    atq.dispatched_at,
    atq.started_at,
    atq.completed_at,
    atq.created_at
FROM agent_task_queue atq
WHERE atq.issue_id = ANY(@issue_ids::uuid[])
ORDER BY atq.issue_id, atq.created_at DESC, atq.id DESC;

-- name: ListMissionLatestStatusChanges :many
-- When each issue last changed status, and to what.
--
-- This is how long a unit has been parked. The issue row itself cannot answer
-- it: `updated_at` moves for any edit and `last_activity_at` moves for any
-- comment, so both would report a unit as freshly parked because someone
-- renamed it. activity_log's status_changed row is the only record of the
-- transition itself.
--
-- The `to` status comes back so the caller can check it still matches the
-- issue's current status. When it does not — a status written by a path that
-- logs no activity row — the caller falls back to updated_at and says so
-- rather than reporting a wrong duration.
SELECT DISTINCT ON (al.issue_id)
    al.issue_id,
    al.created_at,
    al.details->>'to' AS to_status
FROM activity_log al
WHERE al.issue_id = ANY(@issue_ids::uuid[])
  AND al.action = 'status_changed'
ORDER BY al.issue_id, al.created_at DESC, al.id DESC;

-- ── The index ───────────────────────────────────────────────────────────────
--
-- Two queries for the whole list, not one per mission. The same rule the
-- detail endpoint follows, applied one level up: a list that fans out is a list
-- that gets slower the more work you do.

-- name: GetMissionCampaign :one
-- The campaign a mission belongs to.
--
-- Multica calls it a project and it is a real entity — which is why the naming
-- ladder needs no fifth word invented for the top. A mission may have none;
-- the caller renders that as "no campaign" rather than hiding the line, because
-- an unattached mission is a fact worth seeing.
SELECT p.id, p.title, p.icon, p.status
FROM project p
JOIN issue i ON i.project_id = p.id
WHERE i.id = @issue_id;

-- name: ListMissionRoots :many
-- Every mission in the workspace.
--
-- A mission is not an entity in this product. It is a top-level issue that has
-- children — which is exactly what the mission view can say something about,
-- and a leaf is one the board already answers. The EXISTS is the whole
-- definition; nothing new is stored to make this list.
SELECT
    i.id,
    i.number,
    i.title,
    i.status,
    i.updated_at,
    i.last_activity_at
FROM issue i
WHERE i.workspace_id = @workspace_id
  AND i.parent_issue_id IS NULL
  AND EXISTS (SELECT 1 FROM issue c WHERE c.parent_issue_id = i.id)
ORDER BY i.last_activity_at DESC NULLS LAST, i.number DESC;

-- name: ListMissionRollups :many
-- Per mission, how many units carry each status, and how many open hands.
--
-- Grouped by status rather than reduced to done/total because "terminal" is not
-- a SQL fact: it depends on the workspace's status catalog, which the
-- issuestatus resolver owns. Re-deriving that here would be a second copy of
-- the rule, and the two would drift. The caller resolves.
--
-- The hand count is a LATERAL rather than a join so that a node with three open
-- hands stays ONE row — a plain join would multiply the unit counts by the
-- hands and quietly inflate every total.
WITH RECURSIVE tree AS (
    SELECT i.id AS mission_id, i.id AS node_id, 0::int AS depth
    FROM issue i
    WHERE i.id = ANY(@mission_ids::uuid[])

    UNION ALL

    SELECT t.mission_id, c.id, t.depth + 1
    FROM issue c
    JOIN tree t ON c.parent_issue_id = t.node_id
    WHERE t.depth + 1 <= @max_depth::int
)
SELECT
    t.mission_id,
    n.status,
    COUNT(*)::int                       AS units,
    COALESCE(SUM(hands.n), 0)::bigint   AS open_hands
FROM tree t
JOIN issue n ON n.id = t.node_id
LEFT JOIN LATERAL (
    SELECT COUNT(*) AS n
    FROM raised_hand h
    WHERE h.issue_id = n.id AND h.status = 'open'
) hands ON TRUE
-- The root itself is not one of its own units.
WHERE t.node_id <> t.mission_id
GROUP BY t.mission_id, n.status;
