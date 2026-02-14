-- Enforce referential integrity for submissions.problem_id/user_id
ALTER TABLE submissions
    ALTER COLUMN problem_id DROP DEFAULT;

ALTER TABLE submissions
    ALTER COLUMN user_id TYPE BIGINT USING NULLIF(user_id, 0)::BIGINT;

UPDATE submissions SET problem_id = NULL WHERE problem_id <= 0;
UPDATE submissions SET user_id = NULL WHERE user_id <= 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'submissions_problem_id_fkey'
    ) THEN
        ALTER TABLE submissions
            ADD CONSTRAINT submissions_problem_id_fkey
            FOREIGN KEY (problem_id) REFERENCES problems(id)
            ON DELETE SET NULL;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'submissions_user_id_fkey'
    ) THEN
        ALTER TABLE submissions
            ADD CONSTRAINT submissions_user_id_fkey
            FOREIGN KEY (user_id) REFERENCES users(id)
            ON DELETE SET NULL;
    END IF;
END $$;
