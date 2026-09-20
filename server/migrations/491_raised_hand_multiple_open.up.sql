-- A unit may be parked on SEVERAL questions at once.
--
-- 480 allowed one, and said why: "a second open hand on the same issue would
-- mean two answers are needed before it can move, and nothing in the model
-- expresses that ordering." The premise is right and the conclusion was one
-- step too far. Two answers ARE needed — and no ordering is required, because
-- the open hands are a SET that has to empty, not a queue that has to drain.
-- What expresses it is the promotion rule, not the index: the unit resumes
-- when the LAST open hand is answered.
--
-- Found by probing a real ticket. GALACTEAM-136 says, in its own words, "What
-- is open: Four questions, at a stop gate." Three of the four were refused with
-- a 409. The alternative was to split the unit into four whose only content is
-- "decide X" — and operating-model/cycle/raised-hand.md calls a raised hand
-- "the step without a rank", while T2 splits into WORK units. Filing a decision
-- as a work item uses the wrong instrument.
--
-- The guard 480 wanted is not lost, it moves: a second question could be
-- "silent and nobody answers it" only if the unit could move while it was open.
-- It cannot. That is stricter than what this replaces, where answering the
-- first of several would have released a unit still blocked on the rest.
DROP INDEX IF EXISTS idx_raised_hand_one_open_per_issue;

-- Open hands are now read per issue rather than found as a single row, so the
-- lookup that was served by the unique index above needs its own non-unique one.
CREATE INDEX idx_raised_hand_issue_open
    ON raised_hand (issue_id)
    WHERE status = 'open';
