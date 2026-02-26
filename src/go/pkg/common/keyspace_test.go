package common

import "testing"

func TestNamespacedRedisKey_DefaultNamespace(t *testing.T) {
	t.Setenv("REDIS_KEY_PREFIX", "")
	t.Setenv("REDIS_KEY_ENV", "")

	if got := NamespacedRedisKey("task:payload:job-1"); got != "deepoj:task:payload:job-1" {
		t.Fatalf("unexpected namespaced key: %s", got)
	}
}

func TestNamespacedRedisKey_WithEnvNamespace(t *testing.T) {
	t.Setenv("REDIS_KEY_PREFIX", "deepoj")
	t.Setenv("REDIS_KEY_ENV", "dev")

	if got := NamespacedRedisKey("deepoj:jobs"); got != "deepoj:dev:jobs" {
		t.Fatalf("unexpected normalized key: %s", got)
	}
	if got := NamespacedRedisKey("oj:cache:abc"); got != "deepoj:dev:cache:abc" {
		t.Fatalf("unexpected legacy normalized key: %s", got)
	}
}
