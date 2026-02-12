/**
 * @file redis.go
 * @brief Redis 客户端封装
 * 
 * ===========================================================================
 * Redis 数据结构应用 (面试必备)
 * ===========================================================================
 * 
 * 1. String: 缓存判题结果
 *    - SET key value EX seconds
 *    - 设置过期时间防止无限增长
 * 
 * 2. List: 任务队列
 *    - LPUSH: 入队 (O(1))
 *    - BRPOPLPUSH: 阻塞弹出 + 确认队列 (可靠投递)
 *    - LRANGE: 查看队列内容
 * 
 * 3. Hash: 存储 Worker 状态
 *    - HSET: 设置字段
 *    - HGETALL: 获取所有字段
 * 
 * 4. 发布订阅 vs List:
 *    - Pub/Sub: 广播模型，消息不持久化
 *    - List: 点对点模型，消息持久化
 *    - OJ 场景使用 List 更可靠
 * 
 * 5. 可靠队列模式 (BRPOPLPUSH):
 *    pending -> (BRPOPLPUSH) -> processing
 *    处理成功 -> LREM processing
 *    处理失败 -> 超时检测 -> 重新入队 pending
 */
package repository

import (
	"context"
	"time"

	"github.com/go-redis/redis/v8"
)

// RedisClient 封装 Redis 操作
type RedisClient struct {
	client *redis.Client
}

// NewRedisClient 创建 Redis 客户端
func NewRedisClient(addr string) *RedisClient {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     "",        // 无密码
		DB:           0,         // 默认 DB
		PoolSize:     10,        // 连接池大小
		MinIdleConns: 3,         // 最小空闲连接
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})

	return &RedisClient{client: client}
}

// Ping 测试连接
func (r *RedisClient) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
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
	return result, err
}

// Set 设置值
func (r *RedisClient) Set(ctx context.Context, key, value string, expiration time.Duration) error {
	return r.client.Set(ctx, key, value, expiration).Err()
}

// Del 删除键
func (r *RedisClient) Del(ctx context.Context, keys ...string) error {
	return r.client.Del(ctx, keys...).Err()
}

// Incr 自增
func (r *RedisClient) Incr(ctx context.Context, key string) (int64, error) {
	return r.client.Incr(ctx, key).Result()
}

// Expire 设置过期时间
func (r *RedisClient) Expire(ctx context.Context, key string, expiration time.Duration) (bool, error) {
	return r.client.Expire(ctx, key, expiration).Result()
}

// =========================================================================
// List 操作 (任务队列)
// =========================================================================

// LPush 左侧入队
func (r *RedisClient) LPush(ctx context.Context, key string, values ...interface{}) error {
	return r.client.LPush(ctx, key, values...).Err()
}

// RPop 右侧出队
func (r *RedisClient) RPop(ctx context.Context, key string) (string, error) {
	return r.client.RPop(ctx, key).Result()
}

// BRPop 阻塞右侧出队
func (r *RedisClient) BRPop(ctx context.Context, timeout time.Duration, keys ...string) ([]string, error) {
	result, err := r.client.BRPop(ctx, timeout, keys...).Result()
	if err == redis.Nil {
		return nil, nil
	}
	return result, err
}

// BRPopLPush 阻塞弹出并推入另一队列 (可靠投递)
func (r *RedisClient) BRPopLPush(ctx context.Context, source, destination string, timeout time.Duration) (string, error) {
	return r.client.BRPopLPush(ctx, source, destination, timeout).Result()
}

// LRem 移除队列中的元素
func (r *RedisClient) LRem(ctx context.Context, key string, count int64, value interface{}) error {
	return r.client.LRem(ctx, key, count, value).Err()
}

// LLen 获取队列长度
func (r *RedisClient) LLen(ctx context.Context, key string) (int64, error) {
	return r.client.LLen(ctx, key).Result()
}

// =========================================================================
// Hash 操作 (Worker 状态)
// =========================================================================

// HSet 设置 Hash 字段
func (r *RedisClient) HSet(ctx context.Context, key, field string, value interface{}) error {
	return r.client.HSet(ctx, key, field, value).Err()
}

// HGet 获取 Hash 字段
func (r *RedisClient) HGet(ctx context.Context, key, field string) (string, error) {
	return r.client.HGet(ctx, key, field).Result()
}

// HGetAll 获取所有 Hash 字段
func (r *RedisClient) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return r.client.HGetAll(ctx, key).Result()
}

// HDel 删除 Hash 字段
func (r *RedisClient) HDel(ctx context.Context, key string, fields ...string) error {
	return r.client.HDel(ctx, key, fields...).Err()
}

// LRange 获取列表范围
func (r *RedisClient) LRange(ctx context.Context, key string, start, stop int64) ([]string, error) {
	return r.client.LRange(ctx, key, start, stop).Result()
}

// RequeueTask 原子地将任务从 processing 移动到 pending (通过事务)
func (r *RedisClient) RequeueTask(ctx context.Context, src, dst, value string) error {
	pipe := r.client.TxPipeline()
	// LREM count=1
	pipe.LRem(ctx, src, 1, value)
	// LPUSH
	pipe.LPush(ctx, dst, value)
	_, err := pipe.Exec(ctx)
	return err
}

// Eval 执行 Lua 脚本
func (r *RedisClient) Eval(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error) {
	return r.client.Eval(ctx, script, keys, args...).Result()
}

// Subscribe 订阅频道
func (r *RedisClient) Subscribe(ctx context.Context, channels ...string) *redis.PubSub {
	return r.client.Subscribe(ctx, channels...)
}
