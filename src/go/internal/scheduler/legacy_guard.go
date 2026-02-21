package scheduler

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

var forbiddenLegacyEnvKeys = []string{
	"QUEUE_BRPOP_TIMEOUT_SEC",
	"NO_WORKER_SLEEP_MS",
	"ASSIGNMENT_TTL_SEC",
	"PAYLOAD_TTL_SEC",
	"PROCESSING_START_TTL_SEC",
	"INFLIGHT_TTL_SEC",
	"ACK_PENDING_COUNT",
	"ACK_PENDING_BLOCK_MS",
	"ACK_NEW_COUNT",
	"ACK_NEW_BLOCK_MS",
	"SLOW_PATH_TICK_SEC",
	"SLOW_PATH_PROCESSING_CUTOFF_SEC",
	"PENDING_STALE_SEC",
	"SLOW_PATH_DB_SCAN_LIMIT",
	"WATCHDOG_INTERVAL_SEC",
	"DISPATCH_CONN_TIMEOUT_MS",
	"DISPATCH_RPC_TIMEOUT_MS",
	"DISPATCH_MAX_RETRIES",
	"DISPATCH_BACKOFF_BASE_MS",
	"MAX_RETRY",
	"RETRY_TTL_SEC",
}

// GuardLegacyDataPlane 作为第二道护栏，阻止 legacy 数据面相关配置被误启。
func GuardLegacyDataPlane() error {
	violations := make([]string, 0)
	for _, key := range forbiddenLegacyEnvKeys {
		value, ok := os.LookupEnv(key)
		if !ok {
			continue
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		violations = append(violations, key)
	}
	if len(violations) == 0 {
		return nil
	}
	sort.Strings(violations)
	return fmt.Errorf("检测到 legacy 数据面配置，拒绝启动: %s", strings.Join(violations, ","))
}
