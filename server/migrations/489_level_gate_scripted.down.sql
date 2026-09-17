ALTER TABLE level_gate DROP CONSTRAINT IF EXISTS level_gate_ratifier_carries_its_own;
ALTER TABLE level_gate DROP COLUMN IF EXISTS required_checks;
ALTER TABLE level_gate DROP CONSTRAINT IF EXISTS level_gate_ratifier_type_check;
ALTER TABLE level_gate ADD CONSTRAINT level_gate_ratifier_type_check
    CHECK (ratifier_type IN ('human', 'agent'));
ALTER TABLE level_gate ADD CONSTRAINT level_gate_agent_needs_an_id
    CHECK ((ratifier_type = 'agent') = (ratifier_id IS NOT NULL));
