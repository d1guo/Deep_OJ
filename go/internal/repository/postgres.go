/**
 * @file postgres.go
 * @brief PostgreSQL 数据库访问层
 * 
 * ===========================================================================
 * 连接池知识点 (面试必备)
 * ===========================================================================
 * 
 * 1. 为什么需要连接池?
 *    - 数据库连接创建开销大 (TCP 握手 + 认证)
 *    - 复用连接可大幅提高性能
 *    - pgx 内置连接池，无需手动管理
 * 
 * 2. 连接池参数:
 *    - MaxConns: 最大连接数 (一般 = CPU核数 * 2)
 *    - MinConns: 最小连接数 (保持预热)
 *    - MaxConnLifetime: 单连接最大生命周期
 *    - MaxConnIdleTime: 空闲连接超时时间
 * 
 * 3. pgx vs database/sql:
 *    - pgx 是原生 PostgreSQL 驱动
 *    - 支持 PostgreSQL 特有功能 (COPY, LISTEN/NOTIFY)
 *    - 性能更好，类型支持更完善
 * 
 * 4. Context 的使用:
 *    - 传递超时/取消信号
 *    - 防止慢查询占用连接
 */
package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/d1guo/deep_oj/internal/model"
)

// PostgresDB 封装 pgx 连接池
type PostgresDB struct {
	pool *pgxpool.Pool
}

// NewPostgresDB 创建 PostgreSQL 连接
func NewPostgresDB(ctx context.Context, connString string) (*PostgresDB, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, err
	}

	// 连接池配置
	config.MaxConns = 10
	config.MinConns = 2
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}

	// 测试连接
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}

	return &PostgresDB{pool: pool}, nil
}

// Close 关闭连接池
func (db *PostgresDB) Close() {
	db.pool.Close()
}

// CreateSubmission 创建提交记录
func (db *PostgresDB) CreateSubmission(ctx context.Context, s *model.Submission) error {
	query := `
		INSERT INTO submissions (job_id, problem_id, user_id, code, language, time_limit, memory_limit, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at, updated_at
	`
	return db.pool.QueryRow(ctx, query,
		s.JobID, s.ProblemID, s.UserID, s.Code, s.Language,
		s.TimeLimit, s.MemoryLimit, s.Status,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
}

// GetSubmission 根据 Job ID 获取提交
func (db *PostgresDB) GetSubmission(ctx context.Context, jobID string) (*model.Submission, error) {
	query := `
		SELECT id, job_id, problem_id, user_id, code, language, 
		       time_limit, memory_limit, status, result, created_at, updated_at
		FROM submissions
		WHERE job_id = $1
	`
	var s model.Submission
	err := db.pool.QueryRow(ctx, query, jobID).Scan(
		&s.ID, &s.JobID, &s.ProblemID, &s.UserID, &s.Code, &s.Language,
		&s.TimeLimit, &s.MemoryLimit, &s.Status, &s.Result,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// UpdateSubmission 更新提交状态和结果
func (db *PostgresDB) UpdateSubmission(ctx context.Context, jobID string, status string, result any) error {
	query := `
		UPDATE submissions
		SET status = $2, result = $3, updated_at = NOW()
		WHERE job_id = $1
	`
	_, err := db.pool.Exec(ctx, query, jobID, status, result)
	return err
}
