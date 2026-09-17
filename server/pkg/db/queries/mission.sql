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

-- name: CountIssueAncestors :one
-- How many parents sit above this issue, so its rung is its TRUE depth.
--
-- The tree query numbers depth from the root it was asked for. That is right
-- for layout and wrong for the ladder: open the view on a mission and its own
-- root would be numbered 0 and labelled a campaign. This walks up instead, so
-- a unit's rung is the same wherever you entered from.
--
-- Bounded by the same depth guard as the walk down. An issue deeper than the
-- bound reports the bound, which floors it at `step` — the right answer, since
-- everything past the bound is a step anyway.
WITH RECURSIVE up AS (
    SELECT i.id, i.parent_issue_id, 0::int AS height
    FROM issue i
    WHERE i.id = @issue_id

    UNION ALL

    SELECT p.id, p.parent_issue_id, u.height + 1
    FROM issue p
    JOIN up u ON u.parent_issue_id = p.id
    WHERE u.height + 1 <= @max_depth::int
)
SELECT COALESCE(MAX(height), 0)::int AS ancestors FROM up;

-- ── The leveled board ───────────────────────────────────────────────────────
--
-- Two queries, both over one recursive walk of the workspace. The walk carries
-- three things nothing else can give a row: its depth (so an undeclared issue
-- still has a rung), its root (so the board can filter to one campaign), and
-- its declared level (so a disagreement with depth is visible as an orphan).
--
-- The depth→rung CASE is duplicated in Go as missionLevel(). That is deliberate
-- and it is the smaller evil: the alternative is shipping every issue in the
-- workspace to Go to be labelled. A test pins the two together.

-- name: CountIssuesByLevel :many
-- How many units sit at each rung, and how many of them are orphans.
WITH RECURSIVE walk AS (
    SELECT i.id, i.parent_issue_id, i.level, 0::int AS depth
    FROM issue i
    WHERE i.workspace_id = @workspace_id AND i.parent_issue_id IS NULL

    UNION ALL

    SELECT c.id, c.parent_issue_id, c.level, w.depth + 1
    FROM issue c
    JOIN walk w ON c.parent_issue_id = w.id
    WHERE w.depth + 1 <= @max_depth::int
),
rung AS (
    SELECT
        w.level AS declared,
        CASE
            WHEN w.depth = 0 THEN 'campaign'
            WHEN w.depth = 1 THEN 'mission'
            WHEN w.depth = 2 THEN 'objective'
            WHEN w.depth = 3 THEN 'task'
            ELSE 'step'
        END AS derived
    FROM walk w
)
SELECT
    COALESCE(declared, derived)::text                                   AS level,
    COUNT(*)::int                                                       AS total,
    -- The disagreement. Declared and parentage do not agree, which is the only
    -- thing that makes an orphan findable at all.
    COUNT(*) FILTER (WHERE declared IS NOT NULL AND declared <> derived)::int AS orphans
FROM rung
GROUP BY 1;

-- name: ListIssuesAtLevel :many
-- Every unit at one rung, with what it hangs from.
--
-- root_id is the campaign the unit belongs to — carried down the walk rather
-- than re-derived per row, which is what lets the board filter to one campaign
-- without a query per unit.
WITH RECURSIVE walk AS (
    SELECT i.id, i.parent_issue_id, i.level, 0::int AS depth, i.id AS root_id
    FROM issue i
    WHERE i.workspace_id = @workspace_id AND i.parent_issue_id IS NULL

    UNION ALL

    SELECT c.id, c.parent_issue_id, c.level, w.depth + 1, w.root_id
    FROM issue c
    JOIN walk w ON c.parent_issue_id = w.id
    WHERE w.depth + 1 <= @max_depth::int
),
rung AS (
    SELECT
        w.*,
        CASE
            WHEN w.depth = 0 THEN 'campaign'
            WHEN w.depth = 1 THEN 'mission'
            WHEN w.depth = 2 THEN 'objective'
            WHEN w.depth = 3 THEN 'task'
            ELSE 'step'
        END AS derived
    FROM walk w
)
SELECT
    i.id,
    i.number,
    i.title,
    i.status,
    i.updated_at,
    i.last_activity_at,
    r.depth,
    r.level                                        AS declared_level,
    COALESCE(r.level, r.derived)::text             AS level,
    (r.level IS NOT NULL AND r.level <> r.derived) AS orphan,
    r.root_id                                      AS campaign_id,
    root.number                                    AS campaign_number,
    root.title                                     AS campaign_title,
    parent.number                                  AS parent_number,
    parent.title                                   AS parent_title
