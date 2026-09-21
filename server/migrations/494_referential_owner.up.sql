-- A body of knowledge may have an owner.
--
-- A raised hand names the referential that failed to answer it. Until now
-- nothing said who holds that referential, so "which agent is concerned by
-- this question" could only be guessed from the question's words — and a guess
-- that maps `design_system` to a persona by name would weld one team's roster
-- into the product.
--
-- With an owner it is not a guess. The hand says which reference was thin; the
-- reference says whose it is. That is raised-hand.md's own "the counter decides
-- the staffing" read forwards: a category of question earns an agent, and the
-- agent then owns the category.
--
-- Nullable and soft, like every other agent reference here: an unowned
-- referential is the ordinary case, and deleting an agent leaves the
-- referential standing rather than taking it down.
ALTER TABLE referential
    ADD COLUMN owner_agent_id UUID;
