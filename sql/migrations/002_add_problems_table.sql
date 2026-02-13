-- 002_add_problems_table.sql
CREATE TABLE IF NOT EXISTS problems (
    id SERIAL PRIMARY KEY,
    title VARCHAR(255) NOT NULL,
    description TEXT,
    difficulty VARCHAR(50) DEFAULT 'Medium',
    time_limit_ms INT NOT NULL DEFAULT 1000,
    memory_limit_mb INT NOT NULL DEFAULT 128,
    testcase_minio_path VARCHAR(255) NOT NULL, -- MinIO path, e.g., "problems/1.zip"
    testcase_hash VARCHAR(64) NOT NULL,       -- SHA-256 hash for cache validation
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Index for faster search
CREATE INDEX idx_problems_title ON problems(title);
