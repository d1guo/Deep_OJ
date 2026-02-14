package appconfig

import (
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

	// Keep compatibility with existing config.yaml sections
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
}

type APIConfig struct {
	Port               int          `yaml:"port"`
	GinMode            string       `yaml:"gin_mode"`
	DatabaseURL        string       `yaml:"database_url"`
	RedisURL           string       `yaml:"redis_url"`
	MinIO              MinIOConfig  `yaml:"minio"`
	Auth               AuthConfig   `yaml:"auth"`
	Limits             APILimits    `yaml:"limits"`
	Metrics            Metrics      `yaml:"metrics"`
	ShutdownTimeoutSec int          `yaml:"shutdown_timeout_sec"`
	Stream             StreamConfig `yaml:"stream"`
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
	EtcdEndpoints     []string          `yaml:"etcd_endpoints"`
	RedisURL          string            `yaml:"redis_url"`
	DatabaseURL       string            `yaml:"database_url"`
	WorkerCapacity    int               `yaml:"worker_capacity"`
	MaxRetry          int               `yaml:"max_retry"`
	RetryTTLSec       int               `yaml:"retry_ttl_sec"`
	SchedulerID       string            `yaml:"scheduler_id"`
	GRPCTLS           TLSConfig         `yaml:"grpc_tls"`
	Metrics           Metrics           `yaml:"metrics"`
	MetricsPort       int               `yaml:"metrics_port"`
	MetricsPollMs     int               `yaml:"metrics_poll_ms"`
	EtcdDialTimeoutMs int               `yaml:"etcd_dial_timeout_ms"`
	Queue             SchedulerQueue    `yaml:"queue"`
	AckListener       AckListenerConfig `yaml:"ack_listener"`
	SlowPath          SlowPathConfig    `yaml:"slow_path"`
	Watchdog          WatchdogConfig    `yaml:"watchdog"`
	Dispatch          DispatchConfig    `yaml:"dispatch"`
}

type SchedulerQueue struct {
	BRPopTimeoutSec       int `yaml:"brpop_timeout_sec"`
	NoWorkerSleepMs       int `yaml:"no_worker_sleep_ms"`
	AssignmentTTLSec      int `yaml:"assignment_ttl_sec"`
	PayloadTTLSec         int `yaml:"payload_ttl_sec"`
	ProcessingStartTTLSec int `yaml:"processing_start_ttl_sec"`
	InflightTTLSec        int `yaml:"inflight_ttl_sec"`
}

type AckListenerConfig struct {
	PendingCount   int `yaml:"pending_count"`
	PendingBlockMs int `yaml:"pending_block_ms"`
	NewCount       int `yaml:"new_count"`
	NewBlockMs     int `yaml:"new_block_ms"`
}

type SlowPathConfig struct {
	TickSec             int `yaml:"tick_sec"`
	ProcessingCutoffSec int `yaml:"processing_cutoff_sec"`
	PendingStaleSec     int `yaml:"pending_stale_sec"`
	DBScanLimit         int `yaml:"db_scan_limit"`
}

type WatchdogConfig struct {
	IntervalSec int `yaml:"interval_sec"`
}

type DispatchConfig struct {
	ConnTimeoutMs int `yaml:"conn_timeout_ms"`
	RPCTimeoutMs  int `yaml:"rpc_timeout_ms"`
	MaxRetries    int `yaml:"max_retries"`
	BackoffBaseMs int `yaml:"backoff_base_ms"`
}

type WorkerConfig struct {
	ID                     string             `yaml:"id"`
	Addr                   string             `yaml:"addr"`
	Port                   int                `yaml:"port"`
	EtcdEndpoints          []string           `yaml:"etcd_endpoints"`
	EtcdDialTimeoutMs      int                `yaml:"etcd_dial_timeout_ms"`
	EtcdLeaseTTLSec        int                `yaml:"etcd_lease_ttl_sec"`
	RedisURL               string             `yaml:"redis_url"`
	DatabaseURL            string             `yaml:"database_url"`
	Stream                 WorkerStreamConfig `yaml:"stream"`
	MinIO                  MinIOConfig        `yaml:"minio"`
	Workspace              string             `yaml:"workspace"`
	JudgerBin              string             `yaml:"judger_bin"`
	JudgerConfig           string             `yaml:"judger_config"`
	PoolSize               int                `yaml:"pool_size"`
	KeepWorkdir            *bool              `yaml:"keep_workdir"`
	Timeouts               WorkerTimeouts     `yaml:"timeouts"`
	TestcaseCache          WorkerCache        `yaml:"testcase_cache"`
	UnzipLimits            UnzipLimits        `yaml:"unzip_limits"`
	ResultTTLSec           int                `yaml:"result_ttl_sec"`
	CheckerTimeoutMs       int                `yaml:"checker_timeout_ms"`
	CleanupTimeoutSec      int                `yaml:"cleanup_timeout_sec"`
	ResultStreamMaxRetries int                `yaml:"result_stream_max_retries"`
	ResultStreamBackoffMs  int                `yaml:"result_stream_backoff_ms"`
	AllowHostChecker       *bool              `yaml:"allow_host_checker"`
	RequireCgroupsV2       *bool              `yaml:"require_cgroups_v2"`
	GRPCTLS                TLSConfig          `yaml:"grpc_tls"`
	Metrics                Metrics            `yaml:"metrics"`
	MetricsPort            int                `yaml:"metrics_port"`
}

type WorkerStreamConfig struct {
	StreamKey    string `yaml:"stream_key"`
	Group        string `yaml:"group"`
	Consumer     string `yaml:"consumer"`
	ReadCount    int    `yaml:"read_count"`
	BlockMs      int    `yaml:"block_ms"`
	LeaseSec     int    `yaml:"lease_sec"`
	HeartbeatSec int    `yaml:"heartbeat_sec"`
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
	PoolSize       int `yaml:"pool_size"`
	MinIdleConns   int `yaml:"min_idle_conns"`
	DialTimeoutMs  int `yaml:"dial_timeout_ms"`
	ReadTimeoutMs  int `yaml:"read_timeout_ms"`
	WriteTimeoutMs int `yaml:"write_timeout_ms"`
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
		return &Config{}, "", nil
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
