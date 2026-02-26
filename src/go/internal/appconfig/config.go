package appconfig

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	API           APIConfig           `yaml:"api"`
	Scheduler     SchedulerConfig     `yaml:"scheduler"`
	Worker        WorkerConfig        `yaml:"worker"`
	Observability ObservabilityConfig `yaml:"observability"`
	Redis         RedisConfig         `yaml:"redis"`
	Postgres      PostgresConfig      `yaml:"postgres"`

	// Legacy sections for C++ judge core and previous Go worker fallback wiring.
	Server ServerConfig `yaml:"server"`
	Path   PathConfig   `yaml:"path"`
}

type ServerConfig struct {
	Port     int `yaml:"port"`
	PoolSize int `yaml:"pool_size"`
}

type PathConfig struct {
	WorkspaceRoot string `yaml:"workspace_root"`
	CompilerBin   string `yaml:"compiler_bin"`
}

type ObservabilityConfig struct {
	ServiceName string `yaml:"service_name"`
	InstanceID  string `yaml:"instance_id"`
}

type TLSConfig struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
	CA   string `yaml:"ca"`
}

type MinIOConfig struct {
	Endpoint  string `yaml:"endpoint"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	Bucket    string `yaml:"bucket"`
	Secure    *bool  `yaml:"secure"`
}

type APIConfig struct {
	Port               int           `yaml:"port"`
	GinMode            string        `yaml:"gin_mode"`
	DatabaseURL        string        `yaml:"database_url"`
	RedisURL           string        `yaml:"redis_url"`
	MinIO              MinIOConfig   `yaml:"minio"`
	Auth               AuthConfig    `yaml:"auth"`
	Limits             APILimits     `yaml:"limits"`
	Metrics            Metrics       `yaml:"metrics"`
	ShutdownTimeoutSec int           `yaml:"shutdown_timeout_sec"`
	Stream             StreamConfig  `yaml:"stream"`
	TrustedProxies     []string      `yaml:"trusted_proxies"`
	CORSAllowedOrigins []string      `yaml:"cors_allowed_origins"`
	MetricsToken       string        `yaml:"metrics_token"`
	Timeouts           APITimeouts   `yaml:"timeouts"`
	Outbox             APIOutboxConf `yaml:"outbox"`
}

type APITimeouts struct {
	ReadHeaderSec int `yaml:"read_header_sec"`
	ReadSec       int `yaml:"read_sec"`
	WriteSec      int `yaml:"write_sec"`
	IdleSec       int `yaml:"idle_sec"`
}

type APIOutboxConf struct {
	Enabled            *bool `yaml:"enabled"`
	DispatchIntervalMs int   `yaml:"dispatch_interval_ms"`
	DispatchBatchSize  int   `yaml:"dispatch_batch_size"`
	RetryBaseMs        int   `yaml:"retry_base_ms"`
	RetryMaxMs         int   `yaml:"retry_max_ms"`
}

type AuthConfig struct {
	JWTSecret            string      `yaml:"jwt_secret"`
	JWTExpireHours       int         `yaml:"jwt_expire_hours"`
	OAuthStateTTLSeconds int         `yaml:"oauth_state_ttl_sec"`
	AdminUsers           []string    `yaml:"admin_users"`
	OAuth                OAuthConfig `yaml:"oauth"`
}

type OAuthConfig struct {
	GitHub GitHubOAuthConfig `yaml:"github"`
}

type GitHubOAuthConfig struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	RedirectURL  string `yaml:"redirect_url"`
}

type APILimits struct {
	SubmitBodyMaxBytes   int64           `yaml:"submit_body_max_bytes"`
	ProblemZipMaxBytes   int64           `yaml:"problem_zip_max_bytes"`
	SubmitCodeMaxBytes   int64           `yaml:"submit_code_max_bytes"`
	DefaultTimeLimitMs   int             `yaml:"default_time_limit_ms"`
	MaxTimeLimitMs       int             `yaml:"max_time_limit_ms"`
	DefaultMemoryLimitKb int             `yaml:"default_memory_limit_kb"`
	MaxMemoryLimitKb     int             `yaml:"max_memory_limit_kb"`
	InflightTTLSec       int             `yaml:"inflight_ttl_sec"`
	RateLimit            RateLimitConfig `yaml:"rate_limit"`
	AuthRateLimit        RateLimitConfig `yaml:"auth_rate_limit"`
	SubmitRateLimit      RateLimitConfig `yaml:"submit_rate_limit"`
	SubmitMaxInflight    int             `yaml:"submit_max_inflight"`
	ProblemDefaults      ProblemDefaults `yaml:"problem_defaults"`
}

type RateLimitConfig struct {
	IPLimit   int `yaml:"ip_limit"`
	UserLimit int `yaml:"user_limit"`
	WindowSec int `yaml:"window_sec"`
}

type ProblemDefaults struct {
	TimeLimitMs   int `yaml:"time_limit_ms"`
	MemoryLimitMB int `yaml:"memory_limit_mb"`
}

type SchedulerConfig struct {
	RedisURL      string                      `yaml:"redis_url"`
	DatabaseURL   string                      `yaml:"database_url"`
	SchedulerID   string                      `yaml:"scheduler_id"`
	Metrics       Metrics                     `yaml:"metrics"`
	MetricsPort   int                         `yaml:"metrics_port"`
	MetricsPollMs int                         `yaml:"metrics_poll_ms"`
	ControlPlane  SchedulerControlPlaneConf   `yaml:"control_plane"`
	Workers       SchedulerWorkerDiscoverConf `yaml:"workers"`
}

type SchedulerControlPlaneConf struct {
	RepairEnabled    bool  `yaml:"repair_enabled"`
	RepairIntervalMs int   `yaml:"repair_interval_ms"`
	RepairBatchSize  int   `yaml:"repair_batch_size"`
	RepairMinAgeSec  int   `yaml:"repair_min_age_sec"`
	GCEnabled        bool  `yaml:"gc_enabled"`
	GCIntervalMs     int   `yaml:"gc_interval_ms"`
	StreamTrimMaxLen int64 `yaml:"stream_trim_maxlen"`
	DBRetentionDays  int   `yaml:"db_retention_days"`
	DBDeleteEnabled  bool  `yaml:"db_delete_enabled"`
	DBDeleteBatch    int   `yaml:"db_delete_batch_size"`
}

type SchedulerWorkerDiscoverConf struct {
	Addresses      []string `yaml:"addresses"`
	ProbeTimeoutMs int      `yaml:"probe_timeout_ms"`
}

type WorkerConfig struct {
	ID                string               `yaml:"id"`
	Addr              string               `yaml:"addr"`
	Port              int                  `yaml:"port"`
	RedisURL          string               `yaml:"redis_url"`
	DatabaseURL       string               `yaml:"database_url"`
	Stream            WorkerStreamConfig   `yaml:"stream"`
	MinIO             MinIOConfig          `yaml:"minio"`
	Workspace         string               `yaml:"workspace"`
	JudgerBin         string               `yaml:"judger_bin"`
	JudgerConfig      string               `yaml:"judger_config"`
	PoolSize          int                  `yaml:"pool_size"`
	KeepWorkdir       *bool                `yaml:"keep_workdir"`
	Timeouts          WorkerTimeouts       `yaml:"timeouts"`
	TestcaseCache     WorkerCache          `yaml:"testcase_cache"`
	UnzipLimits       UnzipLimits          `yaml:"unzip_limits"`
	ResultTTLSec      int                  `yaml:"result_ttl_sec"`
	CheckerTimeoutMs  int                  `yaml:"checker_timeout_ms"`
	CleanupTimeoutSec int                  `yaml:"cleanup_timeout_sec"`
	AllowHostChecker  *bool                `yaml:"allow_host_checker"`
	RequireCgroupsV2  *bool                `yaml:"require_cgroups_v2"`
	Metrics           Metrics              `yaml:"metrics"`
	MetricsPort       int                  `yaml:"metrics_port"`
	Executor          WorkerExecutorConfig `yaml:"executor"`
}

type WorkerExecutorConfig struct {
	StdoutLimitBytes int64  `yaml:"stdout_limit_bytes"`
	StderrLimitBytes int64  `yaml:"stderr_limit_bytes"`
	CmdCgroupRoot    string `yaml:"cmd_cgroup_root"`
}

type WorkerStreamConfig struct {
	StreamKey           string `yaml:"stream_key"`
	Group               string `yaml:"group"`
	Consumer            string `yaml:"consumer"`
	ReadCount           int    `yaml:"read_count"`
	BlockMs             int    `yaml:"block_ms"`
	LeaseSec            int    `yaml:"lease_sec"`
	HeartbeatSec        int    `yaml:"heartbeat_sec"`
	ReclaimIntervalSec  int    `yaml:"reclaim_interval_sec"`
	ReclaimCount        int    `yaml:"reclaim_count"`
	ReclaimGraceSec     int    `yaml:"reclaim_grace_sec"`
	LogThrottleSec      int    `yaml:"log_throttle_sec"`
	BackpressureSleepMs int    `yaml:"backpressure_sleep_ms"`
}

type WorkerTimeouts struct {
	CompileMs    int `yaml:"compile_ms"`
	ExecBufferMs int `yaml:"exec_buffer_ms"`
	DownloadMs   int `yaml:"download_ms"`
	UnzipMs      int `yaml:"unzip_ms"`
}

type WorkerCache struct {
	Max    int `yaml:"max"`
	TTLSec int `yaml:"ttl_sec"`
}

type UnzipLimits struct {
	MaxBytes     int64 `yaml:"max_bytes"`
	MaxFiles     int   `yaml:"max_files"`
	MaxFileBytes int64 `yaml:"max_file_bytes"`
}

type Metrics struct {
	ServiceName string `yaml:"service_name"`
	InstanceID  string `yaml:"instance_id"`
}

type StreamConfig struct {
	StreamKey        string `yaml:"stream_key"`
	StreamMaxLen     int64  `yaml:"stream_maxlen"`
	JobPayloadTTLSec int    `yaml:"job_payload_ttl_sec"`
}

type RedisConfig struct {
	PoolSize       int    `yaml:"pool_size"`
	MinIdleConns   int    `yaml:"min_idle_conns"`
	DialTimeoutMs  int    `yaml:"dial_timeout_ms"`
	ReadTimeoutMs  int    `yaml:"read_timeout_ms"`
	WriteTimeoutMs int    `yaml:"write_timeout_ms"`
	Password       string `yaml:"password"`
	TLSEnabled     *bool  `yaml:"tls_enabled"`
	KeyPrefix      string `yaml:"key_prefix"`
	KeyEnv         string `yaml:"key_env"`
}

type PostgresConfig struct {
	MaxConns           int `yaml:"max_conns"`
	MinConns           int `yaml:"min_conns"`
	MaxConnLifetimeMin int `yaml:"max_conn_lifetime_min"`
	MaxConnIdleMin     int `yaml:"max_conn_idle_min"`
}

func ResolveConfigPath() string {
	if v := os.Getenv("APP_CONFIG"); v != "" {
		return v
	}
	if _, err := os.Stat("config.yaml"); err == nil {
		return "config.yaml"
	}
	if _, err := os.Stat("/app/config.yaml"); err == nil {
		return "/app/config.yaml"
	}
	return ""
}

func Load() (*Config, string, error) {
	path := ResolveConfigPath()
	if path == "" {
		return nil, "", fmt.Errorf("未找到配置文件，请设置 APP_CONFIG 或提供 config.yaml")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, path, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, path, err
	}
	return &cfg, path, nil
}

func ApplyRuntimeEnvForAPI(cfg *Config) {
	if cfg == nil {
		return
	}
	applySharedRuntimeEnv(cfg)

	setEnvInt("PORT", cfg.API.Port)
	setEnv("GIN_MODE", cfg.API.GinMode)
	setEnv("DATABASE_URL", cfg.API.DatabaseURL)
	setEnv("REDIS_URL", cfg.API.RedisURL)
	setMinIOEnv(cfg.API.MinIO)

	setEnv("JWT_SECRET", cfg.API.Auth.JWTSecret)
	setEnvInt("JWT_EXPIRE_HOURS", cfg.API.Auth.JWTExpireHours)
	setEnvInt("OAUTH_STATE_TTL_SEC", cfg.API.Auth.OAuthStateTTLSeconds)
	setEnv("ADMIN_USERS", joinCSV(cfg.API.Auth.AdminUsers))
	setEnv("GITHUB_CLIENT_ID", cfg.API.Auth.OAuth.GitHub.ClientID)
	setEnv("GITHUB_CLIENT_SECRET", cfg.API.Auth.OAuth.GitHub.ClientSecret)
	setEnv("GITHUB_REDIRECT_URL", cfg.API.Auth.OAuth.GitHub.RedirectURL)

	setEnvInt64("SUBMIT_BODY_MAX_BYTES", cfg.API.Limits.SubmitBodyMaxBytes)
	setEnvInt64("PROBLEM_ZIP_MAX_BYTES", cfg.API.Limits.ProblemZipMaxBytes)
	setEnvInt64("SUBMIT_CODE_MAX_BYTES", cfg.API.Limits.SubmitCodeMaxBytes)
	setEnvInt("SUBMIT_DEFAULT_TIME_LIMIT_MS", cfg.API.Limits.DefaultTimeLimitMs)
	setEnvInt("SUBMIT_MAX_TIME_LIMIT_MS", cfg.API.Limits.MaxTimeLimitMs)
	setEnvInt("SUBMIT_DEFAULT_MEMORY_LIMIT_KB", cfg.API.Limits.DefaultMemoryLimitKb)
	setEnvInt("SUBMIT_MAX_MEMORY_LIMIT_KB", cfg.API.Limits.MaxMemoryLimitKb)
	setEnvInt("SUBMIT_INFLIGHT_TTL_SEC", cfg.API.Limits.InflightTTLSec)
	legacyRateLimit := normalizeRateLimitConfig(cfg.API.Limits.RateLimit, RateLimitConfig{
		IPLimit:   60,
		UserLimit: 120,
		WindowSec: 60,
	})
	authRateLimit := normalizeRateLimitConfig(cfg.API.Limits.AuthRateLimit, legacyRateLimit)
	submitRateLimit := normalizeRateLimitConfig(cfg.API.Limits.SubmitRateLimit, legacyRateLimit)

	submitMaxInflight := cfg.API.Limits.SubmitMaxInflight
	if submitMaxInflight <= 0 {
		if strings.EqualFold(strings.TrimSpace(cfg.Redis.KeyEnv), "dev") {
			submitMaxInflight = 5000
		} else {
			submitMaxInflight = 500
		}
	}

	// Legacy unified rate-limit envs remain for fallback compatibility.
	setEnvInt("RATE_LIMIT_IP_PER_WINDOW", legacyRateLimit.IPLimit)
	setEnvInt("RATE_LIMIT_USER_PER_WINDOW", legacyRateLimit.UserLimit)
	setEnvInt("RATE_LIMIT_WINDOW_SEC", legacyRateLimit.WindowSec)
	setEnvInt("AUTH_RATE_LIMIT_IP_PER_WINDOW", authRateLimit.IPLimit)
	setEnvInt("AUTH_RATE_LIMIT_USER_PER_WINDOW", authRateLimit.UserLimit)
	setEnvInt("AUTH_RATE_LIMIT_WINDOW_SEC", authRateLimit.WindowSec)
	setEnvInt("SUBMIT_RATE_LIMIT_IP_PER_WINDOW", submitRateLimit.IPLimit)
	setEnvInt("SUBMIT_RATE_LIMIT_USER_PER_WINDOW", submitRateLimit.UserLimit)
	setEnvInt("SUBMIT_RATE_LIMIT_WINDOW_SEC", submitRateLimit.WindowSec)
	setEnvInt("SUBMIT_MAX_INFLIGHT", submitMaxInflight)
	setEnvInt("PROBLEM_DEFAULT_TIME_LIMIT_MS", cfg.API.Limits.ProblemDefaults.TimeLimitMs)
	setEnvInt("PROBLEM_DEFAULT_MEMORY_LIMIT_MB", cfg.API.Limits.ProblemDefaults.MemoryLimitMB)

	setEnvInt("API_SHUTDOWN_TIMEOUT_SEC", cfg.API.ShutdownTimeoutSec)
	setEnv("TRUSTED_PROXIES", joinCSV(cfg.API.TrustedProxies))
	setEnv("CORS_ALLOWED_ORIGINS", joinCSV(cfg.API.CORSAllowedOrigins))
	setEnv("METRICS_TOKEN", strings.TrimSpace(cfg.API.MetricsToken))
	setEnvInt("API_READ_HEADER_TIMEOUT_SEC", cfg.API.Timeouts.ReadHeaderSec)
	setEnvInt("API_READ_TIMEOUT_SEC", cfg.API.Timeouts.ReadSec)
	setEnvInt("API_WRITE_TIMEOUT_SEC", cfg.API.Timeouts.WriteSec)
	setEnvInt("API_IDLE_TIMEOUT_SEC", cfg.API.Timeouts.IdleSec)

	setEnvBool("OUTBOX_ENABLED", boolValue(cfg.API.Outbox.Enabled, true))
	setEnvInt("OUTBOX_DISPATCH_INTERVAL_MS", cfg.API.Outbox.DispatchIntervalMs)
	setEnvInt("OUTBOX_DISPATCH_BATCH_SIZE", cfg.API.Outbox.DispatchBatchSize)
	setEnvInt("OUTBOX_RETRY_BASE_MS", cfg.API.Outbox.RetryBaseMs)
	setEnvInt("OUTBOX_RETRY_MAX_MS", cfg.API.Outbox.RetryMaxMs)

	setEnv("JOB_STREAM_KEY", cfg.API.Stream.StreamKey)
	setEnvInt64("JOB_STREAM_MAXLEN", cfg.API.Stream.StreamMaxLen)
	setEnvInt("JOB_PAYLOAD_TTL_SEC", cfg.API.Stream.JobPayloadTTLSec)

	setEnv("SERVICE_NAME", resolveServiceName(cfg.API.Metrics.ServiceName, cfg.Observability.ServiceName, "deep-oj-api"))
	setEnv("INSTANCE_ID", resolveInstanceID(cfg.API.Metrics.InstanceID, cfg.Observability.InstanceID))
}

func ApplyRuntimeEnvForScheduler(cfg *Config) {
	if cfg == nil {
		return
	}
	applySharedRuntimeEnv(cfg)

	setEnv("REDIS_URL", cfg.Scheduler.RedisURL)
	setEnv("DATABASE_URL", cfg.Scheduler.DatabaseURL)
	schedulerID := strings.TrimSpace(cfg.Scheduler.SchedulerID)
	setEnv("SCHEDULER_ID", schedulerID)
	setEnvInt("SCHEDULER_METRICS_PORT", cfg.Scheduler.MetricsPort)
	setEnvInt("SCHEDULER_METRICS_POLL_INTERVAL_MS", cfg.Scheduler.MetricsPollMs)
	setEnv("SERVICE_NAME", resolveServiceName(cfg.Scheduler.Metrics.ServiceName, cfg.Observability.ServiceName, "deep-oj-scheduler"))
	setEnv("INSTANCE_ID", resolveSchedulerInstanceID(cfg.Scheduler.Metrics.InstanceID, cfg.Observability.InstanceID, schedulerID))

	setEnv("JOB_STREAM_KEY", cfg.API.Stream.StreamKey)
	setEnvInt64("JOB_STREAM_MAXLEN", cfg.API.Stream.StreamMaxLen)
	setEnvInt("JOB_PAYLOAD_TTL_SEC", cfg.API.Stream.JobPayloadTTLSec)

	setEnvBool("SCHEDULER_REPAIR_ENABLED", cfg.Scheduler.ControlPlane.RepairEnabled)
	setEnvInt("SCHEDULER_REPAIR_INTERVAL_MS", cfg.Scheduler.ControlPlane.RepairIntervalMs)
	setEnvInt("SCHEDULER_REPAIR_BATCH_SIZE", cfg.Scheduler.ControlPlane.RepairBatchSize)
	setEnvInt("SCHEDULER_REPAIR_MIN_AGE_SEC", cfg.Scheduler.ControlPlane.RepairMinAgeSec)
	setEnvBool("SCHEDULER_GC_ENABLED", cfg.Scheduler.ControlPlane.GCEnabled)
	setEnvInt("SCHEDULER_GC_INTERVAL_MS", cfg.Scheduler.ControlPlane.GCIntervalMs)
	setEnvInt64("SCHEDULER_STREAM_TRIM_MAXLEN", cfg.Scheduler.ControlPlane.StreamTrimMaxLen)
	setEnvInt("SCHEDULER_DB_RETENTION_DAYS", cfg.Scheduler.ControlPlane.DBRetentionDays)
	setEnvBool("SCHEDULER_DB_DELETE_ENABLED", cfg.Scheduler.ControlPlane.DBDeleteEnabled)
	setEnvInt("SCHEDULER_DB_DELETE_BATCH_SIZE", cfg.Scheduler.ControlPlane.DBDeleteBatch)

	addresses := joinCSV(cfg.Scheduler.Workers.Addresses)
	if strings.TrimSpace(addresses) == "" {
		addr := strings.TrimSpace(cfg.Worker.Addr)
		if addr == "" && cfg.Worker.Port > 0 {
			addr = fmt.Sprintf("127.0.0.1:%d", cfg.Worker.Port)
		}
		addresses = addr
	}
	setEnv("WORKER_ADDRS", addresses)
	setEnv("WORKER_ADDR", firstCSV(addresses))
	setEnvInt("WORKER_PROBE_TIMEOUT_MS", cfg.Scheduler.Workers.ProbeTimeoutMs)
}

func ApplyRuntimeEnvForWorker(cfg *Config) {
	if cfg == nil {
		return
	}
	applySharedRuntimeEnv(cfg)

	wcfg := cfg.Worker
	if wcfg.Port == 0 && cfg.Server.Port > 0 {
		wcfg.Port = cfg.Server.Port
	}
	if wcfg.PoolSize == 0 && cfg.Server.PoolSize > 0 {
		wcfg.PoolSize = cfg.Server.PoolSize
	}
	if strings.TrimSpace(wcfg.Workspace) == "" {
		wcfg.Workspace = strings.TrimSpace(cfg.Path.WorkspaceRoot)
	}

	setEnv("WORKER_ID", wcfg.ID)
	setEnv("WORKER_ADDR", wcfg.Addr)
	setEnvInt("WORKER_PORT", wcfg.Port)
	setEnv("REDIS_URL", wcfg.RedisURL)
	setEnv("DATABASE_URL", wcfg.DatabaseURL)
	setMinIOEnv(wcfg.MinIO)
	setEnv("WORKSPACE", wcfg.Workspace)
	setEnv("JUDGER_BIN", wcfg.JudgerBin)
	setEnv("JUDGER_CONFIG", wcfg.JudgerConfig)
	setEnvInt("WORKER_POOL_SIZE", wcfg.PoolSize)
	setEnvBool("KEEP_WORKDIR", boolValue(wcfg.KeepWorkdir, false))

	setEnvInt("COMPILE_TIMEOUT_MS", wcfg.Timeouts.CompileMs)
	setEnvInt("EXEC_TIMEOUT_BUFFER_MS", wcfg.Timeouts.ExecBufferMs)
	setEnvInt("DOWNLOAD_TIMEOUT_MS", wcfg.Timeouts.DownloadMs)
	setEnvInt("UNZIP_TIMEOUT_MS", wcfg.Timeouts.UnzipMs)
	setEnvInt("TESTCASE_CACHE_MAX", wcfg.TestcaseCache.Max)
	setEnvInt("TESTCASE_CACHE_TTL_SEC", wcfg.TestcaseCache.TTLSec)
	setEnvInt64("UNZIP_MAX_BYTES", wcfg.UnzipLimits.MaxBytes)
	setEnvInt("UNZIP_MAX_FILES", wcfg.UnzipLimits.MaxFiles)
	setEnvInt64("UNZIP_MAX_FILE_BYTES", wcfg.UnzipLimits.MaxFileBytes)

	setEnvInt("RESULT_TTL_SEC", wcfg.ResultTTLSec)
	setEnvInt("CHECKER_TIMEOUT_MS", wcfg.CheckerTimeoutMs)
	setEnvInt("CLEANUP_TIMEOUT_SEC", wcfg.CleanupTimeoutSec)
	setEnvBool("ALLOW_HOST_CHECKER", boolValue(wcfg.AllowHostChecker, false))
	setEnvBool("REQUIRE_CGROUPS_V2", boolValue(wcfg.RequireCgroupsV2, false))

	setEnv("JOB_STREAM_KEY", wcfg.Stream.StreamKey)
	setEnv("JOB_STREAM_GROUP", wcfg.Stream.Group)
	setEnv("JOB_STREAM_CONSUMER", wcfg.Stream.Consumer)
	setEnvInt("JOB_STREAM_READ_COUNT", wcfg.Stream.ReadCount)
	setEnvInt("JOB_STREAM_BLOCK_MS", wcfg.Stream.BlockMs)
	setEnvInt("JOB_LEASE_SEC", wcfg.Stream.LeaseSec)
	setEnvInt("JOB_HEARTBEAT_SEC", wcfg.Stream.HeartbeatSec)
	setEnvInt("JOB_RECLAIM_INTERVAL_SEC", wcfg.Stream.ReclaimIntervalSec)
	setEnvInt("JOB_RECLAIM_COUNT", wcfg.Stream.ReclaimCount)
	setEnvInt("JOB_RECLAIM_GRACE_SEC", wcfg.Stream.ReclaimGraceSec)
	setEnvInt("WORKER_LOG_THROTTLE_SEC", wcfg.Stream.LogThrottleSec)
	setEnvInt("WORKER_BACKPRESSURE_SLEEP_MS", wcfg.Stream.BackpressureSleepMs)

	setEnvInt64("JUDGE_STDOUT_LIMIT_BYTES", wcfg.Executor.StdoutLimitBytes)
	setEnvInt64("JUDGE_STDERR_LIMIT_BYTES", wcfg.Executor.StderrLimitBytes)
	setEnv("JUDGE_CMD_CGROUP_ROOT", strings.TrimSpace(wcfg.Executor.CmdCgroupRoot))

	setEnvInt("WORKER_METRICS_PORT", wcfg.MetricsPort)
	setEnv("SERVICE_NAME", resolveServiceName(wcfg.Metrics.ServiceName, cfg.Observability.ServiceName, "deep-oj-worker"))
	setEnv("INSTANCE_ID", resolveInstanceID(wcfg.Metrics.InstanceID, cfg.Observability.InstanceID))
}

func applySharedRuntimeEnv(cfg *Config) {
	setEnvInt("REDIS_POOL_SIZE", cfg.Redis.PoolSize)
	setEnvInt("REDIS_MIN_IDLE_CONNS", cfg.Redis.MinIdleConns)
	setEnvInt("REDIS_DIAL_TIMEOUT_MS", cfg.Redis.DialTimeoutMs)
	setEnvInt("REDIS_READ_TIMEOUT_MS", cfg.Redis.ReadTimeoutMs)
	setEnvInt("REDIS_WRITE_TIMEOUT_MS", cfg.Redis.WriteTimeoutMs)
	setEnv("REDIS_PASSWORD", cfg.Redis.Password)
	setEnvBool("REDIS_TLS", boolValue(cfg.Redis.TLSEnabled, false))
	keyPrefix := strings.TrimSpace(cfg.Redis.KeyPrefix)
	if keyPrefix == "" {
		keyPrefix = "deepoj"
	}
	setEnv("REDIS_KEY_PREFIX", keyPrefix)
	setEnv("REDIS_KEY_ENV", strings.TrimSpace(cfg.Redis.KeyEnv))

	setEnvInt("PG_MAX_CONNS", cfg.Postgres.MaxConns)
	setEnvInt("PG_MIN_CONNS", cfg.Postgres.MinConns)
	setEnvInt("PG_MAX_CONN_LIFETIME_MIN", cfg.Postgres.MaxConnLifetimeMin)
	setEnvInt("PG_MAX_CONN_IDLE_MIN", cfg.Postgres.MaxConnIdleMin)
}

func normalizeRateLimitConfig(cfg RateLimitConfig, fallback RateLimitConfig) RateLimitConfig {
	out := cfg
	if out.IPLimit <= 0 {
		out.IPLimit = fallback.IPLimit
	}
	if out.UserLimit <= 0 {
		out.UserLimit = fallback.UserLimit
	}
	if out.WindowSec <= 0 {
		out.WindowSec = fallback.WindowSec
	}
	return out
}

func setMinIOEnv(cfg MinIOConfig) {
	setEnv("MINIO_ENDPOINT", cfg.Endpoint)
	setEnv("MINIO_ACCESS_KEY", cfg.AccessKey)
	setEnv("MINIO_SECRET_KEY", cfg.SecretKey)
	setEnv("MINIO_BUCKET", cfg.Bucket)
	setEnvBool("MINIO_SECURE", minioSecure(cfg))
}

func minioSecure(cfg MinIOConfig) bool {
	if cfg.Secure != nil {
		return *cfg.Secure
	}
	endpoint := strings.ToLower(strings.TrimSpace(cfg.Endpoint))
	return strings.HasPrefix(endpoint, "https://")
}

func resolveServiceName(local, global, fallback string) string {
	if v := strings.TrimSpace(local); v != "" {
		return v
	}
	if v := strings.TrimSpace(global); v != "" {
		return v
	}
	return fallback
}

func resolveInstanceID(local, global string) string {
	if v := strings.TrimSpace(local); v != "" {
		return v
	}
	return strings.TrimSpace(global)
}

func resolveSchedulerInstanceID(local, global, schedulerID string) string {
	if v := resolveInstanceID(local, global); v != "" {
		return v
	}
	return strings.TrimSpace(schedulerID)
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func joinCSV(values []string) string {
	parts := make([]string, 0, len(values))
	for _, raw := range values {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		parts = append(parts, item)
	}
	return strings.Join(parts, ",")
}

func firstCSV(value string) string {
	for _, item := range strings.Split(value, ",") {
		v := strings.TrimSpace(item)
		if v != "" {
			return v
		}
	}
	return ""
}

func setEnv(key, value string) {
	_ = os.Setenv(key, value)
}

func setEnvInt(key string, value int) {
	_ = os.Setenv(key, strconv.Itoa(value))
}

func setEnvInt64(key string, value int64) {
	_ = os.Setenv(key, strconv.FormatInt(value, 10))
}

func setEnvBool(key string, value bool) {
	if value {
		_ = os.Setenv(key, "true")
		return
	}
	_ = os.Setenv(key, "false")
}

func SetEnvIfEmpty(key, value string) {
	if value == "" {
		return
	}
	if _, ok := os.LookupEnv(key); ok {
		return
	}
	_ = os.Setenv(key, value)
}

func SetEnvIfEmptyInt(key string, value int) {
	if value <= 0 {
		return
	}
	SetEnvIfEmpty(key, strconv.Itoa(value))
}

func SetEnvIfEmptyInt64(key string, value int64) {
	if value <= 0 {
		return
	}
	SetEnvIfEmpty(key, strconv.FormatInt(value, 10))
}

func SetEnvIfEmptyBool(key string, value *bool) {
	if value == nil {
		return
	}
	SetEnvIfEmpty(key, strconv.FormatBool(*value))
}

func SetEnvIfEmptySlice(key string, values []string) {
	if len(values) == 0 {
		return
	}
	SetEnvIfEmpty(key, strings.Join(values, ","))
}
