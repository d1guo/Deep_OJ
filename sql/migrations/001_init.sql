-- SQL 迁移: 初始化数据库 Schema
-- PostgreSQL

-- 提交记录表
CREATE TABLE IF NOT EXISTS submissions (
    id BIGSERIAL PRIMARY KEY,
    job_id VARCHAR(64) UNIQUE NOT NULL,
    problem_id INTEGER,
    user_id BIGINT,
    code TEXT NOT NULL,
    language INTEGER NOT NULL,
    time_limit INTEGER NOT NULL DEFAULT 1000,
    memory_limit INTEGER NOT NULL DEFAULT 65536,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    result JSONB,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_submissions_job_id ON submissions(job_id);
CREATE INDEX IF NOT EXISTS idx_submissions_status ON submissions(status);
CREATE INDEX IF NOT EXISTS idx_submissions_created_at ON submissions(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_submissions_problem_id ON submissions(problem_id);
CREATE INDEX IF NOT EXISTS idx_submissions_user_id ON submissions(user_id);

-- JSONB 索引 (用于查询结果中的字段)
CREATE INDEX IF NOT EXISTS idx_submissions_result ON submissions USING GIN(result);

-- 触发器: 自动更新 updated_at
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ language 'plpgsql';

DROP TRIGGER IF EXISTS update_submissions_updated_at ON submissions;
CREATE TRIGGER update_submissions_updated_at
    BEFORE UPDATE ON submissions
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- 注释
COMMENT ON TABLE submissions IS '代码提交记录表';
COMMENT ON COLUMN submissions.job_id IS '任务唯一标识 (UUID)';
COMMENT ON COLUMN submissions.status IS '状态: pending/running/finished/error';
COMMENT ON COLUMN submissions.result IS '判题结果 (JSONB)';
