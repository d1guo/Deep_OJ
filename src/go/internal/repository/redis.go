package repository

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
)

const (
	defaultPoolSize     = 10
	defaultMinIdleConns = 3
	defaultDialTimeout  = 5 * time.Second
	defaultReadTimeout  = 3 * time.Second
	defaultWriteTimeout = 3 * time.Second
)

// RedisClient 封装 Redis 操作
type RedisClient struct {
	client *redis.Client
}

// NewRedisClient 创建 Redis 客户端
func NewRedisClient(addr string) *RedisClient {
	poolSize := getEnvInt("REDIS_POOL_SIZE", defaultPoolSize)
	minIdle := getEnvInt("REDIS_MIN_IDLE_CONNS", defaultMinIdleConns)
	dialTimeout := time.Duration(getEnvInt("REDIS_DIAL_TIMEOUT_MS", int(defaultDialTimeout/time.Millisecond))) * time.Millisecond
	readTimeout := time.Duration(getEnvInt("REDIS_READ_TIMEOUT_MS", int(defaultReadTimeout/time.Millisecond))) * time.Millisecond
	writeTimeout := time.Duration(getEnvInt("REDIS_WRITE_TIMEOUT_MS", int(defaultWriteTimeout/time.Millisecond))) * time.Millisecond

	opts := &redis.Options{
		Addr: addr,
		DB:   0, // 默认 DB
	}
	if strings.HasPrefix(addr, "redis://") || strings.HasPrefix(addr, "rediss://") {
		if parsed, err := redis.ParseURL(addr); err == nil {
			opts = parsed
		}
	}

	if opts.Password == "" {
		opts.Password = os.Getenv("REDIS_PASSWORD")
	}
	if opts.TLSConfig == nil {
		if strings.HasPrefix(addr, "rediss://") || getEnvBool("REDIS_TLS", false) {
			opts.TLSConfig = &tls.Config{
				MinVersion: tls.VersionTLS12,
			}
		}
	}
	opts.PoolSize = poolSize
	opts.MinIdleConns = minIdle
	opts.DialTimeout = dialTimeout
	opts.ReadTimeout = readTimeout
	opts.WriteTimeout = writeTimeout

	client := redis.NewClient(opts)

	return &RedisClient{client: client}
}

// Ping 测试连接
func (r *RedisClient) Ping(ctx context.Context) error {
	if err := r.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}

// Close 关闭连接
func (r *RedisClient) Close() error {
	return r.client.Close()
}

// =========================================================================
// String 操作 (缓存)
// =========================================================================

// Get 获取值
func (r *RedisClient) Get(ctx context.Context, key string) (string, error) {
	result, err := r.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", err
	}
	if err != nil {
		return "", fmt.Errorf("redis get %s: %w", key, err)
	}
	return result, nil
}

// Set 设置值
func (r *RedisClient) Set(ctx context.Context, key, value string, expiration time.Duration) error {
	if err := r.client.Set(ctx, key, value, expiration).Err(); err != nil {
		return fmt.Errorf("redis set %s: %w", key, err)
	}
	return nil
}

// SetNX sets value if not exists
func (r *RedisClient) SetNX(ctx context.Context, key, value string, expiration time.Duration) (bool, error) {
	val, err := r.client.SetNX(ctx, key, value, expiration).Result()
	if err != nil {
		return false, fmt.Errorf("redis setnx %s: %w", key, err)
	}
	return val, nil
}

// Del 删除键
func (r *RedisClient) Del(ctx context.Context, keys ...string) error {
	if err := r.client.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("redis del: %w", err)
	}
	return nil
}

// Incr 自增
func (r *RedisClient) Incr(ctx context.Context, key string) (int64, error) {
	val, err := r.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("redis incr %s: %w", key, err)
	}
	return val, nil
}

// Decr 自减
func (r *RedisClient) Decr(ctx context.Context, key string) (int64, error) {
	val, err := r.client.Decr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("redis decr %s: %w", key, err)
	}
	return val, nil
}

// Expire 设置过期时间
func (r *RedisClient) Expire(ctx context.Context, key string, expiration time.Duration) (bool, error) {
	val, err := r.client.Expire(ctx, key, expiration).Result()
	if err != nil {
		return false, fmt.Errorf("redis expire %s: %w", key, err)
	}
	return val, nil
}

// =========================================================================
// List 操作 (任务队列)
// =========================================================================

// LPush 左侧入队
func (r *RedisClient) LPush(ctx context.Context, key string, values ...interface{}) error {
	if err := r.client.LPush(ctx, key, values...).Err(); err != nil {
		return fmt.Errorf("redis lpush %s: %w", key, err)
	}
	return nil
}

// RPop 右侧出队
func (r *RedisClient) RPop(ctx context.Context, key string) (string, error) {
	val, err := r.client.RPop(ctx, key).Result()
	if err != nil && err != redis.Nil {
		return "", fmt.Errorf("redis rpop %s: %w", key, err)
	}
	return val, err
}

// BRPop 阻塞右侧出队
func (r *RedisClient) BRPop(ctx context.Context, timeout time.Duration, keys ...string) ([]string, error) {
	result, err := r.client.BRPop(ctx, timeout, keys...).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("redis brpop: %w", err)
	}
	return result, nil
}

