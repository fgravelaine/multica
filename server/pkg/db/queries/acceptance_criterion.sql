-- SPIKE: T4's output — a verdict per criterion, with its evidence.

-- name: ListAcceptanceCriteria :many
SELECT * FROM acceptance_criterion
WHERE issue_id = @issue_id
ORDER BY ordinal;

-- name: AddAcceptanceCriterion :one
INSERT INTO acceptance_criterion (workspace_id, issue_id, ordinal, statement)
VALUES (@workspace_id, @issue_id, @ordinal, @statement)
ON CONFLICT (issue_id, ordinal) DO UPDATE SET statement = EXCLUDED.statement
RETURNING *;

-- name: DeleteAcceptanceCriteria :exec
DELETE FROM acceptance_criterion WHERE issue_id = @issue_id;

-- name: NextCriterionOrdinal :one
SELECT COALESCE(MAX(ordinal), 0) + 1 FROM acceptance_criterion WHERE issue_id = @issue_id;

-- name: GetAcceptanceCriterion :one
SELECT * FROM acceptance_criterion WHERE id = @id;

-- name: RecordCriterionVerdict :one
INSERT INTO criterion_verdict (workspace_id, criterion_id, passed, evidence, author_type, author_id)
VALUES (@workspace_id, @criterion_id, @passed, @evidence, @author_type, sqlc.narg('author_id'))
RETURNING *;

-- SPIKE: the LATEST verdict per criterion for one issue, and criteria that have
-- none.
--
-- DISTINCT ON over created_at DESC rather than a boolean on the criterion:
-- verdicts accumulate, and only the most recent one decides whether the gate
-- opens. A LEFT JOIN so a criterion nobody has ruled on comes back with a NULL
-- verdict — "not yet checked" and "checked and failed" are different refusals.
-- name: ListLatestVerdictsForIssue :many
SELECT
    c.id         AS criterion_id,
    c.ordinal    AS ordinal,
    c.statement  AS statement,
    -- COALESCE, and RULED_AT is what says whether these mean anything.
    --
    -- sqlc infers non-null from the LATERAL and generates a plain `bool`, so a
    -- bare v.passed fails the scan on a criterion nobody has ruled on. A CASE
    -- degrades it to interface{}, which is worse. Coalescing keeps the types
    -- and moves the two-state answer onto ruled_at, which IS typed nullable.
    --
    -- So: `passed` is meaningless unless `ruled_at.Valid`. Reading it without
    -- checking would turn "nobody looked" into "it failed".
    COALESCE(v.passed, false) AS passed,
    COALESCE(v.evidence, '')  AS evidence,
    v.created_at AS ruled_at
FROM acceptance_criterion c
LEFT JOIN LATERAL (
    SELECT passed, evidence, created_at
    FROM criterion_verdict
    WHERE criterion_id = c.id
    ORDER BY created_at DESC
    LIMIT 1
) v ON TRUE
WHERE c.issue_id = @issue_id
ORDER BY c.ordinal;

-- name: ListCriterionVerdicts :many
SELECT * FROM criterion_verdict
WHERE criterion_id = @criterion_id
ORDER BY created_at DESC;
