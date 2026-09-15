-- SPIKE (not upstream): queries for the raised hand.
-- See migrations/480_raised_hand.up.sql for the model and what it omits.

-- name: RaiseHand :one
INSERT INTO raised_hand (
    workspace_id, issue_id, agent_id, task_id,
    question, options, recommendation, material, referential_key,
    recipient_type, recipient_id
) VALUES (
    @workspace_id, @issue_id, sqlc.narg(agent_id), sqlc.narg(task_id),
    @question, @options, sqlc.narg(recommendation), sqlc.narg(material),
    sqlc.narg(referential_key),
    @recipient_type, sqlc.narg(recipient_id)
)
RETURNING *;

-- name: ResolveHandLead :one
-- The lead a raising agent answers to: the leader of the squad it belongs to.
--
-- Returns no row when the agent is in no squad, or when it IS the leader —
-- both mean there is nobody above it, so the hand goes straight to the human.
-- That is the honest answer rather than a fallback that pretends at delegation.
--
-- An archived squad is skipped: a retired squad's leader is not a live
-- recipient, and addressing a hand to one would park it where nothing looks.
SELECT s.leader_id
FROM squad_member sm
JOIN squad s ON s.id = sm.squad_id
WHERE sm.member_type = 'agent'
  AND sm.member_id = @agent_id
  AND s.workspace_id = @workspace_id
  AND s.archived_at IS NULL
  AND s.leader_id <> @agent_id
ORDER BY s.created_at
LIMIT 1;

-- name: EscalateRaisedHand :one
-- The lead giving up: the hand moves to the human and records that it passed
-- through a lead first. CAS on the current shape so a second escalation, or an
-- escalation of a hand that was never with a lead, writes nothing.
UPDATE raised_hand
SET recipient_type       = 'human',
    -- The lead moves from "who has it" to "who had it". recipient_id is the
    -- present tense and must not claim a hand is still with a lead; the
    -- history column is what the per-lead contest ratio counts.
    escalated_by_lead_id = recipient_id,
    recipient_id         = NULL,
    escalated_at         = now(),
    escalation_note      = sqlc.narg(escalation_note)
WHERE id = @id
  AND status = 'open'
  AND recipient_type = 'lead'
RETURNING *;

-- name: CountMissionHands :one
-- The two counters, over a set of issues.
--
-- `reached_human` is the one that measures autonomy — hands that a lead could
-- not settle, plus hands that had no lead to try. `settled_by_lead` is the
-- parenthesis closing without the human, which is the whole point of having a
-- recipient at all.
SELECT
    COUNT(*)::int                                                          AS total,
    COUNT(*) FILTER (WHERE recipient_type = 'human')::int                   AS reached_human,
    COUNT(*) FILTER (WHERE escalated_at IS NOT NULL)::int                   AS escalated,
    COUNT(*) FILTER (WHERE answered_by_level = 'lead')::int                 AS settled_by_lead,
    COUNT(*) FILTER (WHERE answered_by_level = 'human')::int                AS settled_by_human,
    COUNT(*) FILTER (WHERE status = 'open')::int                            AS still_open
FROM raised_hand
WHERE issue_id = ANY(@issue_ids::uuid[]);

-- name: GetOpenHandForIssue :one
SELECT * FROM raised_hand
WHERE issue_id = @issue_id AND status = 'open';

-- name: GetRaisedHand :one
SELECT * FROM raised_hand WHERE id = @id;

-- name: ListHandsForIssue :many
SELECT * FROM raised_hand
WHERE issue_id = @issue_id
ORDER BY created_at DESC;

-- name: ListOpenHandsInWorkspace :many
SELECT h.*, i.number AS issue_number, i.title AS issue_title
FROM raised_hand h
JOIN issue i ON i.id = h.issue_id
WHERE h.workspace_id = @workspace_id AND h.status = 'open'
ORDER BY h.created_at DESC;

-- name: AnswerRaisedHand :one
-- The CAS on status = 'open' is what makes answering idempotent under two
-- people clicking at once: the loser updates no row and the caller reports the
-- hand as already answered rather than overwriting the first decision.
UPDATE raised_hand
SET status            = 'answered',
    chosen_option     = @chosen_option,
    answer            = sqlc.narg(answer),
    answered_by       = sqlc.narg(answered_by),
    answered_by_level = @answered_by_level,
    answered_at       = now()
WHERE id = @id AND status = 'open'
RETURNING *;

-- name: ListMissionLeadContest :many
-- SPIKE: the contest ratio, per lead.
--
-- The lead's job before escalating is to CONTEST — check whether the answer
-- already exists in a referential it can reach. Nothing enforces that, and
-- nothing could: a lead that forwards a question unchanged looks identical to
-- one that checked first and found nothing. What CAN be measured is the
-- outcome, and settled-against-escalated is exactly it.
--
-- Per lead rather than per referential, which is the Prometheus cut: this one
-- answers "is this lead contesting or relaying", the metric answers "is this
-- referential answerable by a lead at all". Two different failures that look
-- the same in a single number.
--
-- Only hands that reached a lead are counted. A hand raised by an agent with no
-- squad never had a contest step to skip, and including it would penalise a
-- lead for work it never saw.
SELECT
    COALESCE(h.recipient_id, h.escalated_by_lead_id)::uuid    AS lead_id,
    a.name                                                    AS lead_name,
    COUNT(*)::int                                             AS total,
    COUNT(*) FILTER (WHERE h.answered_by_level = 'lead')::int  AS settled,
    COUNT(*) FILTER (WHERE h.escalated_at IS NOT NULL)::int    AS escalated,
    COUNT(*) FILTER (WHERE h.status = 'open')::int             AS still_open
FROM raised_hand h
LEFT JOIN agent a ON a.id = COALESCE(h.recipient_id, h.escalated_by_lead_id)
WHERE h.issue_id = ANY(@issue_ids::uuid[])
  AND COALESCE(h.recipient_id, h.escalated_by_lead_id) IS NOT NULL
GROUP BY 1, 2
ORDER BY 3 DESC;
