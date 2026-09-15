-- SPIKE (not upstream): queries for the raised hand.
-- See migrations/480_raised_hand.up.sql for the model and what it omits.

-- name: RaiseHand :one
INSERT INTO raised_hand (
    workspace_id, issue_id, agent_id, task_id,
    question, options, recommendation, material, referential_key
) VALUES (
    @workspace_id, @issue_id, sqlc.narg(agent_id), sqlc.narg(task_id),
    @question, @options, sqlc.narg(recommendation), sqlc.narg(material),
    sqlc.narg(referential_key)
)
RETURNING *;

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
SET status        = 'answered',
    chosen_option = @chosen_option,
    answer        = sqlc.narg(answer),
    answered_by   = sqlc.narg(answered_by),
    answered_at   = now()
WHERE id = @id AND status = 'open'
RETURNING *;
