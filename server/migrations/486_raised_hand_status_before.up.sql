-- SPIKE (not upstream): where the unit was when it raised its hand.
--
-- A defect against a specification that already existed. Galactics'
-- operating-model/cycle/raised-hand.md names four things a tool must carry,
-- "both the minimum and the maximum", and the first is:
--
--     a state that remembers where it came from
--
-- and, on the resume: "the unit resumes at the beat it stopped at. Not at T1."
--
-- This implementation parked to a CONSTANT (backlog) and resumed to a CONSTANT
-- (todo). So an issue that was in_review when its hand went up came back as
-- todo, and the work waiting on a reviewer silently became work waiting to be
-- picked up. The parenthesis did not close where it opened.
--
-- NULL means a hand raised before this column existed. Those still resume to
-- the old constant, because inventing a previous status for them would be
-- worse than admitting it was never recorded.
ALTER TABLE raised_hand
    ADD COLUMN status_before TEXT;
