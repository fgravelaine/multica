-- SPIKE (not upstream): the referential catalog.
-- See migrations/481_referential.up.sql for why this is a catalog and not a
-- text field.

-- name: SeedReferentials :exec
-- Idempotent, like SeedIssueStatusEntries: the unique (workspace_id, key)
-- index makes a losing racer a no-op rather than an error, so a rolling deploy
-- seeding from several pods is safe.
--
-- The first four are the bodies of knowledge the design note names. The last
-- two came out of this fork's own hands: a product question no technical
-- referential can answer, and the honest "I cannot tell which one failed me".
INSERT INTO referential (workspace_id, key, name, description, is_system, position)
VALUES
    (@workspace_id::uuid, 'design_system',     'Design system',     'Components, tokens, layout and interaction patterns.', TRUE, 0),
    (@workspace_id::uuid, 'api_contract',      'API contract',      'Endpoint shapes, payloads, error semantics, versioning.', TRUE, 1),
    (@workspace_id::uuid, 'architecture',      'Architecture',      'Boundaries, data model, where a responsibility lives.', TRUE, 2),
    (@workspace_id::uuid, 'brand_register',    'Brand register',    'Tone, promise, what is said and what is not said yet.', TRUE, 3),
    (@workspace_id::uuid, 'product_direction', 'Product direction', 'What the product is for, and what it deliberately is not.', TRUE, 4),
    (@workspace_id::uuid, 'unclassified',      'Unclassified',      'The raiser could not tell which body of knowledge failed to answer.', TRUE, 5)
ON CONFLICT DO NOTHING;

-- name: ListReferentials :many
SELECT * FROM referential
WHERE workspace_id = @workspace_id
  AND (@include_archived::bool OR archived_at IS NULL)
ORDER BY position, key;

-- name: GetReferentialByKey :one
-- The validation a raise runs against. An archived referential is not a valid
-- target for a NEW hand, which is the whole point of archiving one, while the
-- hands that already name it keep resolving for display through ListReferentials.
SELECT * FROM referential
WHERE workspace_id = @workspace_id AND key = @key AND archived_at IS NULL;
