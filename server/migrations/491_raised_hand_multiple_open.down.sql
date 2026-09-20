-- Reversing this fails while any issue holds two open hands, which is correct:
-- the rows would have to be discarded to restore the constraint, and dropping a
-- question nobody answered is not something a migration may decide.
DROP INDEX IF EXISTS idx_raised_hand_issue_open;

CREATE UNIQUE INDEX idx_raised_hand_one_open_per_issue
    ON raised_hand (issue_id)
    WHERE status = 'open';
