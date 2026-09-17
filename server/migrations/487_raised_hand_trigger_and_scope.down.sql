DROP TABLE IF EXISTS referential_entry;
ALTER TABLE raised_hand DROP CONSTRAINT IF EXISTS raised_hand_scope_needs_an_answer;
ALTER TABLE raised_hand DROP COLUMN IF EXISTS answer_scope;
ALTER TABLE raised_hand DROP COLUMN IF EXISTS trigger;
