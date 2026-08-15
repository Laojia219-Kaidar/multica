ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_review_state_closed_enum;
ALTER TABLE issue DROP COLUMN IF EXISTS review_state_reason;
ALTER TABLE issue DROP COLUMN IF EXISTS review_state;
