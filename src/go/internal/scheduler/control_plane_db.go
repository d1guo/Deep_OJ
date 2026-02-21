package scheduler

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func OpenControlPlanePool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}

	maxConns := getEnvInt("PG_MAX_CONNS", 10)
	if maxConns > 0 {
		cfg.MaxConns = int32(maxConns)
	}
	minConns := getEnvInt("PG_MIN_CONNS", 2)
	if minConns >= 0 {
		cfg.MinConns = int32(minConns)
	}
	maxLifetimeMin := getEnvInt("PG_MAX_CONN_LIFETIME_MIN", 60)
	if maxLifetimeMin > 0 {
		cfg.MaxConnLifetime = time.Duration(maxLifetimeMin) * time.Minute
	}
	maxIdleMin := getEnvInt("PG_MAX_CONN_IDLE_MIN", 30)
	if maxIdleMin > 0 {
		cfg.MaxConnIdleTime = time.Duration(maxIdleMin) * time.Minute
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
