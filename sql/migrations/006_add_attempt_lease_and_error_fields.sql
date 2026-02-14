ALTER TABLE submissions
    ADD COLUMN IF NOT EXISTS attempt_id BIGINT NOT NULL DEFAULT 0;

ALTER TABLE submissions
    ADD COLUMN IF NOT EXISTS lease_owner TEXT;

ALTER TABLE submissions
    ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ;

ALTER TABLE submissions
    ADD COLUMN IF NOT EXISTS error_code TEXT;

ALTER TABLE submissions
    ADD COLUMN IF NOT EXISTS error_message TEXT;

CREATE INDEX IF NOT EXISTS idx_submissions_lease_until ON submissions(lease_until);
