package repository

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
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
		return fmt.Errorf("Redis PING 失败: %w", err)
	}
	return nil
}

// Close 关闭连接
func (r *RedisClient) Close() error {
	return r.client.Close()
}

// String 操作 (缓存)

// Get 获取值
func (r *RedisClient) Get(ctx context.Context, key string) (string, error) {
	result, err := r.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", err
	}
	if err != nil {
		return "", fmt.Errorf("Redis GET %s 失败: %w", key, err)
	}
	return result, nil
}

// Set 设置值
func (r *RedisClient) Set(ctx context.Context, key, value string, expiration time.Duration) error {
	if err := r.client.Set(ctx, key, value, expiration).Err(); err != nil {
		return fmt.Errorf("Redis SET %s 失败: %w", key, err)
	}
	return nil
}

// SetNX 在键不存在时设置值
func (r *RedisClient) SetNX(ctx context.Context, key, value string, expiration time.Duration) (bool, error) {
	val, err := r.client.SetNX(ctx, key, value, expiration).Result()
	if err != nil {
		return false, fmt.Errorf("Redis SETNX %s 失败: %w", key, err)
	}
	return val, nil
}

// Del 删除键
func (r *RedisClient) Del(ctx context.Context, keys ...string) error {
	if err := r.client.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("Redis DEL 失败: %w", err)
	}
	return nil
}

// Incr 自增
func (r *RedisClient) Incr(ctx context.Context, key string) (int64, error) {
	val, err := r.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("Redis INCR %s 失败: %w", key, err)
	}
	return val, nil
}

// Decr 自减
func (r *RedisClient) Decr(ctx context.Context, key string) (int64, error) {
	val, err := r.client.Decr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("Redis DECR %s 失败: %w", key, err)
	}
	return val, nil
}

// Expire 设置过期时间
func (r *RedisClient) Expire(ctx context.Context, key string, expiration time.Duration) (bool, error) {
	val, err := r.client.Expire(ctx, key, expiration).Result()
	if err != nil {
		return false, fmt.Errorf("Redis EXPIRE %s 失败: %w", key, err)
	}
	return val, nil
}

// Hash 操作 (Worker 状态)

// HSet 设置 Hash 字段
func (r *RedisClient) HSet(ctx context.Context, key, field string, value interface{}) error {
	if err := r.client.HSet(ctx, key, field, value).Err(); err != nil {
		return fmt.Errorf("Redis HSET %s %s 失败: %w", key, field, err)
	}
	return nil
}

// HGet 获取 Hash 字段
func (r *RedisClient) HGet(ctx context.Context, key, field string) (string, error) {
	val, err := r.client.HGet(ctx, key, field).Result()
	if err != nil && err != redis.Nil {
		return "", fmt.Errorf("Redis HGET %s %s 失败: %w", key, field, err)
	}
	return val, err
}

// HGetAll 获取所有 Hash 字段
func (r *RedisClient) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	val, err := r.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("Redis HGETALL %s 失败: %w", key, err)
	}
	return val, nil
}

// HDel 删除 Hash 字段
func (r *RedisClient) HDel(ctx context.Context, key string, fields ...string) error {
	if err := r.client.HDel(ctx, key, fields...).Err(); err != nil {
		return fmt.Errorf("Redis HDEL %s 失败: %w", key, err)
	}
	return nil
}

// ZAdd 向有序集合添加成员
func (r *RedisClient) ZAdd(ctx context.Context, key string, members ...*redis.Z) error {
	items := make([]redis.Z, 0, len(members))
	for _, member := range members {
		if member != nil {
			items = append(items, *member)
		}
	}
	if len(items) == 0 {
		return nil
	}
	if err := r.client.ZAdd(ctx, key, items...).Err(); err != nil {
		return fmt.Errorf("Redis ZADD %s 失败: %w", key, err)
	}
	return nil
}

// ZRem 从有序集合移除成员
func (r *RedisClient) ZRem(ctx context.Context, key string, members ...interface{}) error {
	if err := r.client.ZRem(ctx, key, members...).Err(); err != nil {
		return fmt.Errorf("Redis ZREM %s 失败: %w", key, err)
	}
	return nil
}

// ZRangeByScore 按分数范围读取成员
func (r *RedisClient) ZRangeByScore(ctx context.Context, key string, opt *redis.ZRangeBy) ([]string, error) {
	val, err := r.client.ZRangeByScore(ctx, key, opt).Result()
	if err != nil {
		return nil, fmt.Errorf("Redis ZRANGEBYSCORE %s 失败: %w", key, err)
	}
	return val, nil
}

// Eval 执行 Lua 脚本
func (r *RedisClient) Eval(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error) {
	val, err := r.client.Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return nil, fmt.Errorf("Redis EVAL 失败: %w", err)
	}
	return val, nil
}

// XAdd 向流追加一条消息
func (r *RedisClient) XAdd(ctx context.Context, args *redis.XAddArgs) (string, error) {
	val, err := r.client.XAdd(ctx, args).Result()
	if err != nil {
		return "", fmt.Errorf("Redis XADD 失败: %w", err)
	}
	return val, nil
}

// XGroupCreateMkStream 创建消费组（必要时自动创建流）
func (r *RedisClient) XGroupCreateMkStream(ctx context.Context, stream, group, start string) error {
	if err := r.client.XGroupCreateMkStream(ctx, stream, group, start).Err(); err != nil {
		if err.Error() == "BUSYGROUP Consumer Group name already exists" {
			return nil
		}
		return fmt.Errorf("Redis XGROUP CREATE 失败: %w", err)
	}
	return nil
}

// XReadGroup 为消费组读取流消息
func (r *RedisClient) XReadGroup(ctx context.Context, args *redis.XReadGroupArgs) ([]redis.XStream, error) {
	val, err := r.client.XReadGroup(ctx, args).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("Redis XREADGROUP 失败: %w", err)
	}
	return val, nil
}

// XAck 确认流消息
func (r *RedisClient) XAck(ctx context.Context, stream, group string, ids ...string) error {
	if err := r.client.XAck(ctx, stream, group, ids...).Err(); err != nil {
		return fmt.Errorf("Redis XACK 失败: %w", err)
	}
	return nil
}

// Subscribe 订阅频道
func (r *RedisClient) Subscribe(ctx context.Context, channels ...string) *redis.PubSub {
	return r.client.Subscribe(ctx, channels...)
}
