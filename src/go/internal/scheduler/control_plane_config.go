package scheduler

import (
	"os"
	"strings"
)

const (
	defaultDispatchMode = "streams_only"

	defaultRepairEnabled    = false
	defaultRepairIntervalMs = 30000
	defaultRepairBatchSize  = 200
	defaultRepairMinAgeSec  = 60

	defaultGCEnabled         = false
	defaultGCIntervalMs      = 600000
	defaultStreamTrimMaxLen  = 200000
	defaultDBRetentionDays   = 14
	defaultDBDeleteEnabled   = false
	defaultDBDeleteBatchSize = 500

	defaultJobStreamKey     = "deepoj:jobs"
	defaultJobStreamMaxLen  = int64(200000)
	defaultJobPayloadTTLSec = 24 * 60 * 60
)

type ControlPlaneConfig struct {
	DispatchMode string

	RepairEnabled    bool
	RepairIntervalMs int
	RepairBatchSize  int
	RepairMinAgeSec  int

	GCEnabled        bool
	GCIntervalMs     int
	StreamTrimMaxLen int64
	DBRetentionDays  int
	DBDeleteEnabled  bool
	DBDeleteBatch    int

	JobStreamKey     string
	JobStreamMaxLen  int64
	JobPayloadTTLSec int
}

func LoadControlPlaneConfig() ControlPlaneConfig {
	streamKey := strings.TrimSpace(getEnvString("JOB_STREAM_KEY", defaultJobStreamKey))
	if streamKey == "" {
		streamKey = defaultJobStreamKey
	}

	streamMaxLen := int64(getEnvInt("JOB_STREAM_MAXLEN", int(defaultJobStreamMaxLen)))
	if streamMaxLen <= 0 {
		streamMaxLen = defaultJobStreamMaxLen
	}

	payloadTTLSec := getEnvInt("JOB_PAYLOAD_TTL_SEC", defaultJobPayloadTTLSec)
	if payloadTTLSec <= 0 {
		payloadTTLSec = defaultJobPayloadTTLSec
	}

	repairIntervalMs := getEnvInt("SCHEDULER_REPAIR_INTERVAL_MS", defaultRepairIntervalMs)
	if repairIntervalMs <= 0 {
		repairIntervalMs = defaultRepairIntervalMs
	}
	repairBatchSize := getEnvInt("SCHEDULER_REPAIR_BATCH_SIZE", defaultRepairBatchSize)
	if repairBatchSize <= 0 {
		repairBatchSize = defaultRepairBatchSize
	}
	repairMinAgeSec := getEnvInt("SCHEDULER_REPAIR_MIN_AGE_SEC", defaultRepairMinAgeSec)
	if repairMinAgeSec <= 0 {
		repairMinAgeSec = defaultRepairMinAgeSec
	}

	gcIntervalMs := getEnvInt("SCHEDULER_GC_INTERVAL_MS", defaultGCIntervalMs)
	if gcIntervalMs <= 0 {
		gcIntervalMs = defaultGCIntervalMs
	}
	trimMaxLen := int64(getEnvInt("SCHEDULER_STREAM_TRIM_MAXLEN", defaultStreamTrimMaxLen))
	if trimMaxLen <= 0 {
		trimMaxLen = defaultStreamTrimMaxLen
	}
	dbRetentionDays := getEnvInt("SCHEDULER_DB_RETENTION_DAYS", defaultDBRetentionDays)
	if dbRetentionDays < 0 {
		dbRetentionDays = defaultDBRetentionDays
	}
	dbDeleteBatch := getEnvInt("SCHEDULER_DB_DELETE_BATCH_SIZE", defaultDBDeleteBatchSize)
	if dbDeleteBatch <= 0 {
		dbDeleteBatch = defaultDBDeleteBatchSize
	}

	return ControlPlaneConfig{
		DispatchMode: defaultDispatchMode,

		RepairEnabled:    getEnvBool("SCHEDULER_REPAIR_ENABLED", defaultRepairEnabled),
		RepairIntervalMs: repairIntervalMs,
		RepairBatchSize:  repairBatchSize,
		RepairMinAgeSec:  repairMinAgeSec,

		GCEnabled:        getEnvBool("SCHEDULER_GC_ENABLED", defaultGCEnabled),
		GCIntervalMs:     gcIntervalMs,
		StreamTrimMaxLen: trimMaxLen,
		DBRetentionDays:  dbRetentionDays,
		DBDeleteEnabled:  getEnvBool("SCHEDULER_DB_DELETE_ENABLED", defaultDBDeleteEnabled),
		DBDeleteBatch:    dbDeleteBatch,

		JobStreamKey:     streamKey,
		JobStreamMaxLen:  streamMaxLen,
		JobPayloadTTLSec: payloadTTLSec,
	}
}

func getEnvString(key string, fallback string) string {
	if value, ok := lookupEnvTrimmed(key); ok {
		return value
	}
	return fallback
}

func lookupEnvTrimmed(key string) (string, bool) {
	value, ok := os.LookupEnv(key)
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}
