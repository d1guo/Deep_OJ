package common

const (
	// Redis Keys
	QueuePending    = "queue:pending"
	QueueProcessing = "queue:processing"
	QueueDead       = "queue:dead"

	// Cache Keys Prefix
	CacheKeyPrefix    = "oj:cache:"
	ResultKeyPrefix   = "result:"
	InFlightKeyPrefix = "oj:inflight:"

	// Task Assignment Prefix
	TaskAssignmentPrefix      = "task:worker:"
	TaskPayloadPrefix         = "task:payload:"
	TaskProcessingStartPrefix = "task:processing_start:"
	TaskProcessingZSet        = "task:processing:zset"
	TaskRetryPrefix           = "task:retry:"
	ResultStream              = "stream:results"
	ResultStreamGroup         = "results-group"
	ResultStreamConsumer      = "scheduler-1"
	WorkerInflightPrefix      = "worker:inflight:"

	// Job Status
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusFinished   = "Finished" // Kept as specific string for compatibility
)