FROM rung r
JOIN issue i ON i.id = r.id
JOIN issue root ON root.id = r.root_id
LEFT JOIN issue parent ON parent.id = r.parent_issue_id
WHERE COALESCE(r.level, r.derived) = @level::text
  -- Both filters are optional and independent: "orphans only", "one campaign",
  -- or both at once.
  AND (NOT @orphans_only::boolean OR (r.level IS NOT NULL AND r.level <> r.derived))
  AND (sqlc.narg('campaign_id')::uuid IS NULL OR r.root_id = sqlc.narg('campaign_id')::uuid)
ORDER BY i.last_activity_at DESC NULLS LAST, i.number DESC;

-- name: ListMissionDependencies :many
-- What blocks these units, from the dependency table rather than the barrier.
--
-- The barrier can only order SIBLINGS under one parent, so it cannot express
-- "this task waits on a task in another mission, owned by another squad" — and
-- that is the ordinary case the moment two teams share a release. issue_
-- dependency is the product's own table for it (migration 034-era, type
-- blocks/blocked_by/related) and it had no reader, no writer and no rows.
--
-- Both directions are read. The table stores a direction in `type`, and the
-- same fact can be written from either end: A says "I am blocked_by B", or B
-- says "I block A". A reader that honoured only one spelling would show half
-- the dependencies in a workspace and look like it was working.
--
-- `related` is excluded: it is a cross-reference, not a wait.
--
-- The blocker is joined from `issue` with no workspace or subtree restriction
-- ON PURPOSE. A blocker outside this tree is exactly the case this exists for,
-- and hiding it would reproduce the barrier's limitation in the query that was
-- written to escape it.
SELECT
    d.id,
    d.type,
    blocked.id                 AS blocked_issue_id,
    blocker.id                 AS blocker_issue_id,
    blocker.number             AS blocker_number,
    blocker.title              AS blocker_title,
    blocker.status             AS blocker_status,
    blocker.assignee_type      AS blocker_assignee_type,
    blocker.assignee_id        AS blocker_assignee_id,
    blocker.parent_issue_id    AS blocker_parent_id
FROM issue_dependency d
-- 'blocked_by': issue_id is blocked by depends_on_issue_id.
-- 'blocks':     issue_id blocks depends_on_issue_id, so the roles swap.
JOIN issue blocked
    ON blocked.id = CASE WHEN d.type = 'blocked_by' THEN d.issue_id ELSE d.depends_on_issue_id END
JOIN issue blocker
    ON blocker.id = CASE WHEN d.type = 'blocked_by' THEN d.depends_on_issue_id ELSE d.issue_id END
WHERE d.type IN ('blocked_by', 'blocks')
  AND blocked.id = ANY(@issue_ids::uuid[]);

-- name: AddIssueDependency :one
-- SPIKE: the write path this table never had.
--
-- Always stored as 'blocked_by' from the blocked unit's side, so the table has
-- one spelling going forward even though the reader tolerates both. A unit
-- cannot block itself, and the same pair is not recorded twice.
INSERT INTO issue_dependency (issue_id, depends_on_issue_id, type)
SELECT @issue_id, @depends_on_issue_id, 'blocked_by'
WHERE @issue_id::uuid <> @depends_on_issue_id::uuid
  AND NOT EXISTS (
      SELECT 1 FROM issue_dependency existing
      WHERE existing.issue_id = @issue_id
        AND existing.depends_on_issue_id = @depends_on_issue_id
        AND existing.type = 'blocked_by'
  )
