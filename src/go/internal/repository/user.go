package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/d1guo/deep_oj/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// CreateUser 创建用户
func (db *PostgresDB) CreateUser(ctx context.Context, u *model.User) error {
	query := `
		INSERT INTO users (username, password_hash, email)
		VALUES ($1, $2, $3)
		RETURNING id, created_at
	`
	// Handle NULL password_hash by using sql.NullString or just empty string checks if schematic allows
	// Here we assume u.PasswordHash is properly set or empty for non-password users (but schema allows null)
	// Ideally we should use *string for nullable fields in struct, but for simplicity:
	var pwdHash interface{} = u.PasswordHash
	if u.PasswordHash == "" {
		pwdHash = nil
	}

	return db.pool.QueryRow(ctx, query,
		u.Username, pwdHash, u.Email,
	).Scan(&u.ID, &u.CreatedAt)
}

// GetUserByUsername 根据用户名查询用户
func (db *PostgresDB) GetUserByUsername(ctx context.Context, username string) (*model.User, error) {
	query := `
		SELECT id, username, COALESCE(password_hash, ''), COALESCE(email, ''), COALESCE(github_id, ''), COALESCE(avatar_url, ''), created_at
		FROM users
		WHERE username = $1
	`
	var u model.User
	err := db.pool.QueryRow(ctx, query, username).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Email, &u.GitHubID, &u.AvatarURL, &u.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // Not found
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetOrCreateByGitHubID 根据 GitHub ID 获取或创建用户 (处理用户名冲突)
func (db *PostgresDB) GetOrCreateByGitHubID(ctx context.Context, githubID, username, email, avatarURL string) (*model.User, error) {
	// 1. 尝试查找现有用户
	queryFind := `
		SELECT id, username, COALESCE(password_hash, ''), COALESCE(email, ''), github_id, COALESCE(avatar_url, ''), created_at
		FROM users
		WHERE github_id = $1
	`
	var u model.User
	err := db.pool.QueryRow(ctx, queryFind, githubID).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Email, &u.GitHubID, &u.AvatarURL, &u.CreatedAt,
	)

	if err == nil {
		// 用户存在，检查头像是否更新
		if u.AvatarURL != avatarURL {
			_, _ = db.pool.Exec(ctx, "UPDATE users SET avatar_url = $1 WHERE id = $2", avatarURL, u.ID)
			u.AvatarURL = avatarURL
		}
		return &u, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// 2. 用户不存在，尝试创建 (带冲突重试)
	currentUsername := username
	for i := 0; i < 5; i++ {
		queryInsert := `
			INSERT INTO users (username, github_id, email, avatar_url, password_hash)
			VALUES ($1, $2, $3, $4, NULL)
			RETURNING id, created_at
		`
		err := db.pool.QueryRow(ctx, queryInsert,
			currentUsername, githubID, email, avatarURL,
		).Scan(&u.ID, &u.CreatedAt)

		if err == nil {
			u.Username = currentUsername
			u.GitHubID = githubID
			u.Email = email
			u.AvatarURL = avatarURL
			return &u, nil
		}

		// 检查是否是 Unique Constraint Violation (username)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			if pgErr.ConstraintName == "users_github_id_key" {
				// 极端并发情况: 刚刚查还没有，现在有了 -> 直接回查
				errFind := db.pool.QueryRow(ctx, queryFind, githubID).Scan(
					&u.ID, &u.Username, &u.PasswordHash, &u.Email, &u.GitHubID, &u.AvatarURL, &u.CreatedAt,
				)
				if errFind == nil {
					return &u, nil
				}
				if !errors.Is(errFind, pgx.ErrNoRows) {
					return nil, errFind
				}
				continue
			}
			// Username 冲突 -> 生成后续
			suffix, _ := generateRandomHex(2) // 4 chars
			currentUsername = fmt.Sprintf("%s_%s", username, suffix)
			continue
		}

		return nil, err
	}

	return nil, fmt.Errorf("failed to create user after retries due to username conflicts")
}

func generateRandomHex(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
