package repository

import (
	"context"
	"time"

	"github.com/d1guo/deep_oj/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultMaxConns        = 10
	defaultMinConns        = 2
	defaultMaxConnLifetime = time.Hour
	defaultMaxConnIdleTime = 30 * time.Minute
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
	config.MaxConns = int32(getEnvInt("PG_MAX_CONNS", defaultMaxConns))
	config.MinConns = int32(getEnvInt("PG_MIN_CONNS", defaultMinConns))
	lifetimeMin := getEnvInt("PG_MAX_CONN_LIFETIME_MIN", int(defaultMaxConnLifetime/time.Minute))
	idleMin := getEnvInt("PG_MAX_CONN_IDLE_MIN", int(defaultMaxConnIdleTime/time.Minute))
	config.MaxConnLifetime = time.Duration(lifetimeMin) * time.Minute
	config.MaxConnIdleTime = time.Duration(idleMin) * time.Minute

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
	var problemID interface{}
	if s.ProblemID > 0 {
		problemID = s.ProblemID
	}
	var userID interface{}
	if s.UserID > 0 {
		userID = s.UserID
	}

	query := `
		INSERT INTO submissions (job_id, problem_id, user_id, code, language, time_limit, memory_limit, status, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at
	`
	return db.pool.QueryRow(ctx, query,
		s.JobID, problemID, userID, s.Code, s.Language,
		s.TimeLimit, s.MemoryLimit, s.Status, s.State,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
}

// GetSubmission 根据 Job ID 获取提交
func (db *PostgresDB) GetSubmission(ctx context.Context, jobID string) (*model.Submission, error) {
	query := `
		SELECT id, job_id, COALESCE(problem_id, 0), COALESCE(user_id, 0), code, language,
		       time_limit, memory_limit, status, state, result, created_at, updated_at
		FROM submissions
		WHERE job_id = $1
	`
	var s model.Submission
	err := db.pool.QueryRow(ctx, query, jobID).Scan(
		&s.ID, &s.JobID, &s.ProblemID, &s.UserID, &s.Code, &s.Language,
		&s.TimeLimit, &s.MemoryLimit, &s.Status, &s.State, &s.Result,
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

// UpdateSubmissionResultIfNotDone 更新提交状态和结果，并返回端到端耗时 (秒)
// 幂等: 如果 state 已是 done，则不更新
func (db *PostgresDB) UpdateSubmissionResultIfNotDone(ctx context.Context, jobID string, status string, result any) (float64, bool, error) {
	query := `
		UPDATE submissions
		SET status = $2, result = $3, state = 'done', updated_at = NOW()
		WHERE job_id = $1 AND state != 'done'
		RETURNING EXTRACT(EPOCH FROM (NOW() - created_at))
	`
	var latency float64
	err := db.pool.QueryRow(ctx, query, jobID, status, result).Scan(&latency)
	if err == pgx.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return latency, true, nil
}

// UpdateSubmissionState updates lifecycle state (pending/processing/retry/done/failed)
func (db *PostgresDB) UpdateSubmissionState(ctx context.Context, jobID string, state string) error {
	query := `
		UPDATE submissions
		SET state = $2, updated_at = NOW()
		WHERE job_id = $1
	`
	_, err := db.pool.Exec(ctx, query, jobID, state)
	return err
}

// TryMarkSubmissionProcessing updates submission state with CAS semantics.
// Returns true only when exactly one pending row is advanced.
func (db *PostgresDB) TryMarkSubmissionProcessing(ctx context.Context, jobID string) (bool, error) {
	query := `
		UPDATE submissions
		SET status = 'running', state = 'processing', updated_at = NOW()
		WHERE job_id = $1 AND state = 'pending'
	`
	cmd, err := db.pool.Exec(ctx, query, jobID)
	if err != nil {
		return false, err
	}
	return cmd.RowsAffected() == 1, nil
}

// ClaimSubmissionForRun atomically claims a pending job and returns a new attempt_id fencing token.
func (db *PostgresDB) ClaimSubmissionForRun(ctx context.Context, jobID, leaseOwner string, leaseSec int) (int64, bool, error) {
	if leaseSec <= 0 {
		leaseSec = 60
	}
	query := `
		UPDATE submissions
		SET attempt_id = attempt_id + 1,
		    status = 'running',
		    state = 'processing',
		    lease_owner = $2,
		    lease_until = NOW() + make_interval(secs => $3),
		    error_code = NULL,
		    error_message = NULL,
		    updated_at = NOW()
		WHERE job_id = $1 AND state = 'pending'
		RETURNING attempt_id
	`
	var attemptID int64
	err := db.pool.QueryRow(ctx, query, jobID, leaseOwner, leaseSec).Scan(&attemptID)
	if err == pgx.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return attemptID, true, nil
}

// RefreshSubmissionLease refreshes lease_until only for the matching running attempt.
func (db *PostgresDB) RefreshSubmissionLease(ctx context.Context, jobID string, attemptID int64, leaseOwner string, leaseSec int) (bool, error) {
	if leaseSec <= 0 {
		leaseSec = 60
	}
	query := `
		UPDATE submissions
		SET lease_until = NOW() + make_interval(secs => $4),
		    updated_at = NOW()
		WHERE job_id = $1
		  AND attempt_id = $2
		  AND lease_owner = $3
		  AND status = 'running'
		  AND state = 'processing'
	`
	cmd, err := db.pool.Exec(ctx, query, jobID, attemptID, leaseOwner, leaseSec)
	if err != nil {
		return false, err
	}
	return cmd.RowsAffected() == 1, nil
}

// FinalizeSubmissionWithFence writes final result only for the exact running attempt.
func (db *PostgresDB) FinalizeSubmissionWithFence(ctx context.Context, jobID string, attemptID int64, status string, result any) (bool, error) {
	query := `
		UPDATE submissions
		SET status = $3,
		    state = 'done',
		    result = $4,
		    lease_owner = NULL,
		    lease_until = NULL,
		    updated_at = NOW()
		WHERE job_id = $1
		  AND attempt_id = $2
		  AND status = 'running'
		  AND state = 'processing'
	`
	cmd, err := db.pool.Exec(ctx, query, jobID, attemptID, status, result)
	if err != nil {
		return false, err
	}
	return cmd.RowsAffected() == 1, nil
}

// MarkSubmissionPoison marks a submission failed for unrecoverable protocol/payload errors.
func (db *PostgresDB) MarkSubmissionPoison(ctx context.Context, jobID, errorCode, errorMessage string) (bool, error) {
	query := `
		UPDATE submissions
		SET status = 'System Error',
		    state = 'done',
		    error_code = $2,
		    error_message = $3,
		    lease_owner = NULL,
		    lease_until = NULL,
		    updated_at = NOW()
		WHERE job_id = $1
		  AND state != 'done'
	`
	cmd, err := db.pool.Exec(ctx, query, jobID, errorCode, errorMessage)
	if err != nil {
		return false, err
	}
	return cmd.RowsAffected() == 1, nil
}

// GetPendingSubmissions 获取超时的 Pending 任务 (用于 Slow Path 恢复)
func (db *PostgresDB) GetPendingSubmissions(ctx context.Context, before time.Time, limit int) ([]*model.Submission, error) {
	query := `
		SELECT id, job_id, COALESCE(problem_id, 0), COALESCE(user_id, 0), code, language,
		       time_limit, memory_limit, status, state, created_at, updated_at
		FROM submissions
		WHERE state = 'pending' AND created_at < $1
		LIMIT $2
	`
	rows, err := db.pool.Query(ctx, query, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var submissions []*model.Submission
	for rows.Next() {
		var s model.Submission
		if err := rows.Scan(
			&s.ID, &s.JobID, &s.ProblemID, &s.UserID, &s.Code, &s.Language,
			&s.TimeLimit, &s.MemoryLimit, &s.Status, &s.State,
			&s.CreatedAt, &s.UpdatedAt,
		); err != nil {
			return nil, err
		}
		submissions = append(submissions, &s)
	}
	return submissions, nil
}
