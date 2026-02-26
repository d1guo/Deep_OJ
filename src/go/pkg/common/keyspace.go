package common

import (
	"os"
	"strings"
)

const (
	defaultRedisKeyPrefix = "deepoj"
	legacyRedisKeyPrefix  = "oj"
)

func RedisKeyPrefix() string {
	prefix := strings.TrimSpace(os.Getenv("REDIS_KEY_PREFIX"))
	if prefix == "" {
		prefix = defaultRedisKeyPrefix
	}
	return prefix
}

func RedisKeyEnv() string {
	return strings.TrimSpace(os.Getenv("REDIS_KEY_ENV"))
}

func RedisKeyNamespace() string {
	prefix := RedisKeyPrefix()
	env := RedisKeyEnv()
	if env == "" {
		return prefix
	}
	return prefix + ":" + env
}

// NamespacedRedisKey normalizes keys into "<prefix>[:env]:<business-key>".
// It accepts legacy keys like "oj:*" or "deepoj:*" and rewrites them into
// the current runtime namespace.
func NamespacedRedisKey(raw string) string {
	raw = strings.TrimSpace(raw)
	ns := RedisKeyNamespace()
	if raw == "" {
		return ns
	}

	// Already in effective namespace.
	if raw == ns || strings.HasPrefix(raw, ns+":") {
		return raw
	}

	prefix := RedisKeyPrefix()
	env := RedisKeyEnv()

	// Normalize keys beginning with configured prefix (with or without env).
	if raw == prefix || strings.HasPrefix(raw, prefix+":") {
		remainder := strings.TrimPrefix(raw, prefix)
		remainder = strings.TrimPrefix(remainder, ":")
		if env != "" {
			if remainder == env {
				return ns
			}
			if strings.HasPrefix(remainder, env+":") {
				remainder = strings.TrimPrefix(remainder, env+":")
			}
		}
		remainder = strings.TrimPrefix(remainder, ":")
		if remainder == "" {
			return ns
		}
		return ns + ":" + remainder
	}

	// Normalize old "oj:*" style keys.
	if strings.HasPrefix(raw, legacyRedisKeyPrefix+":") {
		remainder := strings.TrimPrefix(raw, legacyRedisKeyPrefix+":")
		if remainder == "" {
			return ns
		}
		return ns + ":" + strings.TrimPrefix(remainder, ":")
	}

	return ns + ":" + strings.TrimPrefix(raw, ":")
}
