package model

import (
	"time"
)

// Submission 提交记录
type Submission struct {
	ID          int64     `json:"id" db:"id"`
	JobID       string    `json:"job_id" db:"job_id"`
	ProblemID   int       `json:"problem_id,omitempty" db:"problem_id"`
	UserID      int64     `json:"user_id,omitempty" db:"user_id"`
	Code        string    `json:"code" db:"code"`
	Language    int       `json:"language" db:"language"`
	TimeLimit   int       `json:"time_limit" db:"time_limit"`
	MemoryLimit int       `json:"memory_limit" db:"memory_limit"`
	Status      string    `json:"status" db:"status"`
	State       string    `json:"state" db:"state"`
	Result      any       `json:"result,omitempty" db:"result"` // JSONB
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" db:"updated_at"`
}

// JudgeResult 判题结果 (存储在 Result 字段中)
type JudgeResult struct {
	Verdict     string `json:"verdict"`     // AC, WA, TLE, MLE, RE, CE
	TimeUsed    int    `json:"time_used"`   // ms
	MemoryUsed  int    `json:"memory_used"` // KB
	Score       int    `json:"score"`       // 得分
	Message     string `json:"message,omitempty"`
	TestCases   int    `json:"test_cases,omitempty"`
	PassedCases int    `json:"passed_cases,omitempty"`
}

// TaskRequest 任务请求 (发送给 Worker)
type TaskRequest struct {
	JobID       string `json:"job_id"`
	Code        string `json:"code"`
	Language    int    `json:"language"`
	TimeLimit   int    `json:"time_limit"`
	MemoryLimit int    `json:"memory_limit"`
	CacheKey    string `json:"cache_key"`
	ProblemID   int    `json:"problem_id,omitempty"`
}