RETURNING *;

-- name: RemoveIssueDependency :exec
DELETE FROM issue_dependency
WHERE issue_id = @issue_id
  AND depends_on_issue_id = @depends_on_issue_id
  AND type = 'blocked_by';

-- ── What a rung costs to run ────────────────────────────────────────────────

-- name: ListLevelPolicies :many
-- Every rung policy in the workspace. Read once per claim, and empty for a
-- workspace that has set none — which is the default and costs one small
-- indexed read.
SELECT id, level, model, thinking_level, service_tier, updated_at
FROM level_policy
WHERE workspace_id = @workspace_id
ORDER BY level;

-- name: UpsertLevelPolicy :one
-- Set what a rung runs on. Each field is independently clearable, which is why
-- this is a full replace rather than a COALESCE merge: a merge cannot express
-- "stop overriding the model but keep overriding thinking".
INSERT INTO level_policy (workspace_id, level, model, thinking_level, service_tier)
VALUES (@workspace_id, @level, sqlc.narg(model), sqlc.narg(thinking_level), sqlc.narg(service_tier))
ON CONFLICT (workspace_id, level) DO UPDATE
SET model          = EXCLUDED.model,
    thinking_level = EXCLUDED.thinking_level,
    service_tier   = EXCLUDED.service_tier,
    updated_at     = now()
RETURNING *;

-- name: DeleteLevelPolicy :exec
DELETE FROM level_policy WHERE workspace_id = @workspace_id AND level = @level;

-- SPIKE: the gates a rung declares, in the order they are walked.
-- name: ListLevelGates :many
SELECT * FROM level_gate
WHERE workspace_id = @workspace_id AND level = @level
ORDER BY position;

-- name: ListAllLevelGates :many
SELECT * FROM level_gate
WHERE workspace_id = @workspace_id
ORDER BY level, position;

-- name: SetLevelGate :one
INSERT INTO level_gate (workspace_id, level, position, status_key, ratifier_type, ratifier_id, required_checks, requires_verdicts)
VALUES (@workspace_id, @level, @position, @status_key, @ratifier_type, sqlc.narg('ratifier_id'), sqlc.narg('required_checks')::text[], @requires_verdicts)
ON CONFLICT (workspace_id, level, position) DO UPDATE
SET status_key        = EXCLUDED.status_key,
    ratifier_type     = EXCLUDED.ratifier_type,
    ratifier_id       = EXCLUDED.ratifier_id,
    required_checks   = EXCLUDED.required_checks,
    requires_verdicts = EXCLUDED.requires_verdicts
RETURNING *;

-- SPIKE: every check run on every change proposal attached to this issue,
-- restricted to each PR's CURRENT head.
--
-- The head_sha join is the load-bearing part. Check runs accumulate per commit,
-- so without it a green run from three commits ago would release a gate on a
-- head that has not been checked at all — the exact failure a scripted gate
-- exists to prevent, and silent.
--
-- LEFT JOIN so a linked PR with no checks yet still returns a row. "A PR exists
-- and reports nothing" and "no PR is attached" are different refusals, and the
-- caller cannot tell them apart from an empty result.
-- name: ListIssueCheckRuns :many
SELECT
    pr.id            AS pr_id,
    pr.pr_number     AS pr_number,
    pr.head_sha      AS head_sha,
    cr.name          AS check_name,
    cr.status        AS check_status,
    cr.conclusion    AS check_conclusion
FROM issue_pull_request ipr
JOIN github_pull_request pr ON pr.id = ipr.pull_request_id
LEFT JOIN github_pull_request_check_run cr
       ON cr.pr_id = pr.id AND cr.head_sha = pr.head_sha
WHERE ipr.issue_id = @issue_id
ORDER BY pr.pr_number, cr.ordinal;

-- name: DeleteLevelGate :exec
DELETE FROM level_gate
WHERE workspace_id = @workspace_id AND level = @level AND position = @position;
