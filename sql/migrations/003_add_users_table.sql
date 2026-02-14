-- SQL Migration: Check users table
-- Required columns: id, username, password_hash, email, github_id, avatar_url, created_at

CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    username VARCHAR(64) UNIQUE NOT NULL,
    password_hash VARCHAR(255),
    email VARCHAR(255),
    github_id VARCHAR(64),
    avatar_url TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

WITH ranked AS (
    SELECT id, github_id,
           ROW_NUMBER() OVER (PARTITION BY github_id ORDER BY id) AS rn
    FROM users
    WHERE github_id IS NOT NULL
)
UPDATE users u
SET github_id = NULL
FROM ranked r
WHERE u.id = r.id AND r.rn > 1;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'users_github_id_key'
    ) THEN
        ALTER TABLE users ADD CONSTRAINT users_github_id_key UNIQUE (github_id);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_github_id ON users(github_id);

COMMENT ON TABLE users IS '用户表';
