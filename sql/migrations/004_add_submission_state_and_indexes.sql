ALTER TABLE submissions
    ADD COLUMN IF NOT EXISTS state TEXT DEFAULT 'pending';

-- Indexes for common query patterns
CREATE INDEX IF NOT EXISTS idx_submissions_user_id ON submissions(user_id);
CREATE INDEX IF NOT EXISTS idx_submissions_problem_id ON submissions(problem_id);
CREATE INDEX IF NOT EXISTS idx_submissions_created_at ON submissions(created_at);
CREATE INDEX IF NOT EXISTS idx_submissions_state ON submissions(state);