// BRPopLPush 阻塞弹出并推入另一队列 (可靠投递)
func (r *RedisClient) BRPopLPush(ctx context.Context, source, destination string, timeout time.Duration) (string, error) {
	val, err := r.client.BRPopLPush(ctx, source, destination, timeout).Result()
	if err != nil && err != redis.Nil {
		return "", fmt.Errorf("redis brpoplpush: %w", err)
	}
	return val, err
}

// LRem 移除队列中的元素
func (r *RedisClient) LRem(ctx context.Context, key string, count int64, value interface{}) error {
	if err := r.client.LRem(ctx, key, count, value).Err(); err != nil {
		return fmt.Errorf("redis lrem %s: %w", key, err)
	}
	return nil
}

// LLen 获取队列长度
func (r *RedisClient) LLen(ctx context.Context, key string) (int64, error) {
	val, err := r.client.LLen(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("redis llen %s: %w", key, err)
	}
	return val, nil
}

// =========================================================================
// Hash 操作 (Worker 状态)
// =========================================================================

// HSet 设置 Hash 字段
func (r *RedisClient) HSet(ctx context.Context, key, field string, value interface{}) error {
	if err := r.client.HSet(ctx, key, field, value).Err(); err != nil {
		return fmt.Errorf("redis hset %s %s: %w", key, field, err)
	}
	return nil
}

// HGet 获取 Hash 字段
func (r *RedisClient) HGet(ctx context.Context, key, field string) (string, error) {
	val, err := r.client.HGet(ctx, key, field).Result()
	if err != nil && err != redis.Nil {
		return "", fmt.Errorf("redis hget %s %s: %w", key, field, err)
	}
	return val, err
}

// HGetAll 获取所有 Hash 字段
func (r *RedisClient) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	val, err := r.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("redis hgetall %s: %w", key, err)
	}
	return val, nil
}

// HDel 删除 Hash 字段
func (r *RedisClient) HDel(ctx context.Context, key string, fields ...string) error {
	if err := r.client.HDel(ctx, key, fields...).Err(); err != nil {
		return fmt.Errorf("redis hdel %s: %w", key, err)
	}
	return nil
}

// LRange 获取列表范围
func (r *RedisClient) LRange(ctx context.Context, key string, start, stop int64) ([]string, error) {
	val, err := r.client.LRange(ctx, key, start, stop).Result()
	if err != nil {
		return nil, fmt.Errorf("redis lrange %s: %w", key, err)
	}
	return val, nil
}

// ZAdd adds members to a sorted set
func (r *RedisClient) ZAdd(ctx context.Context, key string, members ...*redis.Z) error {
	if err := r.client.ZAdd(ctx, key, members...).Err(); err != nil {
		return fmt.Errorf("redis zadd %s: %w", key, err)
	}
	return nil
}

// ZRem removes members from a sorted set
func (r *RedisClient) ZRem(ctx context.Context, key string, members ...interface{}) error {
	if err := r.client.ZRem(ctx, key, members...).Err(); err != nil {
		return fmt.Errorf("redis zrem %s: %w", key, err)
	}
	return nil
}

// ZRangeByScore retrieves members by score range
func (r *RedisClient) ZRangeByScore(ctx context.Context, key string, opt *redis.ZRangeBy) ([]string, error) {
	val, err := r.client.ZRangeByScore(ctx, key, opt).Result()
	if err != nil {
		return nil, fmt.Errorf("redis zrangebyscore %s: %w", key, err)
	}
	return val, nil
}

// RequeueTask 原子地将任务从 processing 移动到 pending (通过事务)
func (r *RedisClient) RequeueTask(ctx context.Context, src, dst, value string) error {
	pipe := r.client.TxPipeline()
	// LREM count=1
	pipe.LRem(ctx, src, 1, value)
	// LPUSH
	pipe.LPush(ctx, dst, value)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis requeue task: %w", err)
	}
	return nil
}

// Eval 执行 Lua 脚本
func (r *RedisClient) Eval(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error) {
	val, err := r.client.Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return nil, fmt.Errorf("redis eval: %w", err)
	}
	return val, nil
}

// XAdd appends an entry to a stream
func (r *RedisClient) XAdd(ctx context.Context, args *redis.XAddArgs) (string, error) {
	val, err := r.client.XAdd(ctx, args).Result()
	if err != nil {
		return "", fmt.Errorf("redis xadd: %w", err)
	}
	return val, nil
}

// XGroupCreateMkStream creates a consumer group (mkstream)
func (r *RedisClient) XGroupCreateMkStream(ctx context.Context, stream, group, start string) error {
	if err := r.client.XGroupCreateMkStream(ctx, stream, group, start).Err(); err != nil {
		if err.Error() == "BUSYGROUP Consumer Group name already exists" {
			return nil
		}
		return fmt.Errorf("redis xgroup create: %w", err)
	}
	return nil
}

// XReadGroup reads entries from a stream for a group
func (r *RedisClient) XReadGroup(ctx context.Context, args *redis.XReadGroupArgs) ([]redis.XStream, error) {
	val, err := r.client.XReadGroup(ctx, args).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("redis xreadgroup: %w", err)
	}
	return val, nil
}

// XAck acknowledges stream entries
func (r *RedisClient) XAck(ctx context.Context, stream, group string, ids ...string) error {
	if err := r.client.XAck(ctx, stream, group, ids...).Err(); err != nil {
		return fmt.Errorf("redis xack: %w", err)
	}
	return nil
}

// Subscribe 订阅频道
func (r *RedisClient) Subscribe(ctx context.Context, channels ...string) *redis.PubSub {
	return r.client.Subscribe(ctx, channels...)
}
