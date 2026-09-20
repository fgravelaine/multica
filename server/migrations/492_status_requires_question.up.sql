-- A status may declare that a unit sitting in it must carry an open question.
--
-- `needs discussion` is the team's own gate: "not startable until a
-- consultation resolves it". Nothing recorded what the consultation was ABOUT,
-- so the status was a label. Measured 2026-09-20 on a mirrored backlog: five
-- issues in it, ZERO carrying a question, the oldest waiting since 2026-09-10.
--
-- The flag lives on the STATUS rather than in code because `needs_discussion`
-- is not a built-in — it is a custom status this workspace created, and naming
-- its key in a handler would weld one team's vocabulary into the product. A
-- workspace that calls it something else, or has two such statuses, or none,
-- is then expressible without a migration.
--
-- Default false: every status that exists today keeps behaving exactly as it
-- does. Turning it on is a deliberate act by whoever owns the catalog.
ALTER TABLE issue_status
    ADD COLUMN requires_question BOOLEAN NOT NULL DEFAULT false;
