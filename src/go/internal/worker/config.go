package worker

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/d1guo/deep_oj/internal/appconfig"
	"github.com/google/uuid"
)

type Config struct {
	WorkerID              string
	WorkerAddr            string
	RedisURL              string
	DatabaseURL           string
	MinIOEndpoint         string
	MinIOAccess           string
	MinIOSecret           string
	MinIOBucket           string
	Workspace             string // For judge_engine
	JudgerBin             string // Path to judge_engine binary
	ConfigPath            string // Path to config.yaml for C++ core
	JobStreamKey          string
	JobStreamGroup        string
	JobStreamConsumer     string
	JobStreamReadCount    int
	JobStreamBlockMs      int
	JobLeaseSec           int
	JobHeartbeatSec       int
	JobReclaimIntervalSec int
	JobReclaimCount       int
	JobReclaimGraceSec    int

	CompileTimeoutMs    int
	ExecTimeoutBufferMs int
	DownloadTimeoutMs   int
	UnzipTimeoutMs      int
	KeepWorkdir         bool
	PoolSize            int
	TestcaseCacheMax    int
	TestcaseCacheTTL    int
	UnzipMaxBytes       int64
	UnzipMaxFiles       int
	UnzipMaxFileBytes   int64
	AllowHostChecker    bool
}

func LoadConfig() *Config {
	port := getEnvInt("WORKER_PORT", 50051)
	workerAddr := strings.TrimSpace(getEnv("WORKER_ADDR", ""))
	if workerAddr == "" {
		workerAddr = ":" + strconv.Itoa(port)
	}

	workspace := getEnv("WORKSPACE", "/tmp/deep_oj_workspace")
	defaultConfigPath := strings.TrimSpace(appconfig.ResolveConfigPath())

	workerID := getEnv("WORKER_ID", "")
	if workerID == "" {
		host, _ := os.Hostname()
		workerID = host + "-" + uuid.New().String()
	}
	streamConsumer := strings.TrimSpace(getEnv("JOB_STREAM_CONSUMER", ""))
	if streamConsumer == "" {
		workerIDEnv := strings.TrimSpace(getEnv("WORKER_ID", ""))
		if workerIDEnv != "" {
			streamConsumer = workerIDEnv
		} else {
			host, _ := os.Hostname()
			if host == "" {
				host = "worker"
			}
			streamConsumer = fmt.Sprintf("%s-%d", host, os.Getpid())
		}
	}

	cfg := &Config{
		WorkerID:              workerID,
		WorkerAddr:            workerAddr, // Derived from port
		RedisURL:              getEnv("REDIS_URL", "localhost:6379"),
		DatabaseURL:           getEnv("DATABASE_URL", ""),
		MinIOEndpoint:         getEnv("MINIO_ENDPOINT", "localhost:9000"),
		MinIOAccess:           getEnv("MINIO_ACCESS_KEY", "minioadmin"),
		MinIOSecret:           getEnv("MINIO_SECRET_KEY", "minioadmin"),
		MinIOBucket:           getEnv("MINIO_BUCKET", "deep-oj-problems"),
		Workspace:             workspace,
		JudgerBin:             getEnv("JUDGER_BIN", "/app/judge_engine"),
		ConfigPath:            getEnv("JUDGER_CONFIG", defaultConfigPath),
		JobStreamKey:          getEnv("JOB_STREAM_KEY", "deepoj:jobs"),
		JobStreamGroup:        getEnv("JOB_STREAM_GROUP", "deepoj:workers"),
		JobStreamConsumer:     streamConsumer,
		JobStreamReadCount:    getEnvInt("JOB_STREAM_READ_COUNT", 16),
		JobStreamBlockMs:      getEnvInt("JOB_STREAM_BLOCK_MS", 2000),
		JobLeaseSec:           getEnvInt("JOB_LEASE_SEC", 60),
		JobHeartbeatSec:       getEnvInt("JOB_HEARTBEAT_SEC", 10),
		JobReclaimIntervalSec: getEnvInt("JOB_RECLAIM_INTERVAL_SEC", 5),
		JobReclaimCount:       getEnvInt("JOB_RECLAIM_COUNT", 16),
		JobReclaimGraceSec:    getEnvInt("JOB_RECLAIM_GRACE_SEC", 15),

		CompileTimeoutMs:    getEnvInt("COMPILE_TIMEOUT_MS", 10000),
		ExecTimeoutBufferMs: getEnvInt("EXEC_TIMEOUT_BUFFER_MS", 500),
		DownloadTimeoutMs:   getEnvInt("DOWNLOAD_TIMEOUT_MS", 60000),
		UnzipTimeoutMs:      getEnvInt("UNZIP_TIMEOUT_MS", 30000),
		KeepWorkdir:         getEnvBool("KEEP_WORKDIR", false),
	}
	configuredPoolSize, configuredPoolSizeSet := loadConfiguredPoolSize()
	cfg.PoolSize = normalizeWorkerPoolSize(configuredPoolSize, configuredPoolSizeSet, runtime.NumCPU())
	cfg.TestcaseCacheMax = getEnvInt("TESTCASE_CACHE_MAX", 100)
	cfg.TestcaseCacheTTL = getEnvInt("TESTCASE_CACHE_TTL_SEC", 3600)
	cfg.UnzipMaxBytes = getEnvInt64("UNZIP_MAX_BYTES", 256*1024*1024)
	cfg.UnzipMaxFiles = getEnvInt("UNZIP_MAX_FILES", 2000)
	cfg.UnzipMaxFileBytes = getEnvInt64("UNZIP_MAX_FILE_BYTES", 64*1024*1024)
	cfg.AllowHostChecker = getEnvBool("ALLOW_HOST_CHECKER", false)

	return cfg
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
	if value, ok := os.LookupEnv(key); ok {
		if i, err := strconv.ParseInt(value, 10, 64); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if value, ok := os.LookupEnv(key); ok {
		if value == "1" || value == "true" || value == "TRUE" || value == "yes" || value == "YES" {
			return true
		}
		if value == "0" || value == "false" || value == "FALSE" || value == "no" || value == "NO" {
			return false
		}
	}
	return fallback
}

func loadConfiguredPoolSize() (int, bool) {
	if value, ok := os.LookupEnv("WORKER_POOL_SIZE"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			slog.Warn("WORKER_POOL_SIZE 解析失败，回退到 CPU 默认值", "value", value, "error", err)
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

func normalizeWorkerPoolSize(configured int, configuredSet bool, cpuCount int) int {
	if cpuCount < 1 {
		cpuCount = 1
	}
	if !configuredSet {
		return cpuCount
	}
	if configured < 1 {
		return cpuCount
	}
	if configured > cpuCount {
		configured = cpuCount
	}
	return configured
}
