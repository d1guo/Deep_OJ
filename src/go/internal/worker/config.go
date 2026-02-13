package worker

import (
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type Config struct {
	WorkerID      string
	WorkerAddr    string // gRPC listen addr
	EtcdEndpoints []string
	RedisURL      string
	MinIOEndpoint string
	MinIOAccess   string
	MinIOSecret   string
	MinIOBucket   string
	Workspace     string // For judge_engine
	JudgerBin     string // Path to judge_engine binary
	ConfigPath    string // Path to config.yaml for C++ core

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

// YAMLConfig 映射 config.yaml 结构
type YAMLConfig struct {
	Server struct {
		Port     int `yaml:"port"`
		PoolSize int `yaml:"pool_size"`
	} `yaml:"server"`
	Path struct {
		WorkspaceRoot string `yaml:"workspace_root"`
	} `yaml:"path"`
	// ... other fields if needed, currently Go worker mainly needs server & path
}

func LoadConfig() *Config {
	// 1. Load YAML (if exists)
	var yamlCfg YAMLConfig
	configPath := ""
	if _, err := os.Stat("config.yaml"); err == nil {
		configPath = "config.yaml"
		data, err := os.ReadFile(configPath)
		if err != nil {
			slog.Warn("Failed to read config.yaml", "error", err)
		} else {
			if err := yaml.Unmarshal(data, &yamlCfg); err != nil {
				slog.Warn("Failed to parse config.yaml", "error", err)
			}
		}
	} else {
		// Try absolute path if running from bin
		absPath := "/app/config.yaml"
		if _, err := os.Stat(absPath); err == nil {
			configPath = absPath
			data, err := os.ReadFile(absPath)
			if err == nil {
				yaml.Unmarshal(data, &yamlCfg)
			}
		}
	}

	// 2. Determine values (Env > YAML > Default)

	// Port: YAML > Env > Default
	port := yamlCfg.Server.Port
	if port == 0 {
		port = getEnvInt("WORKER_PORT", 50051)
	}
	workerAddr := getEnv("WORKER_ADDR", "")
	if workerAddr == "" {
		workerAddr = ":" + strconv.Itoa(port)
	}

	// Workspace: YAML > Env > Default
	workspace := yamlCfg.Path.WorkspaceRoot
	if workspace == "" {
		workspace = getEnv("WORKSPACE", "/tmp/deep_oj_workspace")
	}

	workerID := getEnv("WORKER_ID", "")
	if workerID == "" {
		host, _ := os.Hostname()
		workerID = host + "-" + uuid.New().String()
	}

	cfg := &Config{
		WorkerID:      workerID,
		WorkerAddr:    workerAddr, // Derived from port
		RedisURL:      getEnv("REDIS_URL", "localhost:6379"),
		MinIOEndpoint: getEnv("MINIO_ENDPOINT", "localhost:9000"),
		MinIOAccess:   getEnv("MINIO_ACCESS_KEY", "minioadmin"),
		MinIOSecret:   getEnv("MINIO_SECRET_KEY", "minioadmin"),
		MinIOBucket:   getEnv("MINIO_BUCKET", "deep-oj-problems"),
		Workspace:     workspace,
		JudgerBin:     getEnv("JUDGER_BIN", "/app/judge_engine"),
		ConfigPath:    getEnv("JUDGER_CONFIG", configPath),

		CompileTimeoutMs:    getEnvInt("COMPILE_TIMEOUT_MS", 10000),
		ExecTimeoutBufferMs: getEnvInt("EXEC_TIMEOUT_BUFFER_MS", 500),
		DownloadTimeoutMs:   getEnvInt("DOWNLOAD_TIMEOUT_MS", 60000),
		UnzipTimeoutMs:      getEnvInt("UNZIP_TIMEOUT_MS", 30000),
		KeepWorkdir:         getEnvBool("KEEP_WORKDIR", false),
	}
	cfg.PoolSize = getEnvInt("WORKER_POOL_SIZE", yamlCfg.Server.PoolSize)
	if cfg.PoolSize <= 0 {
		cfg.PoolSize = 4
	}
	cfg.TestcaseCacheMax = getEnvInt("TESTCASE_CACHE_MAX", 100)
	cfg.TestcaseCacheTTL = getEnvInt("TESTCASE_CACHE_TTL_SEC", 3600)
	cfg.UnzipMaxBytes = getEnvInt64("UNZIP_MAX_BYTES", 256*1024*1024)
	cfg.UnzipMaxFiles = getEnvInt("UNZIP_MAX_FILES", 2000)
	cfg.UnzipMaxFileBytes = getEnvInt64("UNZIP_MAX_FILE_BYTES", 64*1024*1024)
	cfg.AllowHostChecker = getEnvBool("ALLOW_HOST_CHECKER", false)

	if endpoints := getEnv("ETCD_ENDPOINTS", "localhost:2379"); endpoints != "" {
		parts := strings.Split(endpoints, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			cfg.EtcdEndpoints = append(cfg.EtcdEndpoints, p)
		}
		if len(cfg.EtcdEndpoints) == 0 {
			cfg.EtcdEndpoints = []string{"localhost:2379"}
		}
	}

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
